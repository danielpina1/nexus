package vault

import (
	"context"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Change feed: every created/updated/deleted/renamed note gets a revision
// number. WatchVault streams them; clients resume with since_rev. The ring
// buffer keeps the last feedSize changes; older since_rev values (or a
// restart: revisions start at the open time in microseconds, so they never
// repeat) get a RESYNC change telling the client to reload.

const feedSize = 2048

// Change is one entry of the change feed.
type Change struct {
	Rev     int64
	Kind    nexusv1.VaultChangeKind
	Path    string
	OldPath string
	ID      string
	At      time.Time
	Origin  string // api | agent | external | system
}

// Proto converts a change to its API form.
func (c Change) Proto() *nexusv1.VaultChange {
	out := &nexusv1.VaultChange{Rev: c.Rev, Kind: c.Kind, Path: c.Path, OldPath: c.OldPath, Id: c.ID, Origin: c.Origin}
	if !c.At.IsZero() {
		out.At = timestampOf(c.At)
	}
	return out
}

type feed struct {
	mu   sync.Mutex
	base int64 // first revision ever issued by this process
	next int64
	buf  []Change // ring, len ≤ feedSize, oldest first
	subs map[chan Change]struct{}
}

func newFeed(now time.Time) *feed {
	b := now.UnixMicro()
	return &feed{base: b, next: b, subs: map[chan Change]struct{}{}}
}

func (f *feed) publish(c Change) Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	c.Rev = f.next
	if len(f.buf) >= feedSize {
		copy(f.buf, f.buf[1:])
		f.buf = f.buf[:len(f.buf)-1]
	}
	f.buf = append(f.buf, c)
	for ch := range f.subs {
		select {
		case ch <- c:
		default:
			// Too slow: drop the subscriber; it resumes with since_rev.
			delete(f.subs, ch)
			close(ch)
		}
	}
	return c
}

// head returns the latest revision.
func (f *feed) head() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next
}

// subscribe returns the backlog after since (resync=true when it is
// incomplete) and a live channel. cancel must be called.
func (f *feed) subscribe(since int64) (backlog []Change, resync bool, ch chan Change, cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if since > 0 {
		oldest := f.next + 1
		if len(f.buf) > 0 {
			oldest = f.buf[0].Rev
		}
		if since < f.base || since+1 < oldest || since > f.next {
			resync = true
		}
		for _, c := range f.buf {
			if c.Rev > since {
				backlog = append(backlog, c)
			}
		}
	}
	ch = make(chan Change, 512)
	f.subs[ch] = struct{}{}
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			f.mu.Lock()
			if _, ok := f.subs[ch]; ok {
				delete(f.subs, ch)
				close(ch)
			}
			f.mu.Unlock()
		})
	}
	return backlog, resync, ch, cancel
}

// Watch streams changes after since (0 = live only) under prefix to fn
// until ctx ends or fn returns an error. A RESYNC change is sent first when
// since is too old.
func (v *Vault) WatchChanges(ctx context.Context, since int64, prefix string, fn func(Change) error) error {
	backlog, resync, ch, cancel := v.feed.subscribe(since)
	defer cancel()
	match := func(c Change) bool {
		return prefix == "" || c.Kind == nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RESYNC ||
			strings.HasPrefix(c.Path, prefix) || (c.OldPath != "" && strings.HasPrefix(c.OldPath, prefix))
	}
	if resync {
		if err := fn(Change{Rev: v.feed.head(), Kind: nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RESYNC, At: v.now(), Origin: "system"}); err != nil {
			return err
		}
	}
	for _, c := range backlog {
		if match(c) {
			if err := fn(c); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c, ok := <-ch:
			if !ok {
				// Dropped for being slow: tell the client to resync.
				return fn(Change{Rev: v.feed.head(), Kind: nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RESYNC, At: v.now(), Origin: "system"})
			}
			if match(c) {
				if err := fn(c); err != nil {
					return err
				}
			}
		}
	}
}

// FeedHead returns the current change revision.
func (v *Vault) FeedHead() int64 { return v.feed.head() }

type originKey struct{}

// WithOrigin tags changes made with ctx (api, agent, external, system).
func WithOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

func originOf(ctx context.Context) string {
	if s, _ := ctx.Value(originKey{}).(string); s != "" {
		return s
	}
	if runID(ctx) != "" {
		return "agent"
	}
	return "api"
}

func (v *Vault) emit(ctx context.Context, kind nexusv1.VaultChangeKind, path, oldPath, id string) {
	v.feed.publish(Change{Kind: kind, Path: path, OldPath: oldPath, ID: id, At: v.now(), Origin: originOf(ctx)})
}
