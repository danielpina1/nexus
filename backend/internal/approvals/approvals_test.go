package approvals

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

type observer struct {
	mu   sync.Mutex
	seen []*nexusv1.Approval
}

func (o *observer) OnApprovalResolved(_ context.Context, a *nexusv1.Approval) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, a)
	return nil
}

func (o *observer) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

type verifier struct{ err error }

func (v verifier) VerifyApproval(context.Context, *nexusv1.SignedApproval, *nexusv1.Approval) error {
	return v.err
}

type fixture struct {
	env  *taskstest.Env
	m    *Module
	b    *Broker
	disp *taskstest.Dispatcher
	obs  *observer
}

func newFixture(t *testing.T, config string) *fixture {
	t.Helper()
	f := &fixture{env: taskstest.New(t), disp: &taskstest.Dispatcher{}, obs: &observer{}}
	f.env.Host.ConfigYAML = config
	f.env.Host.Provide(kernel.SvcDispatcher, f.disp)
	f.env.Host.Provide(kernel.SvcTasks, f.obs)
	f.m = &Module{}
	if err := f.m.Init(context.Background(), f.env.Host); err != nil {
		t.Fatal(err)
	}
	f.b = f.m.Broker
	return f
}

func payload(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) request(t *testing.T, level nexusv1.TrustLevel) *nexusv1.Approval {
	t.Helper()
	a, err := f.b.Request(context.Background(), &nexusv1.Approval{
		TaskId: "tsk_1", RunId: "run_1", NodeId: "node-1", AgentId: "coder",
		ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, Level: level, Title: "git push",
		Payload: payload(t, map[string]any{"branch": "main", "remote": "origin"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func decision(a *nexusv1.Approval, approve bool) *nexusv1.DecideRequest {
	return &nexusv1.DecideRequest{Decision: &nexusv1.SignedApproval{
		ApprovalId: a.Id, Approve: approve, Challenge: a.Challenge, ActionSha256: a.PayloadHash, DeviceId: "phone",
	}}
}

func TestPayloadHashCanonical(t *testing.T) {
	a := payload(t, map[string]any{"b": 1, "a": []any{"x", map[string]any{"z": true, "y": nil}}, "html": "<&>"})
	b := payload(t, map[string]any{"html": "<&>", "a": []any{"x", map[string]any{"y": nil, "z": true}}, "b": 1.0})
	got, _ := CanonicalJSON(a)
	if want := `{"a":["x",{"y":null,"z":true}],"b":1,"html":"<&>"}`; string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	ha, _ := PayloadHash(a)
	hb, _ := PayloadHash(b)
	if !bytes.Equal(ha, hb) || len(ha) != 32 {
		t.Fatal("hash not canonical")
	}
	empty, _ := CanonicalJSON(nil)
	if string(empty) != "{}" {
		t.Fatalf("nil payload = %s", empty)
	}
}

func TestRequest(t *testing.T) {
	f := newFixture(t, "")
	a := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED)
	if a.Id == "" || len(a.Challenge) != 32 || len(a.PayloadHash) != 32 || a.State != statePending {
		t.Fatalf("approval = %+v", a)
	}
	// No policy engine → a tap is required.
	if a.Level != nexusv1.TrustLevel_TRUST_LEVEL_APPROVE {
		t.Fatalf("level = %s", a.Level)
	}
	if ttl := a.ExpiresAt.AsTime().Sub(a.CreatedAt.AsTime()); ttl != 2*time.Hour {
		t.Fatalf("ttl = %s", ttl)
	}
	evs := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.GetApproval() != nil })
	if len(evs) != 1 || evs[0].TaskId != "tsk_1" || evs[0].GetApproval().Id != a.Id {
		t.Fatalf("events = %+v", evs)
	}
	got, err := f.b.List(context.Background(), []nexusv1.ApprovalState{statePending})
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %v, %v", got, err)
	}

	_, err = f.b.Request(context.Background(), &nexusv1.Approval{Payload: payload(t, map[string]any{"a": 1}), PayloadHash: []byte("bogus")})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("mismatched hash err = %v", err)
	}
}

type fixedPolicy struct{}

func (fixedPolicy) Level(_ context.Context, a *nexusv1.Agent, _ nexusv1.ActionClass) (nexusv1.TrustLevel, error) {
	if a.GetId() == "coder" {
		return nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC, nil
	}
	return nexusv1.TrustLevel_TRUST_LEVEL_AUTO, nil
}

