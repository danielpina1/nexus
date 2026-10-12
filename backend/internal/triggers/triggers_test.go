package triggers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// ---------------------------------------------------------------- fake clock

type waiter struct {
	at time.Time
	ch chan time.Time
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(t) {
			w.ch <- t
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type collector struct {
	mu sync.Mutex
	fs []kernel.Firing
}

func (c *collector) emit(f kernel.Firing) {
	c.mu.Lock()
	c.fs = append(c.fs, f)
	c.mu.Unlock()
}

func (c *collector) all() []kernel.Firing {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]kernel.Firing(nil), c.fs...)
}

// ---------------------------------------------------------------- cron

func TestNextFire(t *testing.T) {
	cases := []struct {
		expr, tz string
		after    string
		want     string
	}{
		// Sunday 18:00 Lisbon: WEST (UTC+1) before 25 Oct 2026, WET (UTC+0) after.
		{"0 18 * * 0", "Europe/Lisbon", "2026-10-10T12:00:00Z", "2026-10-11T17:00:00Z"},
		{"0 18 * * 0", "Europe/Lisbon", "2026-10-18T17:00:00Z", "2026-10-25T18:00:00Z"},
		{"30 7 * * 1-5", "Europe/Lisbon", "2026-10-09T07:00:00Z", "2026-10-12T06:30:00Z"}, // Fri after 07:30 local → Mon
		{"0 3 * * *", "", "2026-10-10T12:00:00Z", "2026-10-11T03:00:00Z"},
		{"@hourly", "UTC", "2026-10-10T12:10:00Z", "2026-10-10T13:00:00Z"},
	}
	for _, c := range cases {
		after, _ := time.Parse(time.RFC3339, c.after)
		if c.tz == "" {
			after = after.In(time.UTC)
		}
		tz := c.tz
		if tz == "" {
			tz = "UTC"
		}
		got, err := NextFire(c.expr, tz, after)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if g := got.UTC().Format(time.RFC3339); g != c.want {
			t.Errorf("NextFire(%q, %q, %s) = %s, want %s", c.expr, c.tz, c.after, g, c.want)
		}
	}
	for _, bad := range []struct{ expr, tz string }{{"61 * * * *", ""}, {"", ""}, {"* * * * *", "Mars/Olympus"}} {
		if _, err := ParseCron(bad.expr, bad.tz); err == nil {
			t.Errorf("ParseCron(%q, %q): want error", bad.expr, bad.tz)
		}
	}
}

func TestCronSourceFakeClock(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, "2026-10-10T12:00:00Z")
	clk := &fakeClock{now: start}
	src := NewCronSource(clk, nil)
	if err := src.SetCron("workflow", []CronSpec{
		{ID: "weekly", Expr: "0 18 * * 0", TZ: "Europe/Lisbon"},
		{ID: "bad", Expr: "nope"},
	}); err == nil {
		t.Error("invalid spec: want error")
	}
	if k := src.Keys(); len(k) != 1 || k["weekly"].UTC().Format(time.RFC3339) != "2026-10-11T17:00:00Z" {
		t.Fatalf("keys = %v", k)
	}
	var c collector
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = src.Run(ctx, c.emit); close(done) }()
	defer func() { cancel(); <-done }()

	eventually(t, "timer", func() bool { return clk.pending() >= 1 })
	clk.Set(start.Add(28*time.Hour + 59*time.Minute)) // 16:59Z Sunday: not yet
	time.Sleep(10 * time.Millisecond)
	if len(c.all()) != 0 {
		t.Fatal("fired early")
	}
	clk.Set(start.Add(29 * time.Hour)) // 17:00Z = 18:00 Lisbon
	eventually(t, "firing", func() bool { return len(c.all()) == 1 })
	f := c.all()[0]
	if f.Source != SourceCron || f.Key != "weekly" || f.Payload["scheduled_at"] != "2026-10-11T17:00:00Z" ||
		f.Payload["tz"] != "Europe/Lisbon" || f.Meta["owner"] != "workflow" {
		t.Errorf("firing = %+v", f)
	}
	eventually(t, "next timer", func() bool { return clk.pending() >= 1 })
	if k := src.Keys(); k["weekly"].UTC().Format(time.RFC3339) != "2026-10-18T17:00:00Z" {
		t.Errorf("next = %v", k["weekly"])
	}

	// Missed slots fire once (not once per missed week).
	clk.Set(start.Add(30 * 24 * time.Hour))
	eventually(t, "catch-up firing", func() bool { return len(c.all()) == 2 })
	time.Sleep(10 * time.Millisecond)
	if n := len(c.all()); n != 2 {
		t.Errorf("firings after long sleep = %d, want 2", n)
	}

	// Removing the owner's entries stops them.
	_ = src.SetCron("workflow", nil)
	if len(src.Keys()) != 0 {
		t.Error("entries not removed")
	}
}

