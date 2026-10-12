package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/notify/apns"
	"github.com/danielpina1/nexus/backend/internal/security"
)

const (
	kindStart  = nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_PUSH_TO_START
	kindUpdate = nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE
)

type liveFixture struct {
	clk  *fakeClock
	dir  *droppingDir
	p    *recPusher
	live *LiveManager
}

func newLive(t *testing.T, mutate func(*Config)) *liveFixture {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1760000000, 0)}
	dir := &droppingDir{fakeDir{tokens: []security.LiveActivityToken{
		{DeviceID: "dev_1", Kind: kindStart, ActivityType: ActivityRun, Token: []byte{0xa1}, Environment: "sandbox"},
		{DeviceID: "dev_2", Kind: kindStart, ActivityType: ActivityRun, Token: []byte{0xa2}},
	}}}
	p := &recPusher{}
	cfg := Config{APNS: APNSConfig{BundleID: "com.example.nexus"}}
	if mutate != nil {
		mutate(&cfg)
	}
	_ = cfg.defaults()
	svc := newService(cfg, discard, p, func() Directory { return dir }, nil, "")
	svc.now = clk.Now
	lm := newLiveManager(cfg, discard, func() Directory { return dir }, svc.pushLiveActivity)
	lm.now = clk.Now
	lm.agentInfo = func(context.Context, string) AgentInfo { return AgentInfo{Name: "Code fixer", Symbol: "hammer"} }
	return &liveFixture{clk: clk, dir: dir, p: p, live: lm}
}

func (f *liveFixture) register(dev string, tok byte, task string) {
	f.dir.mu.Lock()
	f.dir.tokens = append(f.dir.tokens, security.LiveActivityToken{DeviceID: dev, Kind: kindUpdate,
		ActivityType: ActivityRun, TaskID: task, Token: []byte{tok}, UpdatedAt: f.clk.Now()})
	f.dir.mu.Unlock()
}

func follow(task string, devices ...any) *nexusv1.Event {
	return evNotice(task, &nexusv1.Notice{Id: "ntc_f", Data: mustStruct(map[string]any{"type": "follow_task", "device_ids": devices})})
}

func running(task string) *nexusv1.Event {
	return evTask(&nexusv1.Task{Id: task, AgentId: "fixer", NodeId: "mac-mini", State: nexusv1.TaskState_TASK_STATE_RUNNING})
}

func toolCall(task string) *nexusv1.Event {
	return &nexusv1.Event{TaskId: task, Body: &nexusv1.Event_ToolCall{ToolCall: &nexusv1.ToolCall{Tool: "shell"}}}
}

