package tasks

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// restart simulates a hub restart: the engine is closed (strategy drivers,
// timers and blocked team.await calls end) and a new Engine is created on
// the same database, bus and services. It does not call Resume.
func (f *fixture) restart(t *testing.T) {
	t.Helper()
	f.e.Close()
	// Like a real restart, start from fresh connections: a query cancelled
	// by Close may leave a pooled connection on a stale read snapshot.
	// (Needs a file database, see durable.)
	db := f.env.Store.DB()
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(2)
	old := f.env.Host
	h := kerneltest.New(t)
	for _, name := range []string{kernel.SvcStore, kernel.SvcEventBus, kernel.SvcDispatcher, kernel.SvcAgents, kernel.SvcTeams,
		kernel.SvcRouter, kernel.SvcApprovals, kernel.SvcSkills, kernel.SvcVault} {
		if v, ok := old.Lookup(name); ok {
			h.Provide(name, v)
		}
	}
	f.env.Host = h
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	f.e = m.Engine
	t.Cleanup(m.Engine.Close)
}

// durable gives the fixture a file database, for tests that restart.
func durable(t *testing.T) opt {
	return func(f *fixture) {
		f.env.Store = taskstest.NewFileStore(t)
		f.env.Host.Provide(kernel.SvcStore, f.env.Store)
	}
}

// durableTeamFixture is teamFixture with a file database.
func durableTeamFixture(t *testing.T, teams *taskstest.Teams, extra ...*nexusv1.Agent) *fixture {
	t.Helper()
	return newFixture(t, durable(t), func(f *fixture) {
		for _, a := range extra {
			f.agents.Add(a, "Instructions for "+a.Id)
		}
		if teams != nil {
			f.env.Host.Provide(kernel.SvcTeams, teams)
		}
	})
}

func notices(f *fixture, taskID string) []*nexusv1.Event {
	return f.env.Bus.Filter(func(e *nexusv1.Event) bool {
		return e.TaskId == taskID && e.GetNotice().GetKind() == nexusv1.NoticeKind_NOTICE_KIND_RESULT
	})
}

func spawn(t *testing.T, f *fixture, parentID, agentID, prompt string) string {
	t.Helper()
	out, err := f.tool(t, "spawn_agent", parentID, map[string]any{"prompt": prompt, "agent_id": agentID})
	if err != nil {
		t.Fatalf("spawn_agent: %v", err)
	}
	return out["task_id"].(string)
}

// ---------------------------------------------------------------- 6. parent waits for spawn_agent children

