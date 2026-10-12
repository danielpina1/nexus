package approvalgate

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	secapproval "github.com/danielpina1/nexus/backend/internal/security/approval"
)

const (
	auto      = nexusv1.TrustLevel_TRUST_LEVEL_AUTO
	notify    = nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY
	approve   = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
	biometric = nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
)

type sink struct {
	mu  sync.Mutex
	evs []*nexusv1.Event
	ch  chan *nexusv1.Event
}

func newSink() *sink { return &sink{ch: make(chan *nexusv1.Event, 64)} }

func (s *sink) emit(ev *nexusv1.Event) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	s.ch <- ev
}

func (s *sink) next(t *testing.T) *nexusv1.Event {
	t.Helper()
	select {
	case ev := <-s.ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
		return nil
	}
}

func (s *sink) all() []*nexusv1.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*nexusv1.Event(nil), s.evs...)
}

func TestLevel(t *testing.T) {
	agent := &nexusv1.Agent{Policy: map[string]nexusv1.TrustLevel{
		"compute":        approve,
		"spend":          auto, // loosening ignored
		"WRITE_EXTERNAL": biometric,
		"substitutions":  approve, // not a class
	}}
	cases := []struct {
		agent *nexusv1.Agent
		class nexusv1.ActionClass
		want  nexusv1.TrustLevel
	}{
		{nil, nexusv1.ActionClass_ACTION_CLASS_READ, auto},
		{nil, nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, notify},
		{nil, nexusv1.ActionClass_ACTION_CLASS_COMPUTE, auto},
		{nil, nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, approve},
		{nil, nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE, biometric},
		{nil, nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED, approve},
		{nil, nexusv1.ActionClass(99), approve},
		{agent, nexusv1.ActionClass_ACTION_CLASS_COMPUTE, approve},
		{agent, nexusv1.ActionClass_ACTION_CLASS_SPEND, biometric},
		{agent, nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, biometric},
		{agent, nexusv1.ActionClass_ACTION_CLASS_READ, auto},
	}
	for _, c := range cases {
		if got := Level(c.agent, c.class, nil); got != c.want {
			t.Errorf("Level(%v, %s) = %s, want %s", c.agent.GetPolicy(), c.class, got, c.want)
		}
	}
	// Node-local levels replace defaults; agents still only tighten.
	levels := map[nexusv1.ActionClass]nexusv1.TrustLevel{nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH: auto}
	if got := Level(nil, nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, levels); got != auto {
		t.Errorf("levels override = %s", got)
	}
}

func newGate(s *sink, agent *nexusv1.Agent) *Gate {
	return New(Config{TaskID: "tsk_1", RunID: "run_1", Agent: agent, NodeID: "n1", Emit: s.emit})
}

var compute = map[string]nexusv1.TrustLevel{"compute": approve}

func req() Request {
	return Request{Class: nexusv1.ActionClass_ACTION_CLASS_COMPUTE, Title: "Run ls", Summary: "ls -la",
		Payload: map[string]any{"action": "shell.exec", "command": "ls -la", "n": 3}}
}

func TestAutoAndNotify(t *testing.T) {
	s := newSink()
	g := newGate(s, nil)
	out, err := g.Check(context.Background(), req())
	if err != nil || !out.Approved || out.Asked || out.Level != auto {
		t.Fatalf("auto: %+v %v", out, err)
	}
	if len(s.all()) != 0 {
		t.Fatalf("auto emitted %v", s.all())
	}
	r := req()
	r.Class = nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL
	out, err = g.Check(context.Background(), r)
	if err != nil || !out.Approved || out.Asked || out.Level != notify {
		t.Fatalf("notify: %+v %v", out, err)
	}
	evs := s.all()
	if len(evs) != 1 || evs[0].GetNotice().GetTitle() != "Run ls" || evs[0].RunId != "run_1" {
		t.Fatalf("notify events %v", evs)
	}
}

func startCheck(g *Gate, r Request) (chan Outcome, chan error) {
	outc, errc := make(chan Outcome, 1), make(chan error, 1)
	go func() {
		o, err := g.Check(context.Background(), r)
		outc <- o
		errc <- err
	}()
	return outc, errc
}

func expectApproval(t *testing.T, s *sink) *nexusv1.Approval {
	t.Helper()
	a := s.next(t).GetApproval()
	if a == nil {
		t.Fatal("first event is not an approval")
	}
	if st := s.next(t).GetTaskUpdated().GetTask().GetState(); st != nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL {
		t.Fatalf("second event state %s", st)
	}
	return a
}

