package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// ---------------------------------------------------------------- helpers

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// teamFixture is a fixture with team and skill catalogs.
func teamFixture(t *testing.T, teams *taskstest.Teams, extra ...*nexusv1.Agent) *fixture {
	t.Helper()
	return newFixture(t, func(f *fixture) {
		for _, a := range extra {
			f.agents.Add(a, "Instructions for "+a.Id)
		}
		if teams != nil {
			f.env.Host.Provide(kernel.SvcTeams, teams)
		}
	})
}

func agentWith(id string, mod func(a *nexusv1.Agent)) *nexusv1.Agent {
	a := taskstest.Agent(id, id, "agent "+id, nexusv1.Tier_TIER_MEDIUM)
	a.Tools = []string{"memory.search"}
	if mod != nil {
		mod(a)
	}
	return a
}

// finish reports a terminal state for the task's current run.
func (f *fixture) finish(t *testing.T, id string, state nexusv1.TaskState, summary string) {
	t.Helper()
	f.runEvent(t, f.get(t, id), done(state, summary))
}

func (f *fixture) costEvent(t *testing.T, id string, cents int64) {
	t.Helper()
	f.runEvent(t, f.get(t, id), &nexusv1.Event{Body: &nexusv1.Event_Cost{Cost: &nexusv1.CostRecorded{Cost: &nexusv1.Cost{
		Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: cents}}}}})
}

// tool invokes a hub tool as the task's current run.
func (f *fixture) tool(t *testing.T, name, taskID string, in any) (map[string]any, error) {
	t.Helper()
	tl, ok := f.env.Host.Tools().Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	b, _ := json.Marshal(in)
	task := f.get(t, taskID)
	res, err := tl.Invoke(context.Background(), kernel.ToolCall{TaskID: taskID, RunID: task.CurrentRunId, AgentID: task.AgentId, Input: b})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("tool output: %v", err)
	}
	return out, nil
}

// dispatched reports whether the task's current run reached the dispatcher.
func (f *fixture) dispatched(t *testing.T, id string) bool {
	task := f.get(t, id)
	for _, a := range f.disp.AssignmentsFor(id) {
		if a.RunId == task.CurrentRunId && task.State == stAssigned {
			return true
		}
	}
	return false
}

func (f *fixture) children(t *testing.T, id string) []*nexusv1.Task {
	t.Helper()
	kids, err := f.e.ChildTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return kids
}

func (f *fixture) lastAssignment(t *testing.T, taskID string) *nexusv1.RunAssignment {
	t.Helper()
	as := f.disp.AssignmentsFor(taskID)
	if len(as) == 0 {
		t.Fatalf("task %s was never assigned", taskID)
	}
	return as[len(as)-1]
}

func hasTool(a *nexusv1.RunAssignment, name string) bool {
	for _, n := range a.GetAgent().GetTools() {
		if n == name {
			return true
		}
	}
	return false
}

func squad(limits *nexusv1.TeamLimits) *taskstest.Teams {
	tm := taskstest.Team("squad", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL,
		taskstest.Member("researcher", "researcher", 2), taskstest.Member("checker", "checker", 1))
	tm.Limits = limits
	tm.BundleApprovals = true
	return taskstest.NewTeams(tm)
}

func squadAgents() []*nexusv1.Agent {
	return []*nexusv1.Agent{
		agentWith("lead", func(a *nexusv1.Agent) {
			a.Tools = []string{"memory.search", "web.search"}
			a.Policy = map[string]nexusv1.TrustLevel{"write_external": nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC}
		}),
		agentWith("researcher", nil),
		agentWith("checker", nil),
	}
}

// ---------------------------------------------------------------- complex agents

func TestSkillsAndMemoryInjected(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		f.agents.Add(agentWith("writer", func(a *nexusv1.Agent) {
			a.Skills = []string{"report", "missing"}
			a.Memory = &nexusv1.AgentMemory{Reads: []string{"[[Prefs]]", "notes/", "search: heat pumps"}}
		}), "Write well.")
		f.env.Host.Provide(kernel.SvcSkills, taskstest.NewSkills(&nexusv1.Skill{Id: "report", Name: "Report", Enabled: true,
			Instructions: "Summary first.", Tools: []string{"vault.write"}}))
		f.env.Host.Provide(kernel.SvcVault, &fakeVault{root: t.TempDir(), notes: map[string]string{
			"Prefs": "Likes oat milk.", "research/heat.md": "Heat pumps are efficient."}})
	})
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "write", AgentId: "writer"})
	a := f.lastAssignment(t, task.Id)
	if !strings.Contains(a.Instructions, "## Skill: Report\nSummary first.") || !strings.HasPrefix(a.Instructions, "Write well.") {
		t.Fatalf("instructions = %q", a.Instructions)
	}
	if !hasTool(a, "vault.write") || !hasTool(a, "memory.search") {
		t.Fatalf("tools = %v", a.Agent.Tools)
	}
	mem := a.Context.Fields["memory"].GetListValue().GetValues()
	if len(mem) != 2 {
		t.Fatalf("memory = %v", a.Context.Fields["memory"])
	}
	got := mem[0].GetStructValue().Fields["content"].GetStringValue() + "|" + mem[1].GetStructValue().Fields["content"].GetStringValue()
	if !strings.Contains(got, "oat milk") || !strings.Contains(got, "efficient") {
		t.Fatalf("memory content = %q", got)
	}
}