func TestLiveActivityLifecycle(t *testing.T) {
	f := newLive(t, func(c *Config) { c.LiveActivity.Pulse = new(bool) })
	ctx := context.Background()
	f.live.Handle(ctx, running("tsk_1"))
	if got := f.p.take(); len(got) != 0 {
		t.Fatalf("untracked task pushed: %d", len(got))
	}

	// Follow from the web for dev_1 only → push-to-start.
	f.live.Handle(ctx, follow("tsk_1", "dev_1"))
	reqs := f.p.take()
	if len(reqs) != 1 {
		t.Fatalf("start pushes = %d", len(reqs))
	}
	r := reqs[0]
	if r.DeviceToken != "a1" || r.Topic != "com.example.nexus.push-type.liveactivity" || r.PushType != apns.PushTypeLiveActivity ||
		r.Priority != 10 || r.Environment != "sandbox" {
		t.Errorf("start request %+v", r)
	}
	aps := decodeAPS(t, r.Payload)
	if aps["event"] != "start" || aps["attributes-type"] != "RunActivityAttributes" || aps["stale-date"] == nil || aps["alert"] == nil {
		t.Errorf("start aps %v", aps)
	}
	attrs := aps["attributes"].(map[string]any)
	wantAttrs := map[string]any{"taskId": "tsk_1", "agentId": "fixer", "agentName": "Code fixer", "agentSymbol": "hammer", "nodeName": "mac-mini"}
	for k, v := range wantAttrs {
		if attrs[k] != v {
			t.Errorf("attributes[%s] = %v want %v", k, attrs[k], v)
		}
	}
	cs := aps["content-state"].(map[string]any)
	if cs["phase"] != "running" || cs["title"] != "Running" || cs["costCents"] != float64(0) {
		t.Errorf("content-state %v", cs)
	}

	// The phone started the activity and registered its update token.
	f.register("dev_1", 0xb1, "tsk_1")

	// Progress (tool calls) is coalesced: nothing until min_interval elapses.
	f.clk.Advance(2 * time.Second)
	for range 5 {
		f.live.Handle(ctx, toolCall("tsk_1"))
	}
	f.live.Flush(ctx)
	if got := f.p.take(); len(got) != 0 {
		t.Fatalf("coalesced update sent early: %d", len(got))
	}
	f.clk.Advance(8 * time.Second)
	f.live.Flush(ctx)
	reqs = f.p.take()
	if len(reqs) != 1 || reqs[0].DeviceToken != "b1" || reqs[0].Priority != 5 {
		t.Fatalf("coalesced update: %+v", reqs)
	}
	aps = decodeAPS(t, reqs[0].Payload)
	cs = aps["content-state"].(map[string]any)
	if aps["event"] != "update" || cs["step"] != float64(5) || aps["alert"] != nil || aps["stale-date"] == nil {
		t.Errorf("update aps %v", aps)
	}
	// No change → no push.
	f.clk.Advance(20 * time.Second)
	f.live.Flush(ctx)
	if got := f.p.take(); len(got) != 0 {
		t.Fatalf("unchanged state pushed")
	}

	// Approval → immediate priority-10 update with alert.
	f.live.Handle(ctx, evApproval(&nexusv1.Approval{Id: "apr_1", TaskId: "tsk_1", Title: "Secret checkout",
		State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING, Level: nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC}))
	reqs = f.p.take()
	if len(reqs) != 1 || reqs[0].Priority != 10 {
		t.Fatalf("approval update: %+v", reqs)
	}
	aps = decodeAPS(t, reqs[0].Payload)
	cs = aps["content-state"].(map[string]any)
	if cs["phase"] != "waitingApproval" || cs["approvalId"] != "apr_1" || cs["approvalLevel"] != "biometric" ||
		cs["title"] != "Waiting for approval" || aps["alert"] == nil || aps["relevance-score"] != float64(100) {
		t.Errorf("approval aps %v", aps)
	}
	if strings.Contains(string(reqs[0].Payload), "Secret checkout") {
		t.Error("approval title leaked to Lock Screen with show_details=false")
	}

	// Task cost update + done → end with dismissal-date (+1h).
	f.clk.Advance(time.Second)
	f.live.Handle(ctx, evTask(&nexusv1.Task{Id: "tsk_1", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED,
		Cost: &nexusv1.Cost{Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 81}}}))
	reqs = f.p.take()
	if len(reqs) != 1 {
		t.Fatalf("end pushes = %d", len(reqs))
	}
	aps = decodeAPS(t, reqs[0].Payload)
	cs = aps["content-state"].(map[string]any)
	now := f.clk.Now()
	if aps["event"] != "end" || cs["phase"] != "done" || cs["costCents"] != float64(81) || cs["progress"] != float64(1) ||
		aps["dismissal-date"] != float64(now.Add(time.Hour).Unix()) || cs["approvalId"] != nil {
		t.Errorf("end aps %v", aps)
	}
	// Ended: further events are ignored.
	f.live.Handle(ctx, toolCall("tsk_1"))
	f.clk.Advance(time.Minute)
	f.live.Flush(ctx)
	if got := f.p.take(); len(got) != 0 {
		t.Errorf("pushes after end: %d", len(got))
	}
}

func TestLiveActivityAdoptsInAppActivityAnd410(t *testing.T) {
	f := newLive(t, func(c *Config) { c.LiveActivity.Pulse = new(bool) })
	ctx := context.Background()
	f.register("dev_2", 0xc2, "tsk_9") // started in-app via "Follow live"
	f.p.status = func(r apns.Request) int {
		if r.DeviceToken == "c2" {
			return 410
		}
		return 200
	}
	f.live.Handle(ctx, evTask(&nexusv1.Task{Id: "tsk_9", State: nexusv1.TaskState_TASK_STATE_WAITING_INPUT}))
	reqs := f.p.take()
	if len(reqs) != 1 || reqs[0].DeviceToken != "c2" || reqs[0].Priority != 10 {
		t.Fatalf("adopted update: %+v", reqs)
	}
	if cs := decodeAPS(t, reqs[0].Payload)["content-state"].(map[string]any); cs["phase"] != "waitingInput" {
		t.Errorf("content-state %v", cs)
	}
	if len(f.dir.dropped) != 1 || f.dir.dropped[0] != "la:dev_2" {
		t.Errorf("410 not dropped: %v", f.dir.dropped)
	}
}