func TestParentWaitsForSpawnedChildrenThenResumes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "compare two laptops", AgentId: "smart"})
	f.runEvent(t, f.get(t, root.Id), output("splitting the work"))
	a := spawn(t, f, root.Id, "helper", "specs of laptop A")
	b := spawn(t, f, root.Id, "helper", "specs of laptop B")
	firstRun := f.get(t, root.Id).CurrentRunId

	// The run ends while both children still run: the task waits.
	f.finish(t, root.Id, stSucceeded, "I asked two helpers; waiting for them.")
	p := f.get(t, root.Id)
	if p.State != stRunning || p.Phase != phaseWaitingChildren {
		t.Fatalf("parent = %s/%s, want running/waiting_children", stateName(p.State), p.Phase)
	}
	if len(notices(f, root.Id)) != 0 {
		t.Fatal("a result notice was sent while sub-tasks still run")
	}
	if r, _ := f.e.loadRun(ctx, firstRun); r.state != runSucceeded {
		t.Fatalf("first run state = %s", r.state)
	}

	f.finish(t, a, stSucceeded, "A: 16 GB, 1.2 kg")
	if p := f.get(t, root.Id); p.Phase != phaseWaitingChildren || p.CurrentRunId != firstRun {
		t.Fatalf("parent resumed before every child ended: %s/%s", stateName(p.State), p.Phase)
	}
	f.finish(t, b, stFailed, "B: page not found")

	// The parent agent is resumed with the children's results.
	p = f.get(t, root.Id)
	if p.State != stAssigned || p.CurrentRunId == firstRun || p.Phase != "" {
		t.Fatalf("parent after children = %s/%s run %s", stateName(p.State), p.Phase, p.CurrentRunId)
	}
	as := f.lastAssignment(t, root.Id)
	c := as.Context.AsMap()
	res, _ := c["sub_task_results"].([]any)
	if len(res) != 2 {
		t.Fatalf("sub_task_results = %v", c["sub_task_results"])
	}
	r0, r1 := res[0].(map[string]any), res[1].(map[string]any)
	if r0["task_id"] != a || r0["state"] != "succeeded" || r0["summary"] != "A: 16 GB, 1.2 kg" || r1["state"] != "failed" {
		t.Fatalf("results = %v", res)
	}
	if !strings.Contains(c["escalation_hint"].(string), "sub_task_results") {
		t.Fatalf("hint = %v", c["escalation_hint"])
	}
	if prev := c["previous_attempt"].(map[string]any); prev["run_id"] != firstRun {
		t.Fatalf("previous_attempt = %v", prev)
	}

	// The resumed run's answer is final.
	f.finish(t, root.Id, stSucceeded, "A is lighter; B's specs could not be found.")
	p = f.get(t, root.Id)
	wantState(t, p, stSucceeded)
	if p.Summary != "A is lighter; B's specs could not be found." {
		t.Fatalf("summary = %q", p.Summary)
	}
	if n := len(notices(f, root.Id)); n != 1 {
		t.Fatalf("result notices = %d, want 1", n)
	}
	// No new sub-tasks: nothing more to wait for, results were handed once.
	x, _ := f.e.loadExt(ctx, root.Id)
	if x.ChildResumes != 1 || len(x.ChildrenReported) != 2 {
		t.Fatalf("ext = %+v", x)
	}
}

func TestParentWithFinishedChildrenEndsAtOnce(t *testing.T) {
	f := newFixture(t)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "smart"})
	kid := spawn(t, f, root.Id, "helper", "sub")
	f.finish(t, kid, stSucceeded, "sub done")
	f.finish(t, root.Id, stSucceeded, "final")
	p := f.get(t, root.Id)
	wantState(t, p, stSucceeded)
	if p.Summary != "final" || len(f.disp.AssignmentsFor(root.Id)) != 1 {
		t.Fatalf("parent = %q, %d runs", p.Summary, len(f.disp.AssignmentsFor(root.Id)))
	}
}

func TestParentWaitingFallsBackWhenNoRunIsLeft(t *testing.T) {
	boss := agentWith("boss", func(a *nexusv1.Agent) {
		a.Tools = []string{"spawn_agent"}
		a.Limits = &nexusv1.AgentLimits{MaxRuns: 2} // the root's run + the child's run
	})
	f := teamFixture(t, nil, boss)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "plan a trip", AgentId: "boss"})
	kid := spawn(t, f, root.Id, "helper", "find flights")
	f.finish(t, root.Id, stSucceeded, "Draft plan.")
	if p := f.get(t, root.Id); p.Phase != phaseWaitingChildren {
		t.Fatalf("phase = %q", p.Phase)
	}
	f.finish(t, kid, stSucceeded, "TAP 7:40, €120")
	p := f.get(t, root.Id)
	wantState(t, p, stSucceeded)
	if !strings.Contains(p.Summary, "Draft plan.") || !strings.Contains(p.Summary, "## Sub-task results") || !strings.Contains(p.Summary, "TAP 7:40, €120") {
		t.Fatalf("summary = %q", p.Summary)
	}
	if subs := p.Result.GetFields()["sub_tasks"].GetListValue().GetValues(); len(subs) != 1 {
		t.Fatalf("result = %v", p.Result)
	}
	if len(f.disp.AssignmentsFor(root.Id)) != 1 {
		t.Fatal("the parent was resumed beyond its run limit")
	}
	if len(notices(f, root.Id)) != 1 {
		t.Fatal("want one result notice")
	}
}