func TestInputSchemaValidated(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		f.agents.Add(agentWith("typed", func(a *nexusv1.Agent) {
			a.InputSchema = mustStruct(t, map[string]any{"type": "object", "required": []any{"topic"},
				"properties": map[string]any{"topic": map[string]any{"type": "string"}}})
		}), "")
	})
	bad := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "typed", Params: mustStruct(t, map[string]any{"topic": 3})})
	wantState(t, bad, stFailed)
	if !strings.Contains(bad.Summary, "input schema") {
		t.Fatalf("summary = %q", bad.Summary)
	}
	ok := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "typed", Params: mustStruct(t, map[string]any{"topic": "x"})})
	wantState(t, ok, stAssigned)
}

func outputAgent(t *testing.T) *nexusv1.Agent {
	return agentWith("structured", func(a *nexusv1.Agent) {
		a.OutputSchema = mustStruct(t, map[string]any{"type": "object", "required": []any{"verdict"},
			"properties": map[string]any{"verdict": map[string]any{"type": "string", "enum": []any{"yes", "no"}}}})
	})
}

func TestOutputSchemaValidAndRepair(t *testing.T) {
	f := teamFixture(t, nil, outputAgent(t))
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "decide", AgentId: "structured"})
	if a := f.lastAssignment(t, task.Id); !strings.Contains(a.Instructions, "Output format (required)") || a.Context.Fields["output_schema"] == nil {
		t.Fatalf("assignment lacks output instructions: %q", a.Instructions)
	}
	// Invalid → one repair run with the validation error as hint.
	f.finish(t, task.Id, stSucceeded, "I think yes.")
	got := f.get(t, task.Id)
	wantState(t, got, stAssigned)
	if got.Phase != phaseRepairing || len(f.disp.AssignmentsFor(task.Id)) != 2 {
		t.Fatalf("phase %q, assignments %d", got.Phase, len(f.disp.AssignmentsFor(task.Id)))
	}
	hint := f.lastAssignment(t, task.Id).Context.Fields["escalation_hint"].GetStringValue()
	if !strings.Contains(hint, "no JSON") && !strings.Contains(hint, "JSON") {
		t.Fatalf("hint = %q", hint)
	}
	// Valid (in a code fence) → succeeded with the structured result.
	f.finish(t, task.Id, stSucceeded, "```json\n{\"verdict\": \"yes\"}\n```")
	got = f.get(t, task.Id)
	wantState(t, got, stSucceeded)
	if got.Result.Fields["verdict"].GetStringValue() != "yes" || got.Phase != "" {
		t.Fatalf("result = %v phase %q", got.Result, got.Phase)
	}

	// Two invalid answers → failed.
	t2 := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "decide", AgentId: "structured"})
	f.finish(t, t2.Id, stSucceeded, `{"verdict": "maybe"}`)
	f.finish(t, t2.Id, stSucceeded, `{"verdict": "perhaps"}`)
	got = f.get(t, t2.Id)
	wantState(t, got, stFailed)
	if !strings.Contains(got.Summary, "output schema") || got.Result.Fields["error_kind"].GetStringValue() != "invalid_output" {
		t.Fatalf("summary = %q result %v", got.Summary, got.Result)
	}
}

func TestReflectionLoop(t *testing.T) {
	writer := agentWith("writer", func(a *nexusv1.Agent) {
		a.Review = &nexusv1.AgentReview{Critic: "critic", Rounds: 2, Criteria: "cites sources"}
	})
	f := teamFixture(t, nil, writer, agentWith("critic", nil))
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "write a report", AgentId: "writer"})
	f.finish(t, task.Id, stSucceeded, "Draft 1")

	got := f.get(t, task.Id)
	wantState(t, got, stRunning)
	if got.Phase != phaseReviewing || got.Summary != "Draft 1" {
		t.Fatalf("phase %q summary %q", got.Phase, got.Summary)
	}
	kids := f.children(t, task.Id)
	if len(kids) != 1 || kids[0].Role != RoleCritic || kids[0].AgentId != "critic" {
		t.Fatalf("critic task = %v", kids)
	}
	ca := f.lastAssignment(t, kids[0].Id)
	if !strings.Contains(ca.Prompt, "cites sources") || !strings.Contains(ca.Prompt, "Draft 1") || !strings.Contains(ca.Instructions, "approved") {
		t.Fatalf("critic assignment: prompt %q", ca.Prompt)
	}
	// Rejected → revision run with the feedback.
	f.finish(t, kids[0].Id, stSucceeded, `{"approved": false, "feedback": "add sources"}`)
	got = f.get(t, task.Id)
	wantState(t, got, stAssigned)
	if got.Phase != phaseRevising {
		t.Fatalf("phase = %q", got.Phase)
	}
	if hint := f.lastAssignment(t, task.Id).Context.Fields["escalation_hint"].GetStringValue(); !strings.Contains(hint, "add sources") {
		t.Fatalf("hint = %q", hint)
	}
	// Revised → reviewed again → approved → final.
	f.finish(t, task.Id, stSucceeded, "Draft 2 [1]")
	kids = f.children(t, task.Id)
	if len(kids) != 2 {
		t.Fatalf("critic tasks = %d", len(kids))
	}
	f.finish(t, kids[1].Id, stSucceeded, `{"approved": true, "feedback": "good"}`)
	got = f.get(t, task.Id)
	wantState(t, got, stSucceeded)
	if got.Summary != "Draft 2 [1]" || got.Phase != "" {
		t.Fatalf("final = %q phase %q", got.Summary, got.Phase)
	}
}

