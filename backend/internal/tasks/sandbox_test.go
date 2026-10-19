package tasks

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Architect sandbox test runs (docs/ARCHITECT.md): a real task for an
// unpublished agent, strict policy, every gated action auto-denied and
// recorded, hidden from lists, small budget and time cap.
func TestCreateSandboxTask(t *testing.T) {
	broker := &taskstest.Broker{}
	f := newFixture(t, func(f *fixture) { f.env.Host.Provide(kernel.SvcApprovals, kernel.ApprovalBroker(broker)) })
	ctx := context.Background()
	reg := f.env.Host.Tools()
	for name, c := range map[string]nexusv1.ActionClass{
		"web.fetch":   nexusv1.ActionClass_ACTION_CLASS_READ,
		"calc":        nexusv1.ActionClass_ACTION_CLASS_COMPUTE,
		"email.send":  nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL,
		"vault.write": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		"shop.buy":    nexusv1.ActionClass_ACTION_CLASS_SPEND,
	} {
		spec := kernel.ToolSpec{Name: name, ActionClass: c}
		if err := reg.Register(kernel.ToolFunc{S: spec, F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]any{"ok": true})
		}}); err != nil {
			t.Fatal(err)
		}
	}
	draft := &nexusv1.Agent{Id: "sandbox-price-watcher", Name: "Price watcher", Runtime: "anthrex", Enabled: false,
		Tools:  []string{"web.fetch", "calc", "email.send", "vault.write", "shop.buy", "spawn_agent", "ask_user", "anthrex.run", "browser"},
		Policy: map[string]nexusv1.TrustLevel{"spend": nexusv1.TrustLevel_TRUST_LEVEL_AUTO, "write_external": nexusv1.TrustLevel_TRUST_LEVEL_AUTO},
		Limits: &nexusv1.AgentLimits{MaxCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 500}, MaxRuntimeSeconds: 3600, MaxTasks: 10},
		Memory: &nexusv1.AgentMemory{Writes: "agents/"}}
	params := mustStruct(t, map[string]any{"architect_sandbox": true, ParamRuntimeHint: "codex"})
	task, err := f.e.CreateSandboxTask(ctx, draft, "Watch prices.", &nexusv1.CreateTaskRequest{Prompt: "watch the drill", Source: "architect", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, task, stAssigned)
	if task.Source != SourceSandbox || !strings.HasPrefix(task.Title, "[sandbox]") || task.AgentId != "eph:sandbox-price-watcher" {
		t.Fatalf("task = %+v", task)
	}
	a := f.lastAssignment(t, task.Id)
	if got := strings.Join(a.Agent.Tools, ","); got != "web.fetch,calc" {
		t.Fatalf("sandbox tools = %s", got)
	}
	for _, c := range []string{"write_internal", "write_external", "spend", "destructive", "code_push", "device_control"} {
		if a.Agent.Policy[c] != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			t.Fatalf("policy %s = %v", c, a.Agent.Policy[c])
		}
	}
	if a.Agent.Runtime == "anthrex" || a.Agent.GetMemory().GetWrites() != "" {
		t.Fatalf("runtime %q memory.writes %q", a.Agent.Runtime, a.Agent.GetMemory().GetWrites())
	}
	if l := a.Agent.Limits; l.GetMaxCost().GetMinorUnits() > 20 || l.GetMaxRuntimeSeconds() > 300 {
		t.Fatalf("limits = %v", l)
	}
	c := a.Context.AsMap()
	if c["sandbox"] != true || c["architect_sandbox"] != true || c[ParamRuntimeHint] != nil {
		t.Fatalf("context = %v", c)
	}
	if cl, _ := c["claude"].(map[string]any); cl["coding"] != false {
		t.Fatalf("claude block = %v", c["claude"])
	}
	if !strings.Contains(a.Instructions, "Sandbox test run") {
		t.Fatalf("instructions = %q", a.Instructions)
	}
	f.e.timerMu.Lock()
	armed := f.e.timers[task.Id] != nil
	f.e.timerMu.Unlock()
	if !armed {
		t.Fatal("no time cap")
	}

	// Hidden from task lists, reachable by id.
	list, _, err := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.Id == task.Id {
			t.Fatal("sandbox task listed")
		}
	}

	// Gated actions are denied at once, never offered for approval.
	f.runEvent(t, f.get(t, task.Id), &nexusv1.Event{Body: &nexusv1.Event_ToolCall{ToolCall: &nexusv1.ToolCall{Tool: "web.fetch"}}})
	f.runEvent(t, f.get(t, task.Id), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{
		Title: "Buy drill €89", ActionClass: nexusv1.ActionClass_ACTION_CLASS_SPEND, Level: nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC}}})
	f.runEvent(t, f.get(t, task.Id), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{
		Title: "git push", ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH}}})
	if offered, _ := broker.List(ctx, nil); len(offered) != 0 {
		t.Fatalf("approvals offered to the owner: %v", offered)
	}
	res := f.disp.ResolutionsSnapshot()
	if len(res) != 2 || res[0].Approval.State != nexusv1.ApprovalState_APPROVAL_STATE_DENIED {
		t.Fatalf("resolutions = %v", res)
	}
	if cur := f.get(t, task.Id); cur.State != stRunning {
		t.Fatalf("state = %s", stateName(cur.State))
	}

	// A sub-task is refused (one task per sandbox tree).
	if _, err := f.tool(t, "spawn_agent", task.Id, map[string]any{"prompt": "x", "agent_id": "helper"}); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("spawn in sandbox: %v", err)
	}

	calls, blocked, err := f.e.SandboxReport(ctx, task.Id)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(blocked, "\n")
	for _, want := range []string{"email.send", "vault.write", "shop.buy", "spawn_agent", "anthrex.run", "browser", "spend: Buy drill €89", "code_push: git push"} {
		if !strings.Contains(all, want) {
			t.Errorf("blocked lacks %q:\n%s", want, all)
		}
	}
	if strings.Join(calls, ",") != "web.fetch" {
		t.Fatalf("calls = %v", calls)
	}

	// The budget cap stops the run; no result notice either way.
	f.costEvent(t, task.Id, 25)
	got := f.get(t, task.Id)
	wantState(t, got, stFailed)
	if !strings.Contains(got.Summary, "budget") {
		t.Fatalf("summary = %q", got.Summary)
	}
	if n := len(notices(f, task.Id)); n != 0 {
		t.Fatalf("notices = %d", n)
	}
	if _, _, err := f.e.SandboxReport(ctx, root(t, f)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("report of a normal task: %v", err)
	}
}

func root(t *testing.T, f *fixture) string {
	return f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "helper"}).Id
}
