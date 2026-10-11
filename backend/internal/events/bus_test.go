package events

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/kernel"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newBus(t *testing.T, opts Options) *Bus {
	t.Helper()
	b, err := New(newStore(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func output(task, text string) *nexusv1.Event {
	return &nexusv1.Event{TaskId: task, Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: text}}}
}

func question(task string) *nexusv1.Event {
	return &nexusv1.Event{TaskId: task, Body: &nexusv1.Event_Question{Question: &nexusv1.Question{Text: "?"}}}
}

func publish(t *testing.T, b *Bus, evs ...*nexusv1.Event) {
	t.Helper()
	for _, ev := range evs {
		if _, err := b.Publish(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
}

// collect reads n events or fails after a timeout.
func collect(t *testing.T, ch <-chan *nexusv1.Event, n int) []*nexusv1.Event {
	t.Helper()
	var out []*nexusv1.Event
	timeout := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d/%d events", len(out), n)
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timeout after %d/%d events", len(out), n)
		}
	}
	return out
}

// expectQuiet asserts nothing arrives for a short while.
func expectQuiet(t *testing.T, ch <-chan *nexusv1.Event) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event seq=%d", ev.Seq)
		}
		t.Fatal("unexpected close")
	case <-time.After(50 * time.Millisecond):
	}
}

func seqs(evs []*nexusv1.Event) []int64 {
	out := make([]int64, len(evs))
	for i, ev := range evs {
		out[i] = ev.Seq
	}
	return out
}

func TestPublish(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := newBus(t, Options{Now: func() time.Time { return fixed }})
	in := output("tsk_1", "hi")
	in.Seq = 99
	for want := int64(1); want <= 3; want++ {
		got, err := b.Publish(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Seq != want {
			t.Fatalf("seq = %d, want %d", got.Seq, want)
		}
		if !got.At.AsTime().Equal(fixed) {
			t.Fatalf("at = %v", got.At.AsTime())
		}
		if got.GetRunOutput().GetText() != "hi" || got.TaskId != "tsk_1" {
			t.Fatalf("body lost: %v", got)
		}
	}
	if in.Seq != 99 || in.At != nil {
		t.Fatal("input event was modified")
	}
	if last, _ := b.LastSeq(context.Background()); last != 3 {
		t.Fatalf("LastSeq = %d", last)
	}
	if _, err := b.Publish(context.Background(), nil); err == nil {
		t.Fatal("nil event accepted")
	}
	var kind string
	var inbox bool
	if err := b.db.QueryRow(`SELECT kind, inbox FROM events WHERE seq = 1`).Scan(&kind, &inbox); err != nil {
		t.Fatal(err)
	}
	if kind != "run_output" || inbox {
		t.Fatalf("kind=%q inbox=%v", kind, inbox)
	}
}

func TestKindAndInbox(t *testing.T) {
	tests := []struct {
		ev    *nexusv1.Event
		kind  string
		inbox bool
	}{
		{&nexusv1.Event{}, "", false},
		{output("", "x"), "run_output", false},
		{question(""), "question", true},
		{&nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{}}}, "approval", true},
		{&nexusv1.Event{Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{}}}, "notice", true},
		{&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{}}}, "task_updated", false},
	}
	for _, tt := range tests {
		if got := Kind(tt.ev); got != tt.kind {
			t.Errorf("Kind = %q, want %q", got, tt.kind)
		}
		if got := IsInbox(tt.ev); got != tt.inbox {
			t.Errorf("IsInbox(%s) = %v, want %v", tt.kind, got, tt.inbox)
		}
	}
}

func TestReplaySince(t *testing.T) {
	tests := []struct {
		since int64
		want  int
	}{{-5, 10}, {0, 10}, {4, 6}, {9, 1}, {10, 0}, {50, 0}}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.since), func(t *testing.T) {
			b := newBus(t, Options{ReplayBatch: 3}) // exercise paging
			for i := 0; i < 10; i++ {
				publish(t, b, output("t", fmt.Sprint(i)))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := b.Subscribe(ctx, tt.since, kernel.EventFilter{})
			if err != nil {
				t.Fatal(err)
			}
			got := collect(t, ch, tt.want)
			first := min(max(tt.since, 0), 10) + 1
			for i, ev := range got {
				if ev.Seq != first+int64(i) {
					t.Fatalf("got seqs %v", seqs(got))
				}
				if ev.At == nil || ev.GetRunOutput() == nil {
					t.Fatalf("event not fully decoded: %v", ev)
				}
			}
			expectQuiet(t, ch)
			// Then live.
			publish(t, b, output("t", "live"))
			live := collect(t, ch, 1)
			if live[0].GetRunOutput().GetText() != "live" || live[0].Seq != 11 {
				t.Fatalf("live = %v", live[0])
			}
		})
	}
}