func TestReflectionLoopRoundsExhausted(t *testing.T) {
	writer := agentWith("writer", func(a *nexusv1.Agent) { a.Review = &nexusv1.AgentReview{Critic: "critic", Rounds: 1} })
	f := teamFixture(t, nil, writer, agentWith("critic", nil))
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "w", AgentId: "writer"})
	f.finish(t, task.Id, stSucceeded, "v1")
	f.finish(t, f.children(t, task.Id)[0].Id, stSucceeded, `{"approved": false, "feedback": "f1"}`)
	f.finish(t, task.Id, stSucceeded, "v2")
	f.finish(t, f.children(t, task.Id)[1].Id, stSucceeded, `{"approved": false, "feedback": "f2"}`)
	got := f.get(t, task.Id)
	wantState(t, got, stSucceeded)
	if got.Summary != "v2" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if n := len(f.children(t, task.Id)); n != 2 {
		t.Fatalf("reviews = %d", n)
	}
}

// ---------------------------------------------------------------- teams

func TestTeamRunDelegateAwait(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxParallel: 3}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "research heat pumps", TeamId: "squad"})
	wantState(t, root, stAssigned)
	if root.TeamId != "squad" || root.Role != RoleOrchestrator || root.AgentId != "lead" {
		t.Fatalf("root = %v", root)
	}
	ra := f.lastAssignment(t, root.Id)
	for _, n := range OrchestratorTools {
		if !hasTool(ra, n) {
			t.Fatalf("orchestrator lacks %s: %v", n, ra.Agent.Tools)
		}
	}
	if !strings.Contains(ra.Instructions, "you are the orchestrator") || !strings.Contains(ra.Instructions, "Mission of squad") {
		t.Fatalf("instructions = %q", ra.Instructions)
	}
	if ra.Context.Fields["team"].GetStructValue().Fields["id"].GetStringValue() != "squad" {
		t.Fatalf("team context = %v", ra.Context.Fields["team"])
	}
	f.runEvent(t, f.get(t, root.Id), output("planning"))

	out, err := f.tool(t, "team.delegate_many", root.Id, map[string]any{"items": []any{
		map[string]any{"role": "researcher", "task": "q1"},
		map[string]any{"role": "researcher", "task": "q2"},
		map[string]any{"role": "researcher", "task": "q3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := out["tasks"].([]any)
	if len(tasks) != 3 {
		t.Fatalf("delegated = %v", out)
	}
	ids := []string{}
	for _, x := range tasks {
		ids = append(ids, x.(map[string]any)["task_id"].(string))
	}
	// Pool of 2 researchers: the third waits for a slot.
	c3 := f.get(t, ids[2])
	if c3.State != stQueued || c3.Phase != phaseWaitingSlot {
		t.Fatalf("third researcher = %s/%s", stateName(c3.State), c3.Phase)
	}
	m := f.lastAssignment(t, ids[0])
	if !hasTool(m, "team.report") || !hasTool(m, "team.blackboard.write") || hasTool(m, "team.delegate") {
		t.Fatalf("member tools = %v", m.Agent.Tools)
	}
	if f.get(t, ids[0]).Role != "researcher" || f.get(t, ids[0]).Depth != 1 {
		t.Fatalf("child = %v", f.get(t, ids[0]))
	}

	// team.await blocks until all three are done.
	var wg sync.WaitGroup
	var awaitOut map[string]any
	var awaitErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		awaitOut, awaitErr = f.tool(t, "team.await", root.Id, map[string]any{"task_ids": ids, "timeout": 10})
	}()
	f.finish(t, ids[0], stSucceeded, "finding 1")
	taskstest.Eventually(t, time.Second, func() bool { return f.dispatched(t, ids[2]) })
	f.finish(t, ids[1], stSucceeded, "finding 2")
	f.finish(t, ids[2], stFailed, "no sources")
	wg.Wait()
	if awaitErr != nil {
		t.Fatal(awaitErr)
	}
	doneList := awaitOut["done"].([]any)
	if len(doneList) != 3 || awaitOut["timed_out"] != false {
		t.Fatalf("await = %v", awaitOut)
	}
	if s := doneList[0].(map[string]any)["summary"]; s != "finding 1" {
		t.Fatalf("summary = %v", s)
	}

	// The root's answer ends the run; unfinished members are cancelled.
	extra, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "check"})
	if err != nil {
		t.Fatal(err)
	}
	f.finish(t, root.Id, stSucceeded, "final report")
	wantState(t, f.get(t, root.Id), stSucceeded)
	wantState(t, f.get(t, extra["task_id"].(string)), stCancelled)

	run, err := f.e.TeamRun(context.Background(), root.Id)
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalTasks != 5 || run.Team.GetId() != "squad" || run.StoppedReason == "" {
		t.Fatalf("run = %d tasks, team %v, stopped %q", run.TotalTasks, run.Team.GetId(), run.StoppedReason)
	}
	kinds := map[nexusv1.TeamEventKind]int{}
	for _, ev := range f.env.Bus.Events() {
		if te := ev.GetTeam(); te != nil {
			kinds[te.Kind]++
			if ev.TaskId != root.Id || ev.RootTaskId != root.Id {
				t.Fatalf("team event on %s", ev.TaskId)
			}
		}
	}
	for _, k := range []nexusv1.TeamEventKind{nexusv1.TeamEventKind_TEAM_EVENT_KIND_STARTED, nexusv1.TeamEventKind_TEAM_EVENT_KIND_DELEGATED,
		nexusv1.TeamEventKind_TEAM_EVENT_KIND_AWAITING, nexusv1.TeamEventKind_TEAM_EVENT_KIND_MEMBER_FINISHED, nexusv1.TeamEventKind_TEAM_EVENT_KIND_FINISHED} {
		if kinds[k] == 0 {
			t.Errorf("no %s event (%v)", k, kinds)
		}
	}
	// Child events carry the root id.
	for _, ev := range f.env.Bus.Events() {
		if ev.TaskId == ids[0] && ev.RootTaskId != root.Id {
			t.Fatalf("child event root = %q", ev.RootTaskId)
		}
	}
}