func TestCancelWaitingParentCancelsChildren(t *testing.T) {
	f := newFixture(t)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "smart"})
	kid := spawn(t, f, root.Id, "helper", "sub")
	f.finish(t, root.Id, stSucceeded, "waiting")
	nCancels := len(f.disp.CancelsSnapshot())
	if err := f.e.CancelTask(context.Background(), root.Id); err != nil {
		t.Fatal(err)
	}
	wantState(t, f.get(t, root.Id), stCancelled)
	wantState(t, f.get(t, kid), stCancelled)
	// Only the child's live run is cancelled on the node (the parent's ended).
	if got := len(f.disp.CancelsSnapshot()) - nCancels; got != 1 {
		t.Fatalf("node cancels = %d", got)
	}
}

func TestWaitingParentFreesItsSlot(t *testing.T) {
	boss := agentWith("boss", func(a *nexusv1.Agent) {
		a.Tools = []string{"spawn_agent"}
		a.Limits = &nexusv1.AgentLimits{MaxParallel: 1}
	})
	f := teamFixture(t, nil, boss)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "boss"})
	mid := spawn(t, f, root.Id, "boss", "middle")
	if !f.dispatched(t, mid) {
		t.Fatal("middle task not started")
	}
	leaf := spawn(t, f, mid, "helper", "leaf")
	if f.get(t, leaf).Phase != phaseWaitingSlot {
		t.Fatalf("leaf phase = %q (max_parallel 1, middle runs)", f.get(t, leaf).Phase)
	}
	// The middle task's run ends: it waits for its child and frees the slot.
	f.finish(t, mid, stSucceeded, "delegated")
	if p := f.get(t, mid).Phase; p != phaseWaitingChildren {
		t.Fatalf("middle phase = %q", p)
	}
	if !f.dispatched(t, leaf) {
		t.Fatal("leaf did not start once its parent waited")
	}
}

func TestWaitingChildrenSurvivesRestart(t *testing.T) {
	f := newFixture(t, durable(t))
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "smart"})
	kid := spawn(t, f, root.Id, "helper", "sub")
	f.finish(t, root.Id, stSucceeded, "waiting")
	// The child ends while the hub is down: its row is written without the
	// engine reacting (no onTerminal), as if the hub crashed right after.
	kr, err := f.e.loadTask(ctx, kid)
	if err != nil {
		t.Fatal(err)
	}
	kr.task.State, kr.task.Summary = stSucceeded, "sub result"
	if err := f.e.saveTask(ctx, kr); err != nil {
		t.Fatal(err)
	}
	if p := f.get(t, root.Id); p.Phase != phaseWaitingChildren {
		t.Fatalf("phase = %q", p.Phase)
	}
	runs := len(f.disp.AssignmentsFor(root.Id))
	f.restart(t)
	if err := f.e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.AssignmentsFor(root.Id)); got != runs+1 {
		t.Fatalf("parent runs = %d, want %d (resumed after restart)", got, runs+1)
	}
	if res := f.lastAssignment(t, root.Id).Context.AsMap()["sub_task_results"]; res == nil {
		t.Fatal("no sub_task_results after restart")
	}
	// Resume is idempotent.
	if err := f.e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(f.disp.AssignmentsFor(root.Id)); got != runs+1 {
		t.Fatalf("second Resume started another run (%d)", got)
	}
}

// ---------------------------------------------------------------- 7. chat tasks

