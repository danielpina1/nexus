package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Migrations create the event log. Append only; never edit released entries.
var Migrations = []string{
	`CREATE TABLE events (
		seq     INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT    NOT NULL DEFAULT '',
		run_id  TEXT    NOT NULL DEFAULT '',
		kind    TEXT    NOT NULL DEFAULT '',
		inbox   INTEGER NOT NULL DEFAULT 0,
		body    BLOB    NOT NULL,
		at      INTEGER NOT NULL
	)`,
	`CREATE INDEX events_task ON events (task_id, seq)`,
	`CREATE INDEX events_inbox ON events (inbox, seq)`,
	// Root of the task tree (team runs), for EventFilter.RootTaskIDs.
	`ALTER TABLE events ADD COLUMN root_task_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX events_root ON events (root_task_id, seq)`,
}

// ErrClosed is returned by Publish and Subscribe after Close.
var ErrClosed = errors.New("events: bus closed")

// Options tune a Bus. Zero values pick defaults.
type Options struct {
	// LiveBuffer is the per-subscriber buffer of live events. A subscriber
	// whose buffer is full is dropped (its channel closed) so publishers never
	// block; it should resubscribe from its last seq. Default 4096.
	LiveBuffer int
	// OutBuffer is the buffer of the channel returned by Subscribe. Default 256.
	OutBuffer int
	// ReplayBatch is the number of rows read per replay query. Default 500.
	ReplayBatch int
	// Now returns the publish timestamp (tests). Default time.Now.
	Now func() time.Time
	// Logger receives replay errors. Default slog.Default().
	Logger *slog.Logger
}