func TestTeamDelegateWaitAndErrors(t *testing.T) {
	f := teamFixture(t, squad(nil), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	if _, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "ghost", "task": "x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown role: %v", err)
	}
	if _, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing task: %v", err)
	}
	go func() {
		taskstest.Eventually(t, time.Second, func() bool {
			k := f.children(t, root.Id)
			return len(k) == 1 && f.dispatched(t, k[0].Id)
		})
		f.finish(t, f.children(t, root.Id)[0].Id, stSucceeded, "checked")
	}()
	out, err := f.tool(t, "team.delegate", root.Id, map[string]any{"agent": "checker", "task": "check it", "wait": true, "timeout": 5})
	if err != nil {
		t.Fatal(err)
	}
	if d := out["done"].([]any); len(d) != 1 || d[0].(map[string]any)["summary"] != "checked" {
		t.Fatalf("wait out = %v", out)
	}
	// Await timeout returns pending tasks.
	c, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "slow"})
	out, err = f.tool(t, "team.await", root.Id, map[string]any{"task_ids": []string{c["task_id"].(string)}, "timeout": 1})
	if err != nil {
		t.Fatal(err)
	}
	if out["timed_out"] != true || len(out["pending"].([]any)) != 1 {
		t.Fatalf("await timeout = %v", out)
	}
}

func TestTeamToolScoping(t *testing.T) {
	f := teamFixture(t, squad(nil), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	out, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "q"})
	child := out["task_id"].(string)
	if _, err := f.tool(t, "team.delegate", child, map[string]any{"role": "checker", "task": "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("member delegating: %v", err)
	}
	plain := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "helper"})
	if _, err := f.tool(t, "team.status", plain.Id, map[string]any{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("non-team task: %v", err)
	}
	// A stale run cannot act.
	tl, _ := f.env.Host.Tools().Get("team.status")
	if _, err := tl.Invoke(context.Background(), kernel.ToolCall{TaskID: root.Id, RunID: "run_stale"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("stale run: %v", err)
	}
	// Members cannot cancel; the orchestrator cannot cancel outside its tree.
	if _, err := f.tool(t, "team.cancel", root.Id, map[string]any{"task_id": plain.Id}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cancel outside: %v", err)
	}
	if _, err := f.tool(t, "team.cancel", root.Id, map[string]any{"task_id": child}); err != nil {
		t.Fatal(err)
	}
	wantState(t, f.get(t, child), stCancelled)
	if _, err := f.tool(t, "team.status", root.Id, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamLimits(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxTasks: 3, MaxDepth: 1, MaxParallel: 5}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	for i := 0; i < 2; i++ {
		if _, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "c"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "c"})
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "3 tasks") {
		t.Fatalf("max_tasks: %v", err)
	}
	// Depth: members (depth 1) cannot create sub-tasks (max_depth 1).
	child := f.children(t, root.Id)[0]
	spawn, _ := f.env.Host.Tools().Get("spawn_agent")
	_, err = spawn.Invoke(context.Background(), kernel.ToolCall{TaskID: child.Id, Input: json.RawMessage(`{"prompt":"sub","agent_id":"helper"}`)})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("max_depth: %v", err)
	}
}

func TestTeamParallelSlots(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxParallel: 1}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	a, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	b, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "b"})
	if b["phase"] != phaseWaitingSlot {
		t.Fatalf("second task should wait: %v", b)
	}
	f.finish(t, a["task_id"].(string), stSucceeded, "ok")
	wantState(t, f.get(t, b["task_id"].(string)), stAssigned)
}

func TestTeamBudgetStopsTree(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 100}}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	c1, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	c2, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "b"})
	// Per-run cost limits are clamped to what is left of the tree budget.
	if mc := f.lastAssignment(t, c1["task_id"].(string)).Agent.GetLimits().GetMaxCost(); mc.GetMinorUnits() != 100 {
		t.Fatalf("clamped max_cost = %v", mc)
	}
	f.costEvent(t, root.Id, 30)
	f.costEvent(t, c1["task_id"].(string), 40)
	if tc := f.get(t, root.Id).TreeCost; tc.GetAmount().GetMinorUnits() != 70 {
		t.Fatalf("tree cost = %v", tc)
	}
	wantState(t, f.get(t, root.Id), stRunning)
	f.costEvent(t, c2["task_id"].(string), 40)
	got := f.get(t, root.Id)
	wantState(t, got, stFailed)
	if !strings.Contains(got.Summary, "budget exceeded") {
		t.Fatalf("summary = %q", got.Summary)
	}
	wantState(t, f.get(t, c1["task_id"].(string)), stCancelled)
	wantState(t, f.get(t, c2["task_id"].(string)), stCancelled)
	// Nothing new can start in a stopped tree.
	run, _ := f.e.TeamRun(context.Background(), root.Id)
	if !strings.Contains(run.StoppedReason, "budget") {
		t.Fatalf("stopped = %q", run.StoppedReason)
	}
}