func TestFilters(t *testing.T) {
	tests := []struct {
		name   string
		filter kernel.EventFilter
		want   []string // texts / kinds expected, in order
	}{
		{"all", kernel.EventFilter{}, []string{"a1", "b1", "q:a", "a2", "q:c"}},
		{"task a", kernel.EventFilter{TaskIDs: []string{"a"}}, []string{"a1", "q:a", "a2"}},
		{"tasks a,b", kernel.EventFilter{TaskIDs: []string{"a", "b"}}, []string{"a1", "b1", "q:a", "a2"}},
		{"inbox", kernel.EventFilter{InboxOnly: true}, []string{"q:a", "q:c"}},
		{"inbox task c", kernel.EventFilter{InboxOnly: true, TaskIDs: []string{"c"}}, []string{"q:c"}},
		{"unknown task", kernel.EventFilter{TaskIDs: []string{"zzz"}}, nil},
	}
	label := func(ev *nexusv1.Event) string {
		if ev.GetQuestion() != nil {
			return "q:" + ev.TaskId
		}
		return ev.GetRunOutput().GetText()
	}
	stream := []*nexusv1.Event{output("a", "a1"), output("b", "b1"), question("a"), output("a", "a2"), question("c")}
	for _, tt := range tests {
		for _, mode := range []string{"replay", "live", "split"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				b := newBus(t, Options{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				split := map[string]int{"replay": len(stream), "live": 0, "split": 2}[mode]
				publish(t, b, stream[:split]...)
				ch, err := b.Subscribe(ctx, 0, tt.filter)
				if err != nil {
					t.Fatal(err)
				}
				publish(t, b, stream[split:]...)
				got := collect(t, ch, len(tt.want))
				for i, ev := range got {
					if label(ev) != tt.want[i] {
						t.Fatalf("event %d = %q, want %v", i, label(ev), tt.want)
					}
				}
				expectQuiet(t, ch)
			})
		}
	}
}

// TestReplayLiveBoundary subscribes at random points while publishers are
// running and checks every subscriber sees a contiguous, duplicate-free
// sequence from since+1 to the end.
func TestReplayLiveBoundary(t *testing.T) {
	const (
		publishers = 4
		perPub     = 200
		total      = publishers * perPub
		subsCount  = 20
	)
	b := newBus(t, Options{ReplayBatch: 7})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var pubWG sync.WaitGroup
	for p := 0; p < publishers; p++ {
		pubWG.Add(1)
		go func() {
			defer pubWG.Done()
			for i := 0; i < perPub; i++ {
				if _, err := b.Publish(ctx, output("t", "x")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}

	type result struct {
		since int64
		got   []*nexusv1.Event
		err   error
	}
	results := make(chan result, subsCount)
	var subWG sync.WaitGroup
	for s := 0; s < subsCount; s++ {
		// Stagger subscriptions over the publishing window.
		time.Sleep(time.Millisecond)
		since, _ := b.LastSeq(ctx)
		since /= int64(s%3 + 1) // mix of "from now" and "from the past"
		ch, err := b.Subscribe(ctx, since, kernel.EventFilter{})
		if err != nil {
			t.Fatal(err)
		}
		subWG.Add(1)
		go func() {
			defer subWG.Done()
			r := result{since: since}
			want := total - int(since)
			timeout := time.After(10 * time.Second)
			for len(r.got) < want {
				select {
				case ev, ok := <-ch:
					if !ok {
						r.err = fmt.Errorf("closed after %d", len(r.got))
						results <- r
						return
					}
					r.got = append(r.got, ev)
				case <-timeout:
					r.err = fmt.Errorf("timeout after %d/%d", len(r.got), want)
					results <- r
					return
				}
			}
			results <- r
		}()
	}
	pubWG.Wait()
	subWG.Wait()
	close(results)
	for r := range results {
		if r.err != nil {
			t.Fatalf("since=%d: %v", r.since, r.err)
		}
		for i, ev := range r.got {
			if want := r.since + 1 + int64(i); ev.Seq != want {
				t.Fatalf("since=%d: event %d has seq %d, want %d", r.since, i, ev.Seq, want)
			}
		}
	}
}

func TestSlowSubscriberDoesNotBlockPublishers(t *testing.T) {
	b := newBus(t, Options{LiveBuffer: 2, OutBuffer: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow, err := b.Subscribe(ctx, 0, kernel.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			publish(t, b, output("t", "x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishers blocked by a slow subscriber")
	}

	// The slow subscriber drains what it had buffered, then is closed.
	var last int64
	timeout := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case ev, ok := <-slow:
			if !ok {
				closed = true
				break
			}
			if ev.Seq != last+1 {
				t.Fatalf("gap before drop: %d after %d", ev.Seq, last)
			}
			last = ev.Seq
		case <-timeout:
			t.Fatal("slow subscriber never closed")
		}
	}
	if last >= 50 {
		t.Fatalf("slow subscriber got everything (%d); expected a drop", last)
	}

	// Resubscribing from the last processed seq recovers the rest exactly.
	again, err := b.Subscribe(ctx, last, kernel.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	rest := collect(t, again, int(50-last))
	if rest[0].Seq != last+1 || rest[len(rest)-1].Seq != 50 {
		t.Fatalf("resubscribe got %v", seqs(rest))
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	b := newBus(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := b.Subscribe(ctx, 0, kernel.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d subscribers still registered", n)
	}
	publish(t, b, output("t", "x")) // must not panic on closed channel
}

func TestClose(t *testing.T) {
	b := newBus(t, Options{})
	ch, err := b.Subscribe(context.Background(), 0, kernel.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after Close")
	}
	if _, err := b.Publish(context.Background(), output("t", "x")); err != ErrClosed {
		t.Fatalf("Publish after Close: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), 0, kernel.EventFilter{}); err != ErrClosed {
		t.Fatalf("Subscribe after Close: %v", err)
	}
}

func TestPersistence(t *testing.T) {
	st := newStore(t)
	b, err := New(st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, output("t", "1"), output("t", "2"))
	b.Close()

	b2, err := New(st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if last, _ := b2.LastSeq(context.Background()); last != 2 {
		t.Fatalf("LastSeq after reopen = %d", last)
	}
	got, err := b2.Publish(context.Background(), output("t", "3"))
	if err != nil || got.Seq != 3 {
		t.Fatalf("seq after reopen = %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b2.Subscribe(ctx, 0, kernel.EventFilter{})
	if s := seqs(collect(t, ch, 3)); s[0] != 1 || s[2] != 3 {
		t.Fatalf("seqs = %v", s)
	}
}