func (o *Options) defaults() {
	if o.LiveBuffer <= 0 {
		o.LiveBuffer = 4096
	}
	if o.OutBuffer <= 0 {
		o.OutBuffer = 256
	}
	if o.ReplayBatch <= 0 {
		o.ReplayBatch = 500
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Bus implements kernel.EventBus on a SQLite table with in-memory fan-out.
//
// Ordering: Publish holds mu while inserting and fanning out, so live delivery
// order equals seq order. Subscribe registers the subscriber and snapshots the
// head seq under the same lock: everything <= head is replayed from the
// database, everything > head arrives live. Delivery is additionally deduped
// by seq, so there are neither gaps nor duplicates at the boundary.
type Bus struct {
	db   *sql.DB
	opts Options

	mu     sync.Mutex
	last   int64
	subs   map[*subscriber]struct{}
	closed bool
}

type subscriber struct {
	filter kernel.EventFilter
	live   chan *nexusv1.Event
	done   bool // live closed; guarded by Bus.mu
}

// New creates a bus on db, applying migrations through store.
func New(st kernel.Store, opts Options) (*Bus, error) {
	if err := st.Migrate("events", Migrations); err != nil {
		return nil, err
	}
	opts.defaults()
	b := &Bus{db: st.DB(), opts: opts, subs: map[*subscriber]struct{}{}}
	if err := b.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&b.last); err != nil {
		return nil, fmt.Errorf("events: read head: %w", err)
	}
	return b, nil
}

// Publish implements kernel.EventBus. The input event is not modified; the
// returned copy carries the assigned seq and timestamp.
func (b *Bus) Publish(ctx context.Context, ev *nexusv1.Event) (*nexusv1.Event, error) {
	if ev == nil {
		return nil, errors.New("events: nil event")
	}
	out := proto.Clone(ev).(*nexusv1.Event)
	out.Seq = 0

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	now := b.opts.Now()
	out.At = timestamppb.New(now)
	body, err := proto.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("events: marshal: %w", err)
	}
	res, err := b.db.ExecContext(ctx,
		`INSERT INTO events (task_id, run_id, kind, inbox, body, at, root_task_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		out.TaskId, out.RunId, Kind(out), IsInbox(out), body, now.UnixNano(), out.RootTaskId)
	if err != nil {
		return nil, fmt.Errorf("events: insert: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("events: insert: %w", err)
	}
	out.Seq = seq
	b.last = seq
	b.fanout(out)
	return proto.Clone(out).(*nexusv1.Event), nil
}

// fanout delivers to live subscribers without blocking. Caller holds mu.
// Subscribers share the same immutable *Event; consumers must not modify it.
func (b *Bus) fanout(ev *nexusv1.Event) {
	for s := range b.subs {
		if !Match(s.filter, ev) {
			continue
		}
		select {
		case s.live <- ev:
		default:
			b.dropLocked(s) // too slow: drop rather than block publishers
		}
	}
}

func (b *Bus) dropLocked(s *subscriber) {
	if s.done {
		return
	}
	s.done = true
	close(s.live)
	delete(b.subs, s)
}

// LastSeq implements kernel.EventBus.
func (b *Bus) LastSeq(context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last, nil
}

// Subscribe implements kernel.EventBus. since < 0 is treated as 0 (replay
// everything); since beyond the head is treated as the head (live only). The returned channel is closed when ctx ends, the bus closes,
// or the subscriber falls too far behind; in the last case the consumer
// should resubscribe from the last seq it processed.
func (b *Bus) Subscribe(ctx context.Context, since int64, f kernel.EventFilter) (<-chan *nexusv1.Event, error) {
	if since < 0 {
		since = 0
	}
	s := &subscriber{filter: f, live: make(chan *nexusv1.Event, b.opts.LiveBuffer)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	head := b.last
	if since > head {
		// The client is ahead of the log (e.g. the hub database was reset):
		// stream from now on rather than silently skipping new events.
		since = head
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	out := make(chan *nexusv1.Event, b.opts.OutBuffer)
	go b.run(ctx, s, since, head, out)
	return out, nil
}

// run replays (since, head] from the database, then forwards live events.
func (b *Bus) run(ctx context.Context, s *subscriber, since, head int64, out chan<- *nexusv1.Event) {
	defer close(out)
	defer b.unsubscribe(s)

	send := func(ev *nexusv1.Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	last := since
	for last < head {
		batch, err := b.replay(ctx, last, head, s.filter)
		if err != nil {
			if ctx.Err() == nil {
				b.opts.Logger.Error("event replay failed", "after", last, "err", err)
			}
			return // close the stream; the client resubscribes
		}
		if len(batch) == 0 {
			break
		}
		for _, ev := range batch {
			if !send(ev) {
				return
			}
			last = ev.Seq
		}
		if len(batch) < b.opts.ReplayBatch {
			break
		}
	}
	if last < head {
		last = head
	}
	for {
		select {
		case ev, ok := <-s.live:
			if !ok {
				return
			}
			if ev.Seq <= last {
				continue // dedupe
			}
			if !send(ev) {
				return
			}
			last = ev.Seq
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bus) unsubscribe(s *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropLocked(s)
}

// replay reads at most ReplayBatch events with after < seq <= head that
// match f. Rows are fully read and closed before returning.
func (b *Bus) replay(ctx context.Context, after, head int64, f kernel.EventFilter) ([]*nexusv1.Event, error) {
	var (
		where = []string{"seq > ?", "seq <= ?"}
		args  = []any{after, head}
	)
	if f.InboxOnly {
		where = append(where, "inbox = 1")
	}
	var ors []string
	if len(f.TaskIDs) > 0 {
		ors = append(ors, "task_id IN (?"+strings.Repeat(",?", len(f.TaskIDs)-1)+")")
		for _, id := range f.TaskIDs {
			args = append(args, id)
		}
	}
	if len(f.RootTaskIDs) > 0 {
		in := "(?" + strings.Repeat(",?", len(f.RootTaskIDs)-1) + ")"
		ors = append(ors, "root_task_id IN "+in, "task_id IN "+in)
		for range 2 {
			for _, id := range f.RootTaskIDs {
				args = append(args, id)
			}
		}
	}
	if len(ors) > 0 {
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	args = append(args, b.opts.ReplayBatch)
	rows, err := b.db.QueryContext(ctx,
		`SELECT seq, body FROM events WHERE `+strings.Join(where, " AND ")+` ORDER BY seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nexusv1.Event
	for rows.Next() {
		var (
			seq  int64
			body []byte
		)
		if err := rows.Scan(&seq, &body); err != nil {
			return nil, err
		}
		ev := &nexusv1.Event{}
		if err := proto.Unmarshal(body, ev); err != nil {
			return nil, fmt.Errorf("events: decode seq %d: %w", seq, err)
		}
		ev.Seq = seq
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Close stops the bus: later Publish/Subscribe calls fail and every live
// subscription is closed. The database is owned by the store and left open.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for s := range b.subs {
		b.dropLocked(s)
	}
}

var _ kernel.EventBus = (*Bus)(nil)