func TestTeamMaxRunsAndTimeout(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxRuns: 2, TimeoutSeconds: 1}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	c1, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "a"})
	wantState(t, f.get(t, c1["task_id"].(string)), stAssigned)
	c2, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "b"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.get(t, c2["task_id"].(string))
	wantState(t, got, stFailed)
	if !strings.Contains(got.Summary, "2 runs") {
		t.Fatalf("summary = %q", got.Summary)
	}
	if ms := f.lastAssignment(t, c1["task_id"].(string)).Agent.GetLimits().GetMaxRuntimeSeconds(); ms < 1 || ms > 1 {
		t.Fatalf("runtime clamp = %d", ms)
	}
	taskstest.Eventually(t, 3*time.Second, func() bool { return f.get(t, root.Id).State == stFailed })
	if s := f.get(t, root.Id).Summary; !strings.Contains(s, "timed out") {
		t.Fatalf("summary = %q", s)
	}
	wantState(t, f.get(t, c1["task_id"].(string)), stCancelled)
}

func TestTeamCancelCascade(t *testing.T) {
	f := teamFixture(t, squad(nil), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	c1, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	f.runEvent(t, f.get(t, c1["task_id"].(string)), output("working"))
	if err := f.e.CancelTask(context.Background(), root.Id); err != nil {
		t.Fatal(err)
	}
	wantState(t, f.get(t, root.Id), stCancelled)
	wantState(t, f.get(t, c1["task_id"].(string)), stCancelled)
	runs := map[string]bool{}
	for _, c := range f.disp.CancelsSnapshot() {
		runs[c.RunId] = true
	}
	if !runs[f.get(t, root.Id).CurrentRunId] || !runs[f.get(t, c1["task_id"].(string)).CurrentRunId] {
		t.Fatalf("cancels = %v", f.disp.CancelsSnapshot())
	}
}

func TestBlackboardAndMessages(t *testing.T) {
	f := teamFixture(t, squad(&nexusv1.TeamLimits{MaxParallel: 1}), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	f.runEvent(t, f.get(t, root.Id), output("x"))
	c1, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	c2, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "b"}) // waits for a slot
	id1, id2 := c1["task_id"].(string), c2["task_id"].(string)
	f.runEvent(t, f.get(t, id1), output("working"))

	if _, err := f.tool(t, "team.blackboard.write", root.Id, map[string]any{"key": "outline", "value": map[string]any{"parts": 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tool(t, "team.blackboard.append", id1, map[string]any{"key": "facts", "value": "f1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tool(t, "team.blackboard.append", id1, map[string]any{"key": "facts", "value": "f2"}); err != nil {
		t.Fatal(err)
	}
	out, err := f.tool(t, "team.blackboard.read", id1, map[string]any{"key": "facts"})
	if err != nil {
		t.Fatal(err)
	}
	if e := out["entries"].([]any)[0].(map[string]any); e["value"] != "f1\nf2" || e["version"].(float64) != 2 || e["by"] != "researcher" {
		t.Fatalf("facts = %v", e)
	}
	// Message to a running member: delivered live as run input.
	m, err := f.tool(t, "team.message", root.Id, map[string]any{"to": "researcher", "text": "focus on costs"})
	if err != nil {
		t.Fatal(err)
	}
	if d := m["delivered_to"].([]any); len(d) != 1 || d[0] != id1 {
		t.Fatalf("delivered = %v", m)
	}
	in := f.disp.InputsSnapshot()
	if len(in) != 1 || !strings.Contains(in[0].Text, "[Team message from orchestrator]: focus on costs") {
		t.Fatalf("inputs = %v", in)
	}
	// Message to a queued member: in its context when it starts.
	if _, err := f.tool(t, "team.message", id1, map[string]any{"to": "checker", "text": "check the numbers"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tool(t, "team.message", root.Id, map[string]any{"to": "nobody", "text": "x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown target: %v", err)
	}
	f.finish(t, id1, stSucceeded, "done")
	a2 := f.lastAssignment(t, id2)
	msgs := a2.Context.Fields["team_messages"].GetListValue().GetValues()
	if len(msgs) != 1 || msgs[0].GetStructValue().Fields["text"].GetStringValue() != "check the numbers" {
		t.Fatalf("team_messages = %v", a2.Context.Fields["team_messages"])
	}
	if bb := a2.Context.Fields["blackboard"].GetStructValue(); bb.Fields["outline"] == nil {
		t.Fatalf("blackboard snapshot = %v", bb)
	}
	run, _ := f.e.TeamRun(context.Background(), root.Id)
	if len(run.Blackboard) != 2 || len(run.Messages) != 2 {
		t.Fatalf("run blackboard %d messages %d", len(run.Blackboard), len(run.Messages))
	}
}

func TestTeamReport(t *testing.T) {
	f := teamFixture(t, squad(nil), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	c, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	id := c["task_id"].(string)
	if _, err := f.tool(t, "team.report", id, map[string]any{"result": map[string]any{"price": 120}}); err != nil {
		t.Fatal(err)
	}
	f.finish(t, id, stSucceeded, "found it")
	got := f.get(t, id)
	if got.Result.Fields["price"].GetNumberValue() != 120 {
		t.Fatalf("result = %v", got.Result)
	}
}

func TestSpawnRestrictions(t *testing.T) {
	f := teamFixture(t, squad(nil), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	base := map[string]any{"role": "translator", "instructions": "Translate.", "task": "translate this"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	if _, err := f.tool(t, "team.spawn", root.Id, with("tools", []string{"node.exec"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("tool escalation: %v", err)
	}
	if _, err := f.tool(t, "team.spawn", root.Id, with("runtime", "shell")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("runtime escalation: %v", err)
	}
	if _, err := f.tool(t, "team.spawn", root.Id, with("role", "researcher")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("member role: %v", err)
	}
	out, err := f.tool(t, "team.spawn", root.Id, with("tools", []string{"web.search"}))
	if err != nil {
		t.Fatal(err)
	}
	id := out["task_id"].(string)
	got := f.get(t, id)
	if got.AgentId != "eph:translator" || got.Role != "translator" {
		t.Fatalf("spawned = %v", got)
	}
	a := f.lastAssignment(t, id)
	if a.Instructions == "" || !strings.HasPrefix(a.Instructions, "Translate.") {
		t.Fatalf("instructions = %q", a.Instructions)
	}
	if !hasTool(a, "web.search") || hasTool(a, "memory.search") || hasTool(a, "team.delegate") || !hasTool(a, "team.report") {
		t.Fatalf("spawned tools = %v", a.Agent.Tools)
	}
	if a.Agent.Policy["write_external"] != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC || a.Agent.Runtime != "echo" {
		t.Fatalf("spawned agent = %v", a.Agent)
	}
	// Ephemeral agents can be reviewed / repaired like others: they survive re-assignment.
	f.finish(t, id, stSucceeded, "done")
	wantState(t, f.get(t, id), stSucceeded)
}

func TestApprovalsCarryTeamMetadata(t *testing.T) {
	broker := &taskstest.Broker{}
	f := newFixture(t, func(f *fixture) {
		for _, a := range squadAgents() {
			f.agents.Add(a, "")
		}
		f.env.Host.Provide(kernel.SvcTeams, squad(nil))
		f.env.Host.Provide(kernel.SvcApprovals, broker)
	})
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	c, _ := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "checker", "task": "a"})
	id := c["task_id"].(string)
	f.runEvent(t, f.get(t, id), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{Title: "Pay"}}})
	if len(broker.Requested) != 1 {
		t.Fatalf("approvals = %d", len(broker.Requested))
	}
	a := broker.Requested[0]
	if a.RootTaskId != root.Id || a.TeamId != "squad" || a.Role != "checker" || !a.Bundle {
		t.Fatalf("approval = %v", a)
	}
	wantState(t, f.get(t, id), stWaitingApproval)
}

func TestPipelineStrategyWithoutOrchestrator(t *testing.T) {
	tm := taskstest.Team("pipe", "", nexusv1.TeamStrategy_TEAM_STRATEGY_PIPELINE,
		taskstest.Member("researcher", "draft", 1), taskstest.Member("checker", "polish", 1))
	f := teamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "write a haiku", TeamId: "pipe"})
	if root.Phase != phaseStrategy {
		t.Fatalf("root phase = %q", root.Phase)
	}
	var first, second *nexusv1.Task
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, root.Id)
		if len(k) == 1 && f.dispatched(t, k[0].Id) {
			first = k[0]
			return true
		}
		return false
	})
	f.finish(t, first.Id, stSucceeded, "draft haiku")
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, root.Id)
		if len(k) == 2 && f.dispatched(t, k[1].Id) {
			second = k[1]
			return true
		}
		return false
	})
	if p := f.lastAssignment(t, second.Id).Prompt; !strings.Contains(p, "draft haiku") || !strings.Contains(p, "previous stage (draft)") {
		t.Fatalf("stage 2 prompt = %q", p)
	}
	f.finish(t, second.Id, stSucceeded, "polished haiku")
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.get(t, root.Id).State == stSucceeded })
	if got := f.get(t, root.Id); got.Summary != "polished haiku" || got.Result.Fields["strategy"].GetStringValue() != "pipeline" {
		t.Fatalf("root = %q %v", got.Summary, got.Result)
	}
}