func TestIntervalSource(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: start}
	src := NewIntervalSource(clk, nil)
	_ = src.SetIntervals("a", []IntervalSpec{{ID: "fast", Every: 10 * time.Minute}, {ID: "slow", Every: time.Hour}})
	if err := src.SetIntervals("b", []IntervalSpec{{ID: "zero"}}); err == nil {
		t.Error("zero period: want error")
	}
	var c collector
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = src.Run(ctx, c.emit); close(done) }()
	defer func() { cancel(); <-done }()

	for i := 1; i <= 6; i++ {
		eventually(t, "timer", func() bool { return clk.pending() >= 1 })
		clk.Set(start.Add(time.Duration(i) * 10 * time.Minute))
		eventually(t, "firing", func() bool { return len(c.all()) >= i })
	}
	eventually(t, "slow firing", func() bool { return len(c.all()) == 7 })
	n := map[string]int{}
	for _, f := range c.all() {
		n[f.Key]++
	}
	if n["fast"] != 6 || n["slow"] != 1 {
		t.Errorf("firings = %v", n)
	}
	// Re-setting an unchanged entry keeps its phase.
	before := src.Keys()["slow"]
	_ = src.SetIntervals("a", []IntervalSpec{{ID: "fast", Every: 10 * time.Minute}, {ID: "slow", Every: time.Hour}})
	if after := src.Keys()["slow"]; !after.Equal(before) {
		t.Errorf("phase reset: %v → %v", before, after)
	}
}

// ---------------------------------------------------------------- webhook