func TestRequestUsesPolicy(t *testing.T) {
	f := newFixture(t, "default_ttl: 10m\n")
	f.env.Host.Provide(kernel.SvcPolicy, fixedPolicy{})
	f.env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(taskstest.Agent("coder", "Coder", "", nexusv1.Tier_TIER_HEAVY)))
	a := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED)
	if a.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
		t.Fatalf("level = %s", a.Level)
	}
	if ttl := a.ExpiresAt.AsTime().Sub(a.CreatedAt.AsTime()); ttl != 10*time.Minute {
		t.Fatalf("ttl = %s", ttl)
	}
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	approve := nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	bio := nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
	cases := []struct {
		name     string
		level    nexusv1.TrustLevel
		verifier Verifier
		mutate   func(r *nexusv1.DecideRequest)
		approve  bool
		code     connect.Code // 0 = success
	}{
		{name: "approve tap", level: approve, approve: true},
		{name: "deny tap", level: approve, approve: false},
		{name: "tap without challenge", level: approve, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.Challenge = nil }},
		{name: "wrong hash", level: approve, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.ActionSha256 = []byte("x") }, code: connect.CodeInvalidArgument},
		{name: "wrong challenge", level: approve, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.Challenge = []byte("x") }, code: connect.CodeInvalidArgument},
		{name: "biometric unsigned", level: bio, approve: true, code: connect.CodePermissionDenied},
		{name: "biometric no challenge", level: bio, approve: true, mutate: func(r *nexusv1.DecideRequest) {
			r.Decision.Signature, r.Decision.Challenge = []byte("sig"), nil
		}, code: connect.CodeInvalidArgument},
		{name: "biometric dev mode", level: bio, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.Signature = []byte("sig") }},
		{name: "biometric verified", level: bio, verifier: verifier{}, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.Signature = []byte("sig") }},
		{name: "biometric bad signature", level: bio, verifier: verifier{err: errors.New("bad")}, approve: true, mutate: func(r *nexusv1.DecideRequest) { r.Decision.Signature = []byte("sig") }, code: connect.CodePermissionDenied},
		{name: "signed decision expired", level: approve, approve: true, mutate: func(r *nexusv1.DecideRequest) {
			r.Decision.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
		}, code: connect.CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "")
			if c.verifier != nil {
				f.env.Host.Provide(kernel.SvcSecurity, c.verifier)
			}
			a := f.request(t, c.level)
			req := decision(a, c.approve)
			if c.mutate != nil {
				c.mutate(req)
			}
			got, err := f.b.Decide(ctx, req)
			if c.code != 0 {
				if connect.CodeOf(err) != c.code {
					t.Fatalf("err = %v, want %s", err, c.code)
				}
				if len(f.disp.Resolutions) != 0 || f.obs.count() != 0 {
					t.Fatal("rejected decision had side effects")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := stateDenied
			if c.approve {
				want = stateApproved
			}
			if got.State != want {
				t.Fatalf("state = %s", got.State)
			}
			if len(f.disp.Resolutions) != 1 || f.disp.Resolutions[0].RunId != "run_1" || f.disp.Resolutions[0].Decision.DeviceId != "phone" {
				t.Fatalf("resolutions = %+v", f.disp.Resolutions)
			}
			if f.obs.count() != 1 {
				t.Fatal("task engine not notified")
			}
			if _, err := f.b.Decide(ctx, decision(a, true)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("second decide err = %v", err)
			}
		})
	}
}

func TestDecideUnknown(t *testing.T) {
	f := newFixture(t, "")
	if _, err := f.b.Decide(context.Background(), &nexusv1.DecideRequest{}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
	_, err := f.b.Decide(context.Background(), &nexusv1.DecideRequest{Decision: &nexusv1.SignedApproval{ApprovalId: "apr_x"}})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestExpiry(t *testing.T) {
	f := newFixture(t, "")
	ctx := context.Background()
	a := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_APPROVE)
	keep := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_APPROVE)
	_ = keep
	// Move the clock past a's expiry only for the decide check.
	f.b.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, err := f.b.Decide(ctx, decision(a, true)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	got, _ := f.b.Get(ctx, a.Id)
	if got.State != stateExpired {
		t.Fatalf("state = %s", got.State)
	}
	n, err := f.b.ExpireDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireDue = %d, %v", n, err)
	}
	if len(f.disp.Resolutions) != 2 || f.disp.Resolutions[0].Decision != nil || f.obs.count() != 2 {
		t.Fatalf("expiry not forwarded: %+v", f.disp.Resolutions)
	}
}

func TestExpirerGoroutine(t *testing.T) {
	f := newFixture(t, "sweep_interval: 10ms\n")
	a := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_APPROVE)
	f.b.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if err := f.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	taskstest.Eventually(t, 2*time.Second, func() bool {
		got, _ := f.b.Get(context.Background(), a.Id)
		return got.State == stateExpired
	})
	if err := f.m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	f := newFixture(t, "")
	a := f.request(t, nexusv1.TrustLevel_TRUST_LEVEL_APPROVE)
	srv := httptest.NewServer(f.env.Host.Mux)
	defer srv.Close()
	c := nexusv1connect.NewApprovalServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()
	list, err := c.ListApprovals(ctx, connect.NewRequest(&nexusv1.ListApprovalsRequest{}))
	if err != nil || len(list.Msg.Approvals) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	resp, err := c.Decide(ctx, connect.NewRequest(decision(a, true)))
	if err != nil || resp.Msg.Approval.State != stateApproved {
		t.Fatalf("decide = %v, %v", resp, err)
	}
}