func TestParallelStrategyHandsResultsToOrchestrator(t *testing.T) {
	tm := taskstest.Team("par", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_PARALLEL, taskstest.Member("researcher", "researcher", 2))
	f := teamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "ideas", TeamId: "par"})
	taskstest.Eventually(t, 2*time.Second, func() bool { return len(f.children(t, root.Id)) == 2 })
	for i, k := range f.children(t, root.Id) {
		taskstest.Eventually(t, time.Second, func() bool { return f.dispatched(t, k.Id) })
		f.finish(t, k.Id, stSucceeded, []string{"idea A", "idea B"}[i])
	}
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.dispatched(t, root.Id) })
	a := f.lastAssignment(t, root.Id)
	st := a.Context.Fields["strategy"].GetStructValue()
	if st.Fields["name"].GetStringValue() != "parallel" || len(st.Fields["results"].GetListValue().GetValues()) != 2 {
		t.Fatalf("strategy context = %v", st)
	}
	if !strings.Contains(a.Instructions, "ALREADY run the parallel strategy") {
		t.Fatalf("instructions = %q", a.Instructions)
	}
}

func TestDebateStrategy(t *testing.T) {
	tm := taskstest.Team("panel", "", nexusv1.TeamStrategy_TEAM_STRATEGY_DEBATE,
		taskstest.Member("researcher", "pro", 1), taskstest.Member("checker", "con", 1), taskstest.Member("helper", "judge", 1))
	tm.DebateRounds = 2
	f := teamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "tabs or spaces?", TeamId: "panel"})
	finishRound := func(n int, prefix string) {
		taskstest.Eventually(t, 2*time.Second, func() bool {
			c := 0
			for _, k := range f.children(t, root.Id) {
				if f.dispatched(t, k.Id) {
					c++
				}
			}
			return c == n
		})
		for _, k := range f.children(t, root.Id) {
			if f.dispatched(t, k.Id) {
				f.finish(t, k.Id, stSucceeded, prefix+" "+k.Role)
			}
		}
	}
	finishRound(2, "round1")
	finishRound(2, "round2")
	var judge *nexusv1.Task
	taskstest.Eventually(t, 2*time.Second, func() bool {
		for _, k := range f.children(t, root.Id) {
			if k.Role == "judge" && f.dispatched(t, k.Id) {
				judge = k
				return true
			}
		}
		return false
	})
	p := f.lastAssignment(t, judge.Id).Prompt
	if !strings.Contains(p, "round2 pro") || !strings.Contains(p, "round2 con") {
		t.Fatalf("judge prompt = %q", p)
	}
	// Round 2 debaters saw each other's round-1 positions.
	var r2 string
	for _, k := range f.children(t, root.Id) {
		if k.Role == "pro" && strings.Contains(k.Prompt, "round 2 of 2") {
			r2 = k.Prompt
		}
	}
	if !strings.Contains(r2, "round1 con") {
		t.Fatalf("round 2 prompt = %q", r2)
	}
	f.finish(t, judge.Id, stSucceeded, "spaces win")
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.get(t, root.Id).State == stSucceeded })
	if s := f.get(t, root.Id).Summary; s != "spaces win" {
		t.Fatalf("verdict = %q", s)
	}
}