func TestSignature(t *testing.T) {
	body := []byte(`{"ref":"main"}`)
	sig := Sign("s3cret", body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != 7+64 {
		t.Fatalf("sig = %q", sig)
	}
	// Known vector: HMAC-SHA256("key", "The quick brown fox jumps over the lazy dog").
	if got := Sign("key", []byte("The quick brown fox jumps over the lazy dog")); got != "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8" {
		t.Errorf("vector = %s", got)
	}
	if !VerifySignature("s3cret", body, sig) {
		t.Error("valid signature rejected")
	}
	for _, bad := range []string{"", "sha256=", "sha256=zz", sig[:len(sig)-2] + "00", strings.TrimPrefix(sig, "sha256=")} {
		if VerifySignature("s3cret", body, bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if VerifySignature("other", body, sig) || VerifySignature("", body, sig) {
		t.Error("wrong secret accepted")
	}
	if VerifySignature("s3cret", []byte(`{"ref":"evil"}`), sig) {
		t.Error("tampered body accepted")
	}
}

func TestWebhookHandler(t *testing.T) {
	h := kerneltest.New(t)
	h.ConfigYAML = "webhooks:\n  - { id: cfg, secret: cfgsecret }\n"
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if len(h.Triggers().All()) != 4 {
		t.Fatalf("sources = %d", len(h.Triggers().All()))
	}
	t.Setenv("TEST_DEPLOY_SECRET", "envsecret")
	_ = m.Webhook.SetHooks("workflow", []HookSpec{{ID: "deploy", SecretEnv: "TEST_DEPLOY_SECRET"}, {ID: "nosecret"}})
	srv := httptest.NewServer(h.Mux)
	defer srv.Close()

	post := func(id string, body []byte, hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hooks/"+id, bytes.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	body := []byte(`{"ref":"main"}`)

	// Not running yet → 503 (after auth).
	if code := post("deploy", body, map[string]string{SignatureHeader: Sign("envsecret", body)}); code != http.StatusServiceUnavailable {
		t.Errorf("not running = %d", code)
	}
	var c collector
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Webhook.Run(ctx, c.emit) }()
	eventually(t, "running", func() bool {
		m.Webhook.mu.RLock()
		defer m.Webhook.mu.RUnlock()
		return m.Webhook.emit != nil
	})

	cases := []struct {
		name string
		id   string
		body []byte
		hdr  map[string]string
		want int
	}{
		{"hmac", "deploy", body, map[string]string{SignatureHeader: Sign("envsecret", body), "X-GitHub-Event": "push"}, http.StatusAccepted},
		{"github header", "deploy", body, map[string]string{"X-Hub-Signature-256": Sign("envsecret", body)}, http.StatusAccepted},
		{"bearer", "cfg", []byte("plain text"), map[string]string{"Authorization": "Bearer cfgsecret"}, http.StatusAccepted},
		{"bad sig", "deploy", body, map[string]string{SignatureHeader: Sign("wrong", body)}, http.StatusUnauthorized},
		{"bad bearer", "deploy", body, map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"no auth", "deploy", body, nil, http.StatusUnauthorized},
		{"unknown", "nope", body, map[string]string{"Authorization": "Bearer cfgsecret"}, http.StatusNotFound},
		{"no secret", "nosecret", body, nil, http.StatusForbidden},
		{"other hook's secret", "deploy", body, map[string]string{"Authorization": "Bearer cfgsecret"}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if code := post(tc.id, tc.body, tc.hdr); code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, code, tc.want)
		}
	}
	fs := c.all()
	if len(fs) != 3 {
		t.Fatalf("firings = %d", len(fs))
	}
	if fs[0].Source != SourceWebhook || fs[0].Key != "deploy" || fs[0].Payload["ref"] != "main" || fs[0].Meta["x_github_event"] != "push" {
		t.Errorf("firing = %+v", fs[0])
	}
	if fs[2].Key != "cfg" || fs[2].Payload["body"] != "plain text" {
		t.Errorf("text firing = %+v", fs[2])
	}

	// GET is not routed to the hook handler.
	res, err := http.Get(srv.URL + "/hooks/deploy")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusAccepted {
		t.Error("GET accepted")
	}

	// Oversized bodies are rejected.
	big := bytes.Repeat([]byte("a"), 2<<20)
	if code := post("deploy", big, map[string]string{SignatureHeader: Sign("envsecret", big)}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("big body = %d", code)
	}
}

// ---------------------------------------------------------------- events

func nodeEv(id string, online bool, disk float64) *nexusv1.Event {
	return &nexusv1.Event{Body: &nexusv1.Event_NodeStatus{NodeStatus: &nexusv1.NodeStatus{NodeId: id, Online: online, DiskPercent: disk}}}
}

func taskEv(id string, s nexusv1.TaskState) *nexusv1.Event {
	return &nexusv1.Event{TaskId: id, Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{Id: id, State: s}}}}
}