func TestChatTasksSendNoResultNotice(t *testing.T) {
	f := newFixture(t)
	chatParams := mustStruct(t, map[string]any{"chat": true, "conversation_id": "conv-1"})
	chat := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "helper", Source: "chat", Params: chatParams})
	plain := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "helper", Source: "command"})
	// A chat-param task from another source is a chat task too.
	chat2 := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "helper", Source: "ios", Params: chatParams})

	// Questions and approvals still reach the user.
	f.runEvent(t, f.get(t, chat.Id), &nexusv1.Event{Body: &nexusv1.Event_Question{Question: &nexusv1.Question{QuestionId: "q1", Text: "Which one?"}}})
	if qs := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.TaskId == chat.Id && e.GetQuestion() != nil }); len(qs) != 1 {
		t.Fatalf("questions = %d", len(qs))
	}
	if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: chat.Id, QuestionId: "q1", Text: "the first"}); err != nil {
		t.Fatal(err)
	}
	f.finish(t, chat.Id, stSucceeded, "Hello!")
	f.finish(t, chat2.Id, stFailed, "oops")
	f.finish(t, plain.Id, stSucceeded, "Hello!")
	wantState(t, f.get(t, chat.Id), stSucceeded)
	if n := len(notices(f, chat.Id)) + len(notices(f, chat2.Id)); n != 0 {
		t.Fatalf("chat tasks produced %d result notices", n)
	}
	if n := len(notices(f, plain.Id)); n != 1 {
		t.Fatalf("plain task notices = %d, want 1", n)
	}
}

// ---------------------------------------------------------------- 5. routed runtime hint

func TestRoutedRuntimeHintPersistsForFallback(t *testing.T) {
	ctx := context.Background()
	f := newRTFixture(t, "claude-code", "codex", "ollama")
	f.agent("researcher", "auto", nexusv1.Tier_TIER_MEDIUM, "")
	params, _ := structpb.NewStruct(map[string]any{ParamRuntimeHint: "codex"})
	f.e.h.(*kerneltest.Host).Provide(kernel.SvcRouter, &taskstest.Router{Result: &nexusv1.Route{
		Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, AgentId: "researcher", Confidence: 0.9, Params: params}})
	task, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "research X"})
	if err != nil {
		t.Fatal(err)
	}
	if f.last().GetAgent().GetRuntime() != "codex" {
		t.Fatalf("hinted runtime not used: %s", f.last().GetAgent().GetRuntime())
	}
	r, _ := f.e.loadTask(ctx, task.Id)
	if r.req.GetParams().GetFields()[ParamRuntimeHint].GetStringValue() != "codex" {
		t.Fatalf("hint not persisted: %v", r.req.GetParams())
	}
	if _, ok := f.last().GetContext().AsMap()[ParamRuntimeHint]; ok {
		t.Fatal("the hint leaked into the run context")
	}
	send := func(ev *nexusv1.Event) {
		t.Helper()
		cur, _ := f.e.GetTask(ctx, task.Id)
		ev.RunId = cur.CurrentRunId
		if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: ev}); err != nil {
			t.Fatal(err)
		}
	}
	// The hinted runtime fails: every other runtime is still tried, in order.
	send(failedEvent(kernel.ErrorKindRateLimited, "Codex usage limit reached."))
	if got := f.last().GetAgent().GetRuntime(); got != "claude-code" {
		t.Fatalf("after codex: %s, want claude-code", got)
	}
	send(failedEvent(kernel.ErrorKindRateLimited, "Claude usage limit reached."))
	if got := f.last().GetAgent().GetRuntime(); got != "ollama" {
		t.Fatalf("after claude-code: %s, want ollama", got)
	}
	f.disp.mu.Lock()
	tried := append([]string(nil), f.disp.tried...)
	f.disp.mu.Unlock()
	if strings.Join(tried, ",") != "codex,claude-code,ollama" {
		t.Fatalf("tried = %v", tried)
	}
}

// ---------------------------------------------------------------- 1. team runs survive a hub restart