func TestRunStrategyTool(t *testing.T) {
	tm := taskstest.Team("auto", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_AUTO, taskstest.Member("researcher", "researcher", 1))
	f := teamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "auto"})
	if !strings.Contains(f.lastAssignment(t, root.Id).Instructions, "team.run_strategy") {
		t.Fatal("auto brief does not mention team.run_strategy")
	}
	go func() {
		taskstest.Eventually(t, 2*time.Second, func() bool {
			k := f.children(t, root.Id)
			return len(k) == 1 && f.dispatched(t, k[0].Id)
		})
		f.finish(t, f.children(t, root.Id)[0].Id, stSucceeded, "only result")
	}()
	out, err := f.tool(t, "team.run_strategy", root.Id, map[string]any{"strategy": "parallel", "task": "sub", "timeout": 5})
	if err != nil {
		t.Fatal(err)
	}
	if out["final"] == "" || len(out["results"].([]any)) != 1 || out["error"] != nil {
		t.Fatalf("run_strategy = %v", out)
	}
}

func TestRoutingToTeam(t *testing.T) {
	rt := &taskstest.Router{Result: &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, TeamId: "squad", Confidence: 0.9}}
	f := newFixture(t, func(f *fixture) {
		for _, a := range squadAgents() {
			f.agents.Add(a, "")
		}
		f.env.Host.Provide(kernel.SvcTeams, squad(nil))
		f.env.Host.Provide(kernel.SvcRouter, rt)
	})
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "deep dive into heat pumps"})
	wantState(t, task, stAssigned)
	if task.TeamId != "squad" || task.AgentId != "lead" || task.Role != RoleOrchestrator {
		t.Fatalf("routed task = %v", task)
	}
	// Choosing "team:<id>" when asked which agent to use.
	rt.Result = &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, Confidence: 0.1}
	q := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "something"})
	wantState(t, q, stNeedsInput)
	if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: q.Id, Choice: "team:squad"}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, q.Id); got.TeamId != "squad" || got.State != stAssigned {
		t.Fatalf("chosen team = %v", got)
	}
	// agent_id "team:<id>" works like team_id; both together are rejected.
	if _, err := f.e.CreateTask(context.Background(), &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "lead", TeamId: "squad"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("agent+team: %v", err)
	}
	if _, err := f.e.CreateTask(context.Background(), &nexusv1.CreateTaskRequest{Prompt: "x", TeamId: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown team: %v", err)
	}
}

