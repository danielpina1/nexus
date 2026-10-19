package architect

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// With the real task engine, TestRun prefers sandbox tasks: the draft runs
// as a real (sandboxed) task, every gated action is auto-denied without
// asking the owner and the refusals end up in the test results.
func TestTestRunUsesSandboxTasks(t *testing.T) {
	f := newFixture(t, "")
	d := f.draft(t, "tell me when prices drop")

	// Swap the fake engine for the real one (same host, same broker).
	disp := &taskstest.Dispatcher{}
	f.env.Host.Provide(kernel.SvcDispatcher, kernel.Dispatcher(disp))
	eng, err := tasks.NewEngine(f.env.Host, f.env.Store, f.env.Bus, tasks.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	f.env.Host.Provide(kernel.SvcTasks, kernel.TaskEngine(eng))

	// The "node": accept the run, try a purchase (denied), fetch, answer.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			a := disp.LastAssignment()
			if a == nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			send := func(ev *nexusv1.Event) {
				ev.RunId = a.RunId
				if err := eng.HandleRunEvent(context.Background(), "node-1", &nexusv1.RunEvent{Event: ev}); err != nil {
					t.Errorf("run event: %v", err)
				}
			}
			send(&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: "checking"}}})
			send(&nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{Title: "Email the owner",
				ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, Level: nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC}}})
			send(&nexusv1.Event{Body: &nexusv1.Event_ToolCall{ToolCall: &nexusv1.ToolCall{Tool: "web.fetch"}}})
			send(&nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
				State: nexusv1.TaskState_TASK_STATE_SUCCEEDED, Summary: "The price is 129; I would have emailed you."}}}})
			return
		}
		t.Errorf("the sandbox task was never assigned")
	}()

	resp, err := f.svc.TestRun(context.Background(), connect.NewRequest(&nexusv1.TestRunRequest{DraftId: d.Id, Wait: true}))
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Msg.Draft.TestResults[0]
	if r.Mode != "sandbox_task" || r.TaskId == "" || r.Error != "" {
		t.Fatalf("result %+v", r)
	}
	task, err := eng.GetTask(context.Background(), r.TaskId)
	if err != nil || task.Source != tasks.SourceSandbox {
		t.Fatalf("task %v, %v", task, err)
	}
	a := disp.LastAssignment()
	for _, tool := range a.Agent.Tools {
		if tool == "email.send" || tool == "notify" {
			t.Fatalf("sandbox run got %s: %v", tool, a.Agent.Tools)
		}
	}
	if offered, _ := f.broker.List(context.Background(), nil); len(offered) != 0 {
		t.Fatalf("approvals offered to the owner: %v", offered)
	}
	blocked := strings.Join(r.BlockedActions, "\n")
	if !strings.Contains(blocked, "write_external: Email the owner") || !strings.Contains(blocked, "email.send") {
		t.Fatalf("blocked = %v", r.BlockedActions)
	}
	if !contains(r.ToolCalls, "web.fetch") {
		t.Fatalf("tool calls = %v", r.ToolCalls)
	}
	// "no_calls: email.send" holds: the run never sent anything.
	if !strings.Contains(strings.Join(r.Checks, "\n"), "✓ no_calls: email.send") || !strings.Contains(strings.Join(r.Checks, "\n"), "✓ contains: price") {
		t.Fatalf("checks = %v", r.Checks)
	}
}