func TestPipelineDriverResumesAfterRestart(t *testing.T) {
	tm := taskstest.Team("pipe", "", nexusv1.TeamStrategy_TEAM_STRATEGY_PIPELINE,
		taskstest.Member("researcher", "draft", 1), taskstest.Member("checker", "polish", 1))
	f := durableTeamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "write a haiku", TeamId: "pipe"})
	var first string
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, root.Id)
		if len(k) == 1 && f.dispatched(t, k[0].Id) {
			first = k[0].Id
			return true
		}
		return false
	})

	f.restart(t)
	if err := f.e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	// Resuming twice (Start and a manual call) must not duplicate anything.
	if err := f.e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if k := f.children(t, root.Id); len(k) != 1 {
		t.Fatalf("children after restart = %d, want 1 (no duplicate stage)", len(k))
	}
	f.finish(t, first, stSucceeded, "draft haiku")
	var second string
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, root.Id)
		if len(k) == 2 && f.dispatched(t, k[1].Id) {
			second = k[1].Id
			return true
		}
		return false
	})
	f.finish(t, second, stSucceeded, "polished haiku")
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.get(t, root.Id).State == stSucceeded })
	if got := f.get(t, root.Id); got.Summary != "polished haiku" {
		t.Fatalf("root = %q", got.Summary)
	}
	if k := f.children(t, root.Id); len(k) != 2 {
		t.Fatalf("children = %d", len(k))
	}
}

func TestDebateDriverResumesFinishedStepsAfterRestart(t *testing.T) {
	tm := taskstest.Team("panel", "", nexusv1.TeamStrategy_TEAM_STRATEGY_DEBATE,
		taskstest.Member("researcher", "pro", 1), taskstest.Member("checker", "con", 1))
	tm.DebateRounds = 1
	f := durableTeamFixture(t, taskstest.NewTeams(tm), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "tabs or spaces?", TeamId: "panel"})
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, root.Id)
		return len(k) == 2 && f.dispatched(t, k[0].Id) && f.dispatched(t, k[1].Id)
	})
	kids := f.children(t, root.Id)
	f.finish(t, kids[0].Id, stSucceeded, "tabs")
	f.restart(t)
	// The second debater finishes while the hub is "down" (before Resume).
	f.finish(t, kids[1].Id, stSucceeded, "spaces")
	if err := f.e.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.get(t, root.Id).State == stSucceeded })
	if k := f.children(t, root.Id); len(k) != 2 {
		t.Fatalf("children = %d, want 2", len(k))
	}
	if s := f.get(t, root.Id).Summary; !strings.Contains(s, "tabs") || !strings.Contains(s, "spaces") {
		t.Fatalf("transcript = %q", s)
	}
}

