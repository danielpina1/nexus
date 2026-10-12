package agentloop

import (
	"context"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// A run tool that asks RunSpec.Gate for a SPEND approval inside a native
// loop run gets the loop's gate: the Approval is emitted on the run's event
// stream and Resolve on the run answers it (here: denied → run resumes).
func TestRunGateBoundToLoopGate(t *testing.T) {
	rg := kernel.NewRunGate()
	var outcome kernel.ApprovalOutcome
	pay := kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "browser.click", ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE},
		F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			out, err := rg.Approve(ctx, kernel.ApprovalRequest{Class: nexusv1.ActionClass_ACTION_CLASS_SPEND, Title: "pay", Payload: map[string]any{"total": "9,99 €"}})
			outcome = out
			if err != nil {
				return kernel.ToolResult{}, err
			}
			return kernel.ToolOutput(map[string]bool{"approved": out.Approved})
		},
	}
	l := &fakeLLM{steps: []step{callTools(kernel.LLMToolCall{ID: "c1", Name: "browser.click", Arguments: `{}`}), answer("stopped", 0)}}
	cfg := baseCfg(l)
	cfg.Tools = []kernel.Tool{pay}
	cfg.RunGate = rg
	r, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rg.Bound() {
		t.Fatal("RunGate not bound")
	}
	var evs []*nexusv1.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
		if a := ev.GetApproval(); a != nil {
			if a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_SPEND || a.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
				t.Fatalf("approval %v", a)
			}
			if err := r.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_DENIED}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if outcome.Approved || outcome.Reason == "" {
		t.Fatalf("outcome %+v", outcome)
	}
	var resumed bool
	for _, ev := range evs {
		if ev.GetTaskUpdated().GetTask().GetState() == nexusv1.TaskState_TASK_STATE_RUNNING {
			resumed = true
		}
	}
	if !resumed {
		t.Fatal("denial did not resume the run")
	}
	if Approver(nil) != nil {
		t.Fatal("Approver(nil) must be nil")
	}
}