func TestApproveFlow(t *testing.T) {
	s := newSink()
	g := newGate(s, &nexusv1.Agent{Id: "shell", Policy: compute})
	outc, errc := startCheck(g, req())
	a := expectApproval(t, s)
	want, _ := secapproval.ActionHash(a.Payload)
	if !bytes.Equal(a.PayloadHash, want) || len(a.PayloadHash) != 32 {
		t.Fatal("payload hash is not ActionHash(payload)")
	}
	if len(a.Id) < 10 || a.Id[:4] != "apr_" || a.Level != approve || a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_COMPUTE ||
		a.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING || a.AgentId != "shell" || a.NodeId != "n1" ||
		a.Payload.Fields["command"].GetStringValue() != "ls -la" || a.TaskId != "tsk_1" || a.RunId != "run_1" {
		t.Fatalf("approval %v", a)
	}
	if d := a.ExpiresAt.AsTime().Sub(a.CreatedAt.AsTime()); d != DefaultTTL {
		t.Fatalf("ttl %v", d)
	}
	if got := g.Pending(); len(got) != 1 || got[0] != a.Id {
		t.Fatalf("pending %v", got)
	}
	// A resolution for some other approval is refused.
	if err := g.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: "apr_other"}}); !errors.Is(err, ErrUnknownApproval) {
		t.Fatalf("other id: %v", err)
	}
	res := &nexusv1.ApprovalResolution{RunId: "run_1",
		Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED, PayloadHash: a.PayloadHash},
		Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: true, ActionSha256: a.PayloadHash}}
	if err := g.Resolve(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	if out := <-outc; !out.Approved || !out.Asked || out.ApprovalID != a.Id {
		t.Fatalf("outcome %+v", out)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if st := s.next(t).GetTaskUpdated().GetTask().GetState(); st != nexusv1.TaskState_TASK_STATE_RUNNING {
		t.Fatalf("after approval state %s", st)
	}
	if len(g.Pending()) != 0 {
		t.Fatal("pending not cleared")
	}
}

func TestDenyVariants(t *testing.T) {
	cases := []struct {
		name   string
		res    func(a *nexusv1.Approval) *nexusv1.ApprovalResolution
		reason string
	}{
		{"denied", func(a *nexusv1.Approval) *nexusv1.ApprovalResolution {
			return &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_DENIED},
				Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: false, ActionSha256: a.PayloadHash}}
		}, "denied"},
		{"expired (no decision)", func(a *nexusv1.Approval) *nexusv1.ApprovalResolution {
			return &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_EXPIRED}}
		}, "expired"},
		{"approve for a different action", func(a *nexusv1.Approval) *nexusv1.ApprovalResolution {
			return &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id},
				Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: true, ActionSha256: make([]byte, 32)}}
		}, "action hash mismatch"},
		{"hub swapped payload", func(a *nexusv1.Approval) *nexusv1.ApprovalResolution {
			return &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED, PayloadHash: []byte("x")}}
		}, "action hash mismatch"},
		{"no id, single pending", func(a *nexusv1.Approval) *nexusv1.ApprovalResolution {
			return &nexusv1.ApprovalResolution{Decision: &nexusv1.SignedApproval{Approve: false}}
		}, "denied"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSink()
			g := newGate(s, &nexusv1.Agent{Policy: compute})
			outc, errc := startCheck(g, req())
			a := expectApproval(t, s)
			if err := g.Resolve(context.Background(), c.res(a)); err != nil {
				t.Fatal(err)
			}
			out := <-outc
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			if out.Approved || out.Reason != c.reason {
				t.Fatalf("outcome %+v, want reason %q", out, c.reason)
			}
			// No RUNNING is emitted after a denial (the caller decides).
			select {
			case ev := <-s.ch:
				t.Fatalf("unexpected event %v", ev)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

func TestCancelAndClose(t *testing.T) {
	s := newSink()
	g := newGate(s, &nexusv1.Agent{Policy: compute})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := g.Check(ctx, req()); errc <- err }()
	expectApproval(t, s)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx cancel: %v", err)
	}

	_, errc2 := startCheck(g, req())
	a := expectApproval(t, s)
	g.Close()
	g.Close() // idempotent
	if err := <-errc2; !errors.Is(err, ErrClosed) {
		t.Fatalf("close: %v", err)
	}
	if err := g.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("resolve after close: %v", err)
	}
	if _, err := g.Check(context.Background(), Request{Class: nexusv1.ActionClass_ACTION_CLASS_READ}); !errors.Is(err, ErrClosed) {
		t.Fatalf("check after close: %v", err)
	}
}

func TestLocalExpiry(t *testing.T) {
	s := newSink()
	g := New(Config{RunID: "r", Agent: &nexusv1.Agent{Policy: compute}, Emit: s.emit, TTL: time.Millisecond})
	g.grace = 0
	outc, errc := startCheck(g, req())
	expectApproval(t, s)
	select {
	case o := <-outc:
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
		if o.Approved || o.Reason != "expired" {
			t.Fatalf("outcome %+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local expiry did not fire")
	}
}

func TestPayloadMustBeJSON(t *testing.T) {
	g := newGate(newSink(), &nexusv1.Agent{Policy: compute})
	r := req()
	r.Payload = map[string]any{"bad": make(chan int)}
	if _, err := g.Check(context.Background(), r); err == nil {
		t.Fatal("expected payload error")
	}
}