func TestDeadlinesRearmedAfterRestart(t *testing.T) {
	f := durableTeamFixture(t, squad(&nexusv1.TeamLimits{TimeoutSeconds: 3600}), squadAgents()...)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", TeamId: "squad"})
	f.restart(t)
	f.e.resumeDeadlines(ctx)
	f.e.timerMu.Lock()
	armed := f.e.timers[root.Id] != nil
	f.e.timerMu.Unlock()
	if !armed {
		t.Fatal("deadline timer not re-armed")
	}

	// A deadline that passed while the hub was down stops the run.
	if _, err := f.e.db.ExecContext(ctx, `UPDATE task_trees SET deadline = ? WHERE root_id = ?`, time.Now().Add(-time.Minute).UnixMilli(), root.Id); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.e.resumeDeadlines(ctx)
	got := f.get(t, root.Id)
	wantState(t, got, stFailed)
	if !strings.Contains(got.Summary, "while the hub was down") {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestTeamAwaitInterruptedByRestart(t *testing.T) {
	f := durableTeamFixture(t, squad(nil), squadAgents()...)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "research", TeamId: "squad"})
	f.runEvent(t, f.get(t, root.Id), output("planning"))
	out, err := f.tool(t, "team.delegate_many", root.Id, map[string]any{"items": []any{
		map[string]any{"role": "researcher", "task": "q1"}, map[string]any{"role": "checker", "task": "q2"}}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range out["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["task_id"].(string))
	}
	f.finish(t, ids[0], stSucceeded, "finding 1")

	// team.await blocks on the second member; the hub stops.
	awaitTool, _ := f.env.Host.Tools().Get("team.await")
	cur := f.get(t, root.Id)
	var wg sync.WaitGroup
	var awaitErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, awaitErr = awaitTool.Invoke(ctx, kernel.ToolCall{TaskID: cur.Id, RunID: cur.CurrentRunId, AgentID: cur.AgentId,
			Input: []byte(`{"timeout":30}`)})
	}()
	time.Sleep(100 * time.Millisecond)
	f.restart(t)
	wg.Wait()
	if connect.CodeOf(awaitErr) != connect.CodeUnavailable || !strings.Contains(awaitErr.Error(), "call team.await again") {
		t.Fatalf("interrupted await err = %v", awaitErr)
	}
	if err := f.e.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	f.finish(t, ids[1], stSucceeded, "finding 2")

	// The orchestrator (same run, still connected) calls team.await again:
	// both results come back, including the one finished before the restart.
	again, err := f.tool(t, "team.await", root.Id, map[string]any{"timeout": 5})
	if err != nil {
		t.Fatal(err)
	}
	done := again["done"].([]any)
	if len(done) != 2 || done[0].(map[string]any)["summary"] != "finding 1" || done[1].(map[string]any)["summary"] != "finding 2" {
		t.Fatalf("await after restart = %v", again)
	}
	// Results are returned once per run.
	last, err := f.tool(t, "team.await", root.Id, map[string]any{"timeout": 1})
	if err != nil || len(last["done"].([]any)) != 0 {
		t.Fatalf("third await = %v, %v", last, err)
	}
}

// ---------------------------------------------------------------- 3. nested teams

func nestedTeams() *taskstest.Teams {
	outer := taskstest.Team("outer", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL,
		taskstest.Member("researcher", "researcher", 1), taskstest.Member("team:inner", "inner", 1))
	outer.Limits = &nexusv1.TeamLimits{MaxTasks: 8, MaxParallel: 2, MaxCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 100}}
	inner := taskstest.Team("inner", "", nexusv1.TeamStrategy_TEAM_STRATEGY_PARALLEL, taskstest.Member("checker", "checker", 2))
	inner.Limits = &nexusv1.TeamLimits{MaxTasks: 20, MaxCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 500}}
	loop := taskstest.Team("loop", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL, taskstest.Member("team:outer", "outer", 1))
	return taskstest.NewTeams(outer, inner, loop)
}

func TestNestedTeamRunsWithinParentLimits(t *testing.T) {
	f := teamFixture(t, nestedTeams(), squadAgents()...)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "investigate", TeamId: "outer"})
	f.runEvent(t, f.get(t, root.Id), output("planning"))
	out, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "inner", "task": "check the claims"})
	if err != nil {
		t.Fatal(err)
	}
	subID := out["task_id"].(string)
	sub := f.get(t, subID)
	if sub.ParentId != root.Id || sub.RootId != subID || sub.TeamId != "inner" || sub.Role != "inner" {
		t.Fatalf("sub-team root = %+v", sub)
	}
	tree, _ := f.e.loadTree(ctx, subID)
	if tree.parentRoot != root.Id {
		t.Fatalf("sub tree parent = %q", tree.parentRoot)
	}
	// Capped by the parent: 8 tasks - 1 used (the sub root counts in both
	// trees) = 8 instead of its own 20; budget ≤ the parent's €1 instead of €5.
	if tree.limits.MaxTasks != 8 || tree.limits.MaxCost.GetMinorUnits() > 100 {
		t.Fatalf("sub limits = %v", tree.limits)
	}
	taskstest.Eventually(t, 2*time.Second, func() bool { return len(f.children(t, subID)) == 2 })
	for _, k := range f.children(t, subID) {
		taskstest.Eventually(t, time.Second, func() bool { return f.dispatched(t, k.Id) })
		if k.RootId != subID {
			t.Fatalf("member root = %s", k.RootId)
		}
		f.costEvent(t, k.Id, 10)
		f.finish(t, k.Id, stSucceeded, "ok "+k.Id)
	}
	taskstest.Eventually(t, 2*time.Second, func() bool { return f.get(t, subID).State == stSucceeded })
	// The parent tree sees the nested costs.
	taskstest.Eventually(t, time.Second, func() bool {
		return f.get(t, root.Id).GetTreeCost().GetAmount().GetMinorUnits() == 20
	})
	aw, err := f.tool(t, "team.await", root.Id, map[string]any{"task_ids": []string{subID}, "timeout": 2})
	if err != nil {
		t.Fatal(err)
	}
	if d := aw["done"].([]any); len(d) != 1 || d[0].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("await sub-team = %v", aw)
	}
	if notes := notices(f, subID); len(notes) != 0 {
		t.Fatal("a sub-team run sent a result notice")
	}
	// Ending the parent cancels a running sub-team.
	out, err = f.tool(t, "team.delegate", root.Id, map[string]any{"role": "inner", "task": "again"})
	if err != nil {
		t.Fatal(err)
	}
	sub2 := out["task_id"].(string)
	taskstest.Eventually(t, 2*time.Second, func() bool {
		k := f.children(t, sub2)
		return len(k) == 2 && f.dispatched(t, k[0].Id) && f.dispatched(t, k[1].Id)
	})
	f.finish(t, root.Id, stSucceeded, "done")
	wantState(t, f.get(t, sub2), stCancelled)
	for _, k := range f.children(t, sub2) {
		wantState(t, f.get(t, k.Id), stCancelled)
	}
}