func TestLiveActivityAutoFollowAndMaxDuration(t *testing.T) {
	f := newLive(t, func(c *Config) {
		c.LiveActivity.Pulse = new(bool)
		c.LiveActivity.AutoFollowAgents = []string{"fixer"}
		c.LiveActivity.MaxDuration = time.Hour
	})
	ctx := context.Background()
	f.live.Handle(ctx, running("tsk_2"))
	if reqs := f.p.take(); len(reqs) != 2 { // both devices (no device filter)
		t.Fatalf("auto-follow start pushes = %d", len(reqs))
	}
	f.register("dev_1", 0xd1, "tsk_2")
	f.clk.Advance(61 * time.Minute)
	f.live.Flush(ctx)
	reqs := f.p.take()
	if len(reqs) != 1 || decodeAPS(t, reqs[0].Payload)["event"] != "end" {
		t.Fatalf("max duration end: %+v", reqs)
	}
}

func TestPulse(t *testing.T) {
	f := newLive(t, nil)
	f.dir.tokens = []security.LiveActivityToken{
		{DeviceID: "dev_1", Kind: kindStart, ActivityType: ActivityPulse, Token: []byte{0xe1}},
	}
	ctx := context.Background()
	f.live.Handle(ctx, running("tsk_1"))
	reqs := f.p.take()
	if len(reqs) != 1 {
		t.Fatalf("pulse start = %d", len(reqs))
	}
	aps := decodeAPS(t, reqs[0].Payload)
	cs := aps["content-state"].(map[string]any)
	if aps["event"] != "start" || aps["attributes-type"] != "PulseActivityAttributes" ||
		aps["attributes"].(map[string]any)["hubName"] != "Nexus" || cs["running"] != float64(1) || cs["waiting"] != float64(0) ||
		cs["headline"] != "Code fixer" || cs["headlineSymbol"] != "hammer" {
		t.Errorf("pulse start %v", aps)
	}
	f.clk.Advance(time.Second)
	f.dir.tokens = append(f.dir.tokens, security.LiveActivityToken{DeviceID: "dev_1", Kind: kindUpdate,
		ActivityType: ActivityPulse, Token: []byte{0xe2}, UpdatedAt: f.clk.Now()})
	f.live.Handle(ctx, &nexusv1.Event{TaskId: "tsk_1", Body: &nexusv1.Event_Cost{Cost: &nexusv1.CostRecorded{
		Cost: &nexusv1.Cost{Amount: &nexusv1.Money{MinorUnits: 42}}}}})
	// A task waiting for the user → immediate priority-10 update.
	f.live.Handle(ctx, evTask(&nexusv1.Task{Id: "tsk_2", State: nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL}))
	reqs = f.p.take()
	if len(reqs) != 1 || reqs[0].DeviceToken != "e2" || reqs[0].Priority != 10 {
		t.Fatalf("pulse waiting update: %+v", reqs)
	}
	cs = decodeAPS(t, reqs[0].Payload)["content-state"].(map[string]any)
	if cs["waiting"] != float64(1) || cs["costCentsToday"] != float64(42) {
		t.Errorf("pulse content %v", cs)
	}
	// All done → end after 2 min idle.
	f.live.Handle(ctx, evTask(&nexusv1.Task{Id: "tsk_1", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED}))
	f.live.Handle(ctx, evTask(&nexusv1.Task{Id: "tsk_2", State: nexusv1.TaskState_TASK_STATE_SUCCEEDED}))
	f.clk.Advance(11 * time.Second)
	f.live.Flush(ctx)
	if reqs = f.p.take(); len(reqs) != 1 || decodeAPS(t, reqs[0].Payload)["event"] != "update" {
		t.Fatalf("pulse idle update: %+v", reqs)
	}
	f.clk.Advance(2 * time.Minute)
	f.live.Flush(ctx)
	if reqs = f.p.take(); len(reqs) != 1 || decodeAPS(t, reqs[0].Payload)["event"] != "end" {
		t.Fatalf("pulse end: %+v", reqs)
	}
}

func TestLivePayloadSizeGuard(t *testing.T) {
	f := newLive(t, nil)
	big := make([]byte, 6000)
	for i := range big {
		big[i] = 'x'
	}
	b, err := f.live.payload(laAPS{Event: "update", ContentState: RunContent{Phase: PhaseRunning, Title: string(big), Detail: string(big)},
		Alert: &laAlert{Title: string(big), Body: string(big)}})
	if err != nil || len(b) > MaxPayload {
		t.Fatalf("size %d err %v", len(b), err)
	}
}