func TestTopics(t *testing.T) {
	cases := []struct {
		ev   *nexusv1.Event
		want string
	}{
		{nodeEv("n", false, 0), "node.offline"},
		{nodeEv("n", true, 95), "node.online,node.disk_high"},
		{taskEv("t", nexusv1.TaskState_TASK_STATE_FAILED), "task.failed"},
		{&nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING}}}, "approval.pending"},
		{&nexusv1.Event{Body: &nexusv1.Event_Notice{Notice: &nexusv1.Notice{Kind: nexusv1.NoticeKind_NOTICE_KIND_ALERT}}}, "notice.alert"},
		{&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: "x"}}}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(Topics(c.ev), ","); got != c.want {
			t.Errorf("Topics(%s) = %q, want %q", EventKind(c.ev), got, c.want)
		}
	}
	for _, m := range []struct {
		p, t string
		ok   bool
	}{{"task.*", "task.failed", true}, {"*", "x", true}, {"node.offline", "node.offline", true}, {"node.*", "task.failed", false}, {"node.off", "node.offline", false}} {
		if MatchTopic(m.p, m.t) != m.ok {
			t.Errorf("MatchTopic(%q, %q) != %v", m.p, m.t, m.ok)
		}
	}
	em := EventMap(nodeEv("mac", false, 0))
	if em["node_status"].(map[string]any)["node_id"] != "mac" {
		t.Errorf("EventMap = %v", em)
	}
}

func TestEventSourceDedupe(t *testing.T) {
	s := NewEventSource(nil, nil)
	seq := []struct {
		ev   *nexusv1.Event
		want string
	}{
		{nodeEv("a", true, 10), "node.online"},
		{nodeEv("a", true, 20), ""}, // telemetry, no change
		{nodeEv("a", false, 20), "node.offline"},
		{nodeEv("a", false, 20), ""},
		{nodeEv("b", false, 0), "node.offline"}, // other node
		{nodeEv("a", true, 92), "node.online,node.disk_high"},
		{nodeEv("a", true, 95), ""},
		{nodeEv("a", true, 50), ""},
		{nodeEv("a", true, 91), "node.disk_high"}, // crossed again
		{taskEv("t1", nexusv1.TaskState_TASK_STATE_RUNNING), "task.running"},
		{taskEv("t1", nexusv1.TaskState_TASK_STATE_RUNNING), ""},
		{taskEv("t1", nexusv1.TaskState_TASK_STATE_FAILED), "task.failed"},
	}
	for i, c := range seq {
		var got []string
		for _, f := range s.Firings(c.ev) {
			got = append(got, f.Key)
			if f.Source != SourceEvent || f.Payload["event"] == nil {
				t.Errorf("%d: firing = %+v", i, f)
			}
		}
		if g := strings.Join(got, ","); g != c.want {
			t.Errorf("%d: firings = %q, want %q", i, g, c.want)
		}
	}
}

type fakeBus struct {
	ch chan *nexusv1.Event
}

func (b *fakeBus) Publish(_ context.Context, ev *nexusv1.Event) (*nexusv1.Event, error) {
	b.ch <- ev
	return ev, nil
}
func (b *fakeBus) Subscribe(ctx context.Context, _ int64, _ kernel.EventFilter) (<-chan *nexusv1.Event, error) {
	out := make(chan *nexusv1.Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-b.ch:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
func (b *fakeBus) LastSeq(context.Context) (int64, error) { return 0, nil }

func TestEventSourceRun(t *testing.T) {
	bus := &fakeBus{ch: make(chan *nexusv1.Event, 10)}
	s := NewEventSource(func() (kernel.EventBus, error) { return bus, nil }, nil)
	var c collector
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx, c.emit); close(done) }()
	_, _ = bus.Publish(ctx, nodeEv("x", false, 0))
	_, _ = bus.Publish(ctx, &nexusv1.Event{Seq: 3, Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{}}})
	eventually(t, "firing", func() bool { return len(c.all()) == 1 })
	cancel()
	<-done
	if f := c.all()[0]; f.Key != "node.offline" || f.Payload["kind"] != "node_status" {
		t.Errorf("firing = %+v", f)
	}
}