func TestNestedTeamCycleRefused(t *testing.T) {
	a := taskstest.Team("alpha", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL, taskstest.Member("team:beta", "beta", 1))
	b := taskstest.Team("beta", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL,
		taskstest.Member("team:alpha", "alpha", 1), taskstest.Member("team:gamma", "gamma", 1))
	// gamma contains itself: refused before anything is created.
	g := taskstest.Team("gamma", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL, taskstest.Member("team:gamma", "self", 1))
	f := teamFixture(t, taskstest.NewTeams(a, b, g), squadAgents()...)
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", TeamId: "alpha"})
	f.runEvent(t, f.get(t, root.Id), output("planning"))
	out, err := f.tool(t, "team.delegate", root.Id, map[string]any{"role": "beta", "task": "go"})
	if err != nil {
		t.Fatalf("alpha → beta: %v", err)
	}
	betaRoot := out["task_id"].(string)
	taskstest.Eventually(t, time.Second, func() bool { return f.dispatched(t, betaRoot) })
	f.runEvent(t, f.get(t, betaRoot), output("planning"))
	_, err = f.tool(t, "team.delegate", betaRoot, map[string]any{"role": "alpha", "task": "recurse"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "already runs") {
		t.Fatalf("cycle alpha → beta → alpha: err = %v", err)
	}
	_, err = f.tool(t, "team.delegate", betaRoot, map[string]any{"role": "gamma", "task": "recurse"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "contains itself") {
		t.Fatalf("self-containing team: err = %v", err)
	}
	if k := f.children(t, betaRoot); len(k) != 0 {
		t.Fatalf("cycle created tasks: %v", k)
	}
}

// Escalating a waiting parent starts a new run at once; the children's end
// then does not start yet another run, and the new run waits for them again.
func TestEscalateWaitingParent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "smart"})
	kid := spawn(t, f, root.Id, "helper", "sub")
	f.finish(t, root.Id, stSucceeded, "waiting")
	if _, err := f.e.EscalateTask(ctx, root.Id, "try harder"); err != nil {
		t.Fatal(err)
	}
	p := f.get(t, root.Id)
	if p.State != stAssigned || p.Phase != "" {
		t.Fatalf("escalated = %s/%s", stateName(p.State), p.Phase)
	}
	runs := len(f.disp.AssignmentsFor(root.Id))
	f.finish(t, root.Id, stSucceeded, "still waiting")
	if p := f.get(t, root.Id); p.Phase != phaseWaitingChildren {
		t.Fatalf("phase after second run = %q", p.Phase)
	}
	f.finish(t, kid, stSucceeded, "sub done")
	if got := len(f.disp.AssignmentsFor(root.Id)); got != runs+1 {
		t.Fatalf("runs = %d, want %d", got, runs+1)
	}
	f.finish(t, root.Id, stSucceeded, "final")
	wantState(t, f.get(t, root.Id), stSucceeded)
}
