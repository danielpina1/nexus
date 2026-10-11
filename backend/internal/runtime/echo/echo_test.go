package echo

import (
	"context"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func start(t *testing.T, prompt string) kernel.Run {
	t.Helper()
	r, err := (&Runtime{}).Start(context.Background(), kernel.RunSpec{
		Assignment: &nexusv1.RunAssignment{RunId: "r1", TaskId: "t1", Prompt: prompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func next(t *testing.T, r kernel.Run) *nexusv1.Event {
	t.Helper()
	select {
	case ev, ok := <-r.Events():
		if !ok {
			t.Fatal("events closed")
		}
		if ev.RunId != "r1" || ev.TaskId != "t1" {
			t.Fatalf("ids not set: %v", ev)
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	return nil
}

func drain(t *testing.T, r kernel.Run) []*nexusv1.Event {
	var out []*nexusv1.Event
	for ev := range r.Events() {
		out = append(out, ev)
	}
	return out
}

func TestModuleRegistersRuntime(t *testing.T) {
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	if err := (Module{}).Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	rt, ok := h.Runtimes().Get("echo")
	if !ok {
		t.Fatal("echo not registered")
	}
	if _, ok := rt.Available(context.Background()); !ok {
		t.Fatal("echo unavailable")
	}
}

func TestEchoStreamsWords(t *testing.T) {
	r := start(t, "a b  c")
	evs := drain(t, r)
	var text string
	for _, ev := range evs {
		text += ev.GetRunOutput().GetText()
	}
	if text != "a b c" {
		t.Fatalf("text %q", text)
	}
	last := evs[len(evs)-1].GetTaskUpdated().GetTask()
	if last.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || last.Summary != "echoed 3 words" {
		t.Fatalf("last %v", last)
	}
}

func TestEchoAsk(t *testing.T) {
	r := start(t, "?ask")
	next(t, r) // word
	q := next(t, r).GetQuestion()
	if q == nil || q.QuestionId == "" {
		t.Fatal("expected question")
	}
	if st := next(t, r).GetTaskUpdated().GetTask().GetState(); st != nexusv1.TaskState_TASK_STATE_WAITING_INPUT {
		t.Fatalf("state %v", st)
	}
	if err := r.Send(context.Background(), &nexusv1.RunInput{RunId: "r1", QuestionId: q.QuestionId, Text: "blue"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range drain(t, r) {
		if ev.GetRunOutput().GetText() == "\nanswer: blue" {
			found = true
		}
	}
	if !found {
		t.Fatal("answer not echoed")
	}
}

func TestEchoApproval(t *testing.T) {
	for _, approve := range []bool{true, false} {
		r := start(t, "?approve")
		next(t, r)
		apr := next(t, r).GetApproval()
		if apr == nil || apr.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING || len(apr.PayloadHash) != 32 {
			t.Fatalf("approval %v", apr)
		}
		next(t, r) // WAITING_APPROVAL
		// A resolution for another approval is ignored.
		_ = r.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: "other"}, Decision: &nexusv1.SignedApproval{Approve: !approve}})
		_ = r.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: apr, Decision: &nexusv1.SignedApproval{ApprovalId: apr.Id, Approve: approve}})
		evs := drain(t, r)
		want := nexusv1.TaskState_TASK_STATE_FAILED
		if approve {
			want = nexusv1.TaskState_TASK_STATE_SUCCEEDED
		}
		if got := evs[len(evs)-1].GetTaskUpdated().GetTask().GetState(); got != want {
			t.Fatalf("approve=%v: final %v", approve, got)
		}
	}
}

func TestEchoCancel(t *testing.T) {
	r := start(t, "?ask")
	next(t, r)
	_ = r.Cancel(context.Background())
	evs := drain(t, r)
	if got := evs[len(evs)-1].GetTaskUpdated().GetTask().GetState(); got != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("final %v", got)
	}
	if err := r.Send(context.Background(), &nexusv1.RunInput{}); err == nil {
		t.Fatal("send after cancel should fail")
	}
}
