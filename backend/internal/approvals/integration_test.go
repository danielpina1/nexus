package approvals

import (
	"context"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

var _ ResolutionObserver = (*tasks.Engine)(nil)

// A node asks for approval mid-run; the phone approves; the task resumes and
// the node receives the resolution.
func TestTaskApprovalRoundTrip(t *testing.T) {
	ctx := context.Background()
	env := taskstest.New(t)
	disp := &taskstest.Dispatcher{}
	env.Host.Provide(kernel.SvcDispatcher, disp)
	env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(taskstest.Agent("coder", "Coder", "", nexusv1.Tier_TIER_HEAVY)))
	tm, am := &tasks.Module{}, &Module{}
	for _, m := range []kernel.Module{tm, am} {
		if err := m.Init(ctx, env.Host); err != nil {
			t.Fatal(err)
		}
	}
	task, err := tm.Engine.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "push it", AgentId: "coder"})
	if err != nil {
		t.Fatal(err)
	}
	p := payload(t, map[string]any{"branch": "main"})
	err = tm.Engine.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: &nexusv1.Event{RunId: task.CurrentRunId,
		Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, Payload: p}}}})
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := am.Broker.List(ctx, []nexusv1.ApprovalState{statePending})
	if len(pending) != 1 || pending[0].NodeId != "node-1" || pending[0].TaskId != task.Id {
		t.Fatalf("pending = %+v", pending)
	}
	got, _ := tm.Engine.GetTask(ctx, task.Id)
	if got.State != nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL {
		t.Fatalf("state = %s", got.State)
	}
	if _, err := am.Broker.Decide(ctx, decision(pending[0], true)); err != nil {
		t.Fatal(err)
	}
	got, _ = tm.Engine.GetTask(ctx, task.Id)
	if got.State != nexusv1.TaskState_TASK_STATE_RUNNING {
		t.Fatalf("state after approval = %s", got.State)
	}
	if len(disp.Resolutions) != 1 || disp.Resolutions[0].RunId != task.CurrentRunId {
		t.Fatalf("resolutions = %+v", disp.Resolutions)
	}
}