func TestPlainTreeBudgetRollup(t *testing.T) {
	boss := agentWith("boss", func(a *nexusv1.Agent) {
		a.Tools = []string{"spawn_agent"}
		a.Limits = &nexusv1.AgentLimits{MaxTreeCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 50}, MaxParallel: 1}
	})
	f := teamFixture(t, nil, boss)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "plan", AgentId: "boss"})
	spawn, _ := f.env.Host.Tools().Get("spawn_agent")
	var ids []string
	for i := 0; i < 2; i++ {
		res, err := spawn.Invoke(context.Background(), kernel.ToolCall{TaskID: root.Id, Input: json.RawMessage(`{"prompt":"sub","agent_id":"helper"}`)})
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(res.Output, &out)
		ids = append(ids, out["task_id"].(string))
	}
	if f.get(t, ids[1]).Phase != phaseWaitingSlot {
		t.Fatalf("max_parallel 1: second child = %v", f.get(t, ids[1]))
	}
	f.costEvent(t, ids[0], 20)
	if got := f.get(t, root.Id).TreeCost.GetAmount().GetMinorUnits(); got != 20 {
		t.Fatalf("tree cost = %d", got)
	}
	f.costEvent(t, ids[0], 40)
	wantState(t, f.get(t, root.Id), stFailed)
	wantState(t, f.get(t, ids[1]), stCancelled)
}

func TestTeamService(t *testing.T) {
	teams := squad(nil)
	f := teamFixture(t, teams, squadAgents()...)
	srv := httptest.NewServer(f.env.Host.Mux)
	defer srv.Close()
	cl := nexusv1connect.NewTeamServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	lr, err := cl.ListTeams(ctx, connect.NewRequest(&nexusv1.ListTeamsRequest{}))
	if err != nil || len(lr.Msg.Teams) != 1 {
		t.Fatalf("ListTeams = %v, %v", lr, err)
	}
	gr, err := cl.GetTeam(ctx, connect.NewRequest(&nexusv1.GetTeamRequest{Id: "squad"}))
	if err != nil || gr.Msg.Team.Id != "squad" || !strings.Contains(gr.Msg.Markdown, "Mission") {
		t.Fatalf("GetTeam = %v, %v", gr, err)
	}
	if _, err := cl.GetTeam(ctx, connect.NewRequest(&nexusv1.GetTeamRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetTeam unknown: %v", err)
	}
	if _, err := cl.UpsertTeam(ctx, connect.NewRequest(&nexusv1.UpsertTeamRequest{Markdown: "---\nid: x\n---\n", ValidateOnly: true})); err != nil || len(teams.Upserted) != 1 {
		t.Fatalf("UpsertTeam: %v", err)
	}

	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	f.runEvent(t, f.get(t, root.Id), output("x"))
	_, _ = f.tool(t, "team.delegate", root.Id, map[string]any{"role": "researcher", "task": "a"})
	mr, err := cl.SendTeamMessage(ctx, connect.NewRequest(&nexusv1.SendTeamMessageRequest{RootTaskId: root.Id, Text: "hurry up"}))
	if err != nil || mr.Msg.Message.FromRole != "user" || len(mr.Msg.Message.DeliveredTo) != 1 {
		t.Fatalf("SendTeamMessage = %v, %v", mr, err)
	}
	rr, err := cl.GetTeamRun(ctx, connect.NewRequest(&nexusv1.GetTeamRunRequest{RootTaskId: root.Id}))
	if err != nil {
		t.Fatal(err)
	}
	run := rr.Msg.Run
	if run.Root.Id != root.Id || run.TotalTasks != 2 || run.ActiveTasks != 1 || run.Limits.GetMaxParallel() != 4 || len(run.Messages) != 1 || run.Deadline == nil {
		t.Fatalf("run = %v", run)
	}
	if run.Tasks[1].MemberRole != "researcher" || run.Tasks[1].Task.Depth != 1 {
		t.Fatalf("run task = %v", run.Tasks[1])
	}

	// ListTasks filters by team and tree.
	tl, ts, err := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{TeamId: "squad", RootsOnly: true})
	if err != nil || len(tl) != 1 || ts != "" {
		t.Fatalf("ListTasks team = %v, %v", tl, err)
	}
	tl, _, _ = f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{RootId: root.Id})
	if len(tl) != 2 {
		t.Fatalf("ListTasks root = %d", len(tl))
	}
}

// ---------------------------------------------------------------- fakes

type fakeVault struct {
	root  string
	notes map[string]string // id/path/title → body
}

func (v *fakeVault) Read(_ context.Context, key string) (*nexusv1.Note, error) {
	body, ok := v.notes[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &nexusv1.Note{Ref: &nexusv1.NoteRef{Path: key, Title: key}, Body: body}, nil
}

func (v *fakeVault) Write(_ context.Context, path, markdown, _ string) (*nexusv1.Note, error) {
	v.notes[path] = markdown
	return &nexusv1.Note{Ref: &nexusv1.NoteRef{Path: path}}, nil
}

func (v *fakeVault) Search(_ context.Context, q string, _ []string, _ int) ([]*nexusv1.SearchHit, error) {
	var out []*nexusv1.SearchHit
	for k, b := range v.notes {
		if strings.Contains(strings.ToLower(b), "heat") && strings.Contains(q, "heat") {
			out = append(out, &nexusv1.SearchHit{Note: &nexusv1.NoteRef{Path: k}})
		}
	}
	return out, nil
}

func (v *fakeVault) Graph(context.Context, *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error) {
	return nil, errors.New("no graph")
}

func (v *fakeVault) Capture(context.Context, *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error) {
	return nil, errors.New("no capture")
}

func (v *fakeVault) Root() string { return v.root }
