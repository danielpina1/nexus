package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

type fixture struct {
	env    *taskstest.Env
	e      *Engine
	disp   *taskstest.Dispatcher
	agents *taskstest.Catalog
}

type opt func(f *fixture)

func noDispatcher(f *fixture) { f.disp = nil }

func newFixture(t *testing.T, opts ...opt) *fixture {
	t.Helper()
	f := &fixture{
		env:  taskstest.New(t),
		disp: &taskstest.Dispatcher{},
		agents: taskstest.NewCatalog(
			taskstest.Agent("helper", "Helper", "general help", nexusv1.Tier_TIER_SIMPLE),
			taskstest.Agent("smart", "Smart", "harder problems", nexusv1.Tier_TIER_MEDIUM),
			taskstest.Agent("coder", "Coder", "code fixes", nexusv1.Tier_TIER_HEAVY),
		),
	}
	for _, o := range opts {
		o(f)
	}
	h := f.env.Host
	if f.disp != nil {
		h.Provide(kernel.SvcDispatcher, f.disp)
	}
	if f.agents != nil {
		h.Provide(kernel.SvcAgents, f.agents)
	}
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	f.e = m.Engine
	return f
}

func (f *fixture) create(t *testing.T, req *nexusv1.CreateTaskRequest) *nexusv1.Task {
	t.Helper()
	task, err := f.e.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (f *fixture) runEvent(t *testing.T, task *nexusv1.Task, ev *nexusv1.Event) {
	t.Helper()
	ev.RunId = task.CurrentRunId
	if err := f.e.HandleRunEvent(context.Background(), "node-1", &nexusv1.RunEvent{Event: ev}); err != nil {
		t.Fatalf("HandleRunEvent: %v", err)
	}
}

func (f *fixture) get(t *testing.T, id string) *nexusv1.Task {
	t.Helper()
	task, err := f.e.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func wantState(t *testing.T, task *nexusv1.Task, want nexusv1.TaskState) {
	t.Helper()
	if task.State != want {
		t.Fatalf("state = %s, want %s (summary %q)", stateName(task.State), stateName(want), task.Summary)
	}
}

func wantStates(t *testing.T, got []nexusv1.TaskState, want ...nexusv1.TaskState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
}

func output(text string) *nexusv1.Event {
	return &nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: text}}}
}

func done(state nexusv1.TaskState, summary string) *nexusv1.Event {
	return &nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{State: state, Summary: summary}}}}
}

func TestTransitions(t *testing.T) {
	cases := []struct {
		from, to nexusv1.TaskState
		ok       bool
	}{
		{stQueued, stRouting, true},
		{stQueued, stAssigned, true},
		{stRouting, stNeedsInput, true},
		{stNeedsInput, stRouting, true},
		{stNeedsInput, stAssigned, false},
		{stAssigned, stRunning, true},
		{stRunning, stWaitingApproval, true},
		{stWaitingApproval, stRunning, true},
		{stWaitingApproval, stSucceeded, false},
		{stRunning, stWaitingInput, true},
		{stWaitingInput, stRunning, true},
		{stRunning, stEscalating, true},
		{stEscalating, stAssigned, true},
		{stRunning, stSucceeded, true},
		{stQueued, stCancelled, true},
		{stSucceeded, stRunning, false},
		{stCancelled, stEscalating, false},
		{stFailed, stEscalating, true},
		{stQueued, stRunning, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("%s → %s = %v, want %v", stateName(c.from), stateName(c.to), got, c.ok)
		}
	}
	for s := range transitions {
		if IsTerminal(s) && s != stCancelled && len(transitions[s]) != 1 {
			t.Errorf("terminal %s should only allow escalation", stateName(s))
		}
	}
}

func TestCreateWithAgentDispatches(t *testing.T) {
	f := newFixture(t)
	f.agents.Add(&nexusv1.Agent{Id: "tagged", Runtime: "shell", NodeTags: []string{"browser"}, Enabled: true, Tier: nexusv1.Tier_TIER_SIMPLE}, "do it")
	params, _ := structpb.NewStruct(map[string]any{"list": "milk"})
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "buy milk\nand eggs", AgentId: "tagged", Params: params, Source: "cli"})
	wantState(t, task, stAssigned)
	if task.NodeId != "node-1" || task.CurrentRunId == "" || task.Title != "buy milk" || task.RootId != task.Id {
		t.Fatalf("unexpected task %+v", task)
	}
	if len(f.disp.Assigned) != 1 {
		t.Fatalf("assignments = %d", len(f.disp.Assigned))
	}
	as := f.disp.Assigned[0]
	if as.Selector.Runtime != "shell" || len(as.Selector.Tags) != 1 || as.Selector.Tags[0] != "browser" {
		t.Fatalf("selector = %+v", as.Selector)
	}
	a := as.Assignment
	if a.RunToken == "" || a.Instructions != "do it" || a.Prompt != task.Prompt || a.TaskId != task.Id || a.RunId != task.CurrentRunId {
		t.Fatalf("assignment = %+v", a)
	}
	if got := a.Context.Fields["list"].GetStringValue(); got != "milk" {
		t.Fatalf("context params lost: %v", a.Context)
	}
	// queued, assigned, then an update carrying the node id.
	wantStates(t, f.env.Bus.TaskStates(task.Id), stQueued, stAssigned, stAssigned)

	if _, err := f.e.VerifyRunToken(context.Background(), a.RunId, a.RunToken); err != nil {
		t.Fatalf("VerifyRunToken: %v", err)
	}
	if _, err := f.e.VerifyRunToken(context.Background(), a.RunId, "nope"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("bad token err = %v", err)
	}
}

func TestCreateIdempotent(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "helper", IdempotencyKey: "k1"})
	b := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "helper", IdempotencyKey: "k1"})
	if a.Id != b.Id || len(f.disp.Assigned) != 1 {
		t.Fatalf("idempotency broken: %s vs %s, %d assignments", a.Id, b.Id, len(f.disp.Assigned))
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	if _, err := f.e.CreateTask(context.Background(), &nexusv1.CreateTaskRequest{Prompt: "  "}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

func TestDispatchFailures(t *testing.T) {
	cases := []struct {
		name  string
		opts  []opt
		setup func(f *fixture)
		agent string
	}{
		{"no dispatcher", []opt{noDispatcher}, nil, "helper"},
		{"no catalog", []opt{func(f *fixture) { f.agents = nil }}, nil, "helper"},
		{"unknown agent", nil, nil, "ghost"},
		{"no node", nil, func(f *fixture) { f.disp.AssignErr = errors.New("no matching node") }, "helper"},
		{"disabled agent", nil, func(f *fixture) {
			f.agents.Add(&nexusv1.Agent{Id: "off", Enabled: false}, "")
		}, "off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.opts...)
			if c.setup != nil {
				c.setup(f)
			}
			task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: c.agent})
			wantState(t, task, stFailed)
			if task.Summary == "" {
				t.Fatal("failed task without summary")
			}
		})
	}
}

func TestRouting(t *testing.T) {
	t.Run("confident", func(t *testing.T) {
		f := newFixture(t)
		rt := &taskstest.Router{Result: &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, AgentId: "coder", Confidence: 0.9, Tier: nexusv1.Tier_TIER_HEAVY}}
		f.env.Host.Provide(kernel.SvcRouter, rt)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "fix the tests"})
		wantState(t, task, stAssigned)
		if task.AgentId != "coder" || task.Tier != nexusv1.Tier_TIER_HEAVY {
			t.Fatalf("task = %+v", task)
		}
		wantStates(t, f.env.Bus.TaskStates(task.Id), stQueued, stRouting, stAssigned, stAssigned)
	})

	t.Run("low confidence asks then answer assigns", func(t *testing.T) {
		f := newFixture(t)
		rt := &taskstest.Router{Result: &nexusv1.Route{AgentId: "helper", Confidence: 0.4, Alternatives: []string{"smart", "helper"}}}
		f.env.Host.Provide(kernel.SvcRouter, rt)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "something vague"})
		wantState(t, task, stNeedsInput)
		qs := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.GetQuestion() != nil })
		if len(qs) != 1 {
			t.Fatalf("questions = %d", len(qs))
		}
		q := qs[0].GetQuestion()
		if len(q.Choices) != 2 || q.Choices[0] != "helper" || q.Choices[1] != "smart" || qs[0].TaskId != task.Id {
			t.Fatalf("question = %+v", q)
		}
		err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: task.Id, QuestionId: q.QuestionId, Choice: "smart"})
		if err != nil {
			t.Fatal(err)
		}
		got := f.get(t, task.Id)
		wantState(t, got, stAssigned)
		if got.AgentId != "smart" {
			t.Fatalf("agent = %s", got.AgentId)
		}
	})

	t.Run("free text re-routes", func(t *testing.T) {
		f := newFixture(t)
		rt := &taskstest.Router{Result: &nexusv1.Route{Confidence: 0.1}}
		f.env.Host.Provide(kernel.SvcRouter, rt)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "hmm"})
		wantState(t, task, stNeedsInput) // choices fall back to the catalog
		rt.Result = &nexusv1.Route{AgentId: "helper", Confidence: 0.95}
		if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: task.Id, Text: "use the helper"}); err != nil {
			t.Fatal(err)
		}
		wantState(t, f.get(t, task.Id), stAssigned)
		if len(rt.Inputs) != 2 || rt.Inputs[1] != "hmm\n\nClarification: use the helper" {
			t.Fatalf("router inputs = %q", rt.Inputs)
		}
	})

	t.Run("router error fails", func(t *testing.T) {
		f := newFixture(t)
		f.env.Host.Provide(kernel.SvcRouter, &taskstest.Router{Err: errors.New("boom")})
		wantState(t, f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x"}), stFailed)
	})

	t.Run("no router asks with catalog", func(t *testing.T) {
		f := newFixture(t)
		wantState(t, f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x"}), stNeedsInput)
	})

	t.Run("dry run persists nothing", func(t *testing.T) {
		f := newFixture(t)
		f.env.Host.Provide(kernel.SvcRouter, &taskstest.Router{Result: &nexusv1.Route{AgentId: "smart", Confidence: 0.9}})
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "x", DryRun: true})
		if task.Id != "" || task.AgentId != "smart" || task.Tier != nexusv1.Tier_TIER_MEDIUM {
			t.Fatalf("dry run = %+v", task)
		}
		if len(f.env.Bus.Events()) != 0 || len(f.disp.Assigned) != 0 {
			t.Fatal("dry run had side effects")
		}
	})
}

func TestRunLifecycle(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "helper"})

	f.runEvent(t, task, output("hello "))
	wantState(t, f.get(t, task.Id), stRunning)

	f.runEvent(t, task, &nexusv1.Event{Body: &nexusv1.Event_Cost{Cost: &nexusv1.CostRecorded{Cost: &nexusv1.Cost{
		InputTokens: 10, Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 3}}}}})
	f.runEvent(t, task, &nexusv1.Event{Body: &nexusv1.Event_Cost{Cost: &nexusv1.CostRecorded{Cost: &nexusv1.Cost{
		InputTokens: 5, OutputTokens: 2, Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 4}}}}})
	c := f.get(t, task.Id).Cost
	if c.InputTokens != 15 || c.OutputTokens != 2 || c.Amount.MinorUnits != 7 {
		t.Fatalf("cost = %+v", c)
	}

	f.runEvent(t, task, &nexusv1.Event{Body: &nexusv1.Event_Question{Question: &nexusv1.Question{QuestionId: "q1", Text: "which?"}}})
	wantState(t, f.get(t, task.Id), stWaitingInput)

	if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: task.Id, QuestionId: "q1", Choice: "A", IdempotencyKey: "m1"}); err != nil {
		t.Fatal(err)
	}
	// Duplicate delivery is ignored.
	if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: task.Id, QuestionId: "q1", Choice: "A", IdempotencyKey: "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.disp.Inputs) != 1 || f.disp.Inputs[0].Choice != "A" || f.disp.Inputs[0].QuestionId != "q1" || f.disp.Inputs[0].RunId != task.CurrentRunId {
		t.Fatalf("inputs = %+v", f.disp.Inputs)
	}
	wantState(t, f.get(t, task.Id), stRunning)

	f.runEvent(t, task, done(stSucceeded, "all done"))
	got := f.get(t, task.Id)
	wantState(t, got, stSucceeded)
	if got.Summary != "all done" {
		t.Fatalf("summary = %q", got.Summary)
	}
	wantStates(t, f.env.Bus.TaskStates(task.Id), stQueued, stAssigned, stAssigned, stRunning, stWaitingInput, stRunning, stSucceeded)

	outs := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.GetRunOutput() != nil })
	if len(outs) != 1 || outs[0].Actor != "node:node-1" || outs[0].TaskId != task.Id || outs[0].RunId != task.CurrentRunId {
		t.Fatalf("republished output = %+v", outs)
	}
	notices := f.env.Bus.Filter(func(e *nexusv1.Event) bool {
		return e.GetNotice().GetKind() == nexusv1.NoticeKind_NOTICE_KIND_RESULT
	})
	if len(notices) != 1 || notices[0].GetNotice().Body != "all done" {
		t.Fatalf("result notices = %+v", notices)
	}

	// Late events of a finished task are republished but do not change state.
	f.runEvent(t, task, output("late"))
	wantState(t, f.get(t, task.Id), stSucceeded)
	if err := f.e.SendMessage(context.Background(), &nexusv1.SendMessageRequest{TaskId: task.Id, Text: "hi"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("message to finished task err = %v", err)
	}
}

func TestRunEventValidation(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "helper"})
	ctx := context.Background()
	err := f.e.HandleRunEvent(ctx, "node-2", &nexusv1.RunEvent{Event: &nexusv1.Event{RunId: task.CurrentRunId, Body: output("x").Body}})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign node err = %v", err)
	}
	if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: &nexusv1.Event{Body: output("x").Body}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing run err = %v", err)
	}
	if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: &nexusv1.Event{RunId: "run_nope", Body: output("x").Body}}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown run err = %v", err)
	}
}

func TestApprovals(t *testing.T) {
	approval := &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: &nexusv1.Approval{Title: "push", ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH}}}

	t.Run("approved resumes", func(t *testing.T) {
		f := newFixture(t)
		broker := &taskstest.Broker{}
		f.env.Host.Provide(kernel.SvcApprovals, broker)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "coder"})
		f.runEvent(t, task, approval)
		wantState(t, f.get(t, task.Id), stWaitingApproval)
		if len(broker.Requested) != 1 {
			t.Fatal("approval not requested")
		}
		a := broker.Requested[0]
		if a.TaskId != task.Id || a.RunId != task.CurrentRunId || a.NodeId != "node-1" || a.AgentId != "coder" {
			t.Fatalf("approval = %+v", a)
		}
		a.State = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
		if err := f.e.OnApprovalResolved(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		wantState(t, f.get(t, task.Id), stRunning)
	})

	t.Run("denied fails and cancels", func(t *testing.T) {
		f := newFixture(t)
		broker := &taskstest.Broker{}
		f.env.Host.Provide(kernel.SvcApprovals, broker)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "coder"})
		f.runEvent(t, task, approval)
		a := broker.Requested[0]
		a.State = nexusv1.ApprovalState_APPROVAL_STATE_DENIED
		if err := f.e.OnApprovalResolved(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		wantState(t, f.get(t, task.Id), stFailed)
		if len(f.disp.Cancels) != 1 || f.disp.Cancels[0].RunId != task.CurrentRunId {
			t.Fatalf("cancels = %+v", f.disp.Cancels)
		}
	})

	t.Run("no broker fails closed", func(t *testing.T) {
		f := newFixture(t)
		task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "go", AgentId: "coder"})
		f.runEvent(t, task, approval)
		wantState(t, f.get(t, task.Id), stFailed)
		if len(f.disp.Cancels) != 1 {
			t.Fatal("run not cancelled")
		}
	})
}

func TestCancelAndStopAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	parent := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "parent", AgentId: "helper"})
	child, err := f.e.CreateChildTask(ctx, parent.Id, &nexusv1.CreateTaskRequest{Prompt: "child", AgentId: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentId != parent.Id || child.RootId != parent.Id {
		t.Fatalf("child = %+v", child)
	}
	kids, err := f.e.ChildTasks(ctx, parent.Id)
	if err != nil || len(kids) != 1 {
		t.Fatalf("children = %v, %v", kids, err)
	}
	if err := f.e.CancelTask(ctx, parent.Id); err != nil {
		t.Fatal(err)
	}
	wantState(t, f.get(t, parent.Id), stCancelled)
	wantState(t, f.get(t, child.Id), stCancelled)
	if len(f.disp.Cancels) != 2 {
		t.Fatalf("node cancels = %d", len(f.disp.Cancels))
	}
	if err := f.e.CancelTask(ctx, parent.Id); err != nil {
		t.Fatalf("second cancel: %v", err)
	}

	a := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "a", AgentId: "helper"})
	b := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "b", AgentId: "helper"})
	f.runEvent(t, b, done(stSucceeded, "ok"))
	n, err := f.e.StopAll(ctx, true)
	if err != nil || n != 1 {
		t.Fatalf("StopAll = %d, %v", n, err)
	}
	wantState(t, f.get(t, a.Id), stCancelled)
	wantState(t, f.get(t, b.Id), stSucceeded)
}

func TestEscalate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "solve", AgentId: "helper"})
	oldRun := task.CurrentRunId
	f.runEvent(t, task, output("first try"))

	esc, err := f.e.EscalateTask(ctx, task.Id, "be thorough")
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, esc, stAssigned)
	if esc.AgentId != "smart" || esc.Tier != nexusv1.Tier_TIER_MEDIUM || esc.CurrentRunId == oldRun {
		t.Fatalf("escalated = %+v", esc)
	}
	if len(f.disp.Cancels) != 1 || f.disp.Cancels[0].RunId != oldRun {
		t.Fatalf("old run not cancelled: %+v", f.disp.Cancels)
	}
	c := f.disp.LastAssignment().Context.AsMap()
	prev, _ := c["previous_attempt"].(map[string]any)
	if c["escalation_hint"] != "be thorough" || prev["output"] != "first try" || prev["run_id"] != oldRun {
		t.Fatalf("context = %v", c)
	}
	// Events from the superseded run do not touch the task.
	if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: &nexusv1.Event{RunId: oldRun, Body: done(stFailed, "x").Body}}); err != nil {
		t.Fatal(err)
	}
	wantState(t, f.get(t, task.Id), stAssigned)

	// Escalating a finished task ("Try harder") starts a new heavy run.
	cur := f.get(t, task.Id)
	f.runEvent(t, cur, done(stSucceeded, "meh"))
	esc, err = f.e.EscalateTask(ctx, task.Id, "")
	if err != nil {
		t.Fatal(err)
	}
	if esc.AgentId != "coder" || esc.State != stAssigned || esc.Summary != "" {
		t.Fatalf("second escalation = %+v", esc)
	}
	if len(f.disp.Cancels) != 1 {
		t.Fatal("finished run should not be cancelled again")
	}
}

func TestHubTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, &nexusv1.CreateTaskRequest{Prompt: "plan", AgentId: "helper"})
	reg := f.env.Host.Tools()
	call := func(name string, in any) (kernel.ToolResult, error) {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		b, _ := json.Marshal(in)
		return tool.Invoke(ctx, kernel.ToolCall{RunID: task.CurrentRunId, AgentID: "helper", Input: b})
	}

	res, err := call("spawn_agent", map[string]any{"prompt": "sub job", "agent_id": "smart"})
	if err != nil {
		t.Fatal(err)
	}
	var spawned map[string]any
	_ = json.Unmarshal(res.Output, &spawned)
	child := f.get(t, spawned["task_id"].(string))
	if child.ParentId != task.Id || child.AgentId != "smart" || spawned["state"] != "assigned" {
		t.Fatalf("child = %+v / %v", child, spawned)
	}

	if _, err := call("notify", map[string]any{"title": "hi", "body": "there", "kind": "alert"}); err != nil {
		t.Fatal(err)
	}
	ns := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.GetNotice().GetKind() == nexusv1.NoticeKind_NOTICE_KIND_ALERT })
	if len(ns) != 1 || ns[0].TaskId != task.Id || ns[0].Actor != "agent:helper" {
		t.Fatalf("notices = %+v", ns)
	}

	res, err = call("ask_user", map[string]any{"question": "Color?", "choices": []string{"red", "blue"}})
	if err != nil {
		t.Fatal(err)
	}
	var asked map[string]string
	_ = json.Unmarshal(res.Output, &asked)
	wantState(t, f.get(t, task.Id), stWaitingInput)
	qs := f.env.Bus.Filter(func(e *nexusv1.Event) bool { return e.GetQuestion().GetQuestionId() == asked["question_id"] })
	if len(qs) != 1 || qs[0].GetQuestion().AllowFreeText {
		t.Fatalf("question events = %+v", qs)
	}

	if _, err := call("schedule", map[string]any{"at": "2030-01-01T00:00:00Z", "prompt": "x"}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("schedule err = %v", err)
	}

	f.engineCfgDepth(1)
	childRun := child.CurrentRunId
	tool, _ := reg.Get("spawn_agent")
	_, err = tool.Invoke(ctx, kernel.ToolCall{RunID: childRun, Input: json.RawMessage(`{"prompt":"deeper","agent_id":"helper"}`)})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("depth limit err = %v", err)
	}
}

func (f *fixture) engineCfgDepth(d int) { f.e.cfg.MaxDepth = d }

func TestListTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, f.create(t, &nexusv1.CreateTaskRequest{Prompt: "t", AgentId: "helper"}).Id)
	}
	if _, err := f.e.CreateChildTask(ctx, ids[0], &nexusv1.CreateTaskRequest{Prompt: "c", AgentId: "smart"}); err != nil {
		t.Fatal(err)
	}
	_ = f.e.CancelTask(ctx, ids[4])

	page1, next, err := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{RootsOnly: true, Page: &nexusv1.PageRequest{PageSize: 3}})
	if err != nil || len(page1) != 3 || next == "" || page1[0].Id != ids[4] {
		t.Fatalf("page1 = %d next=%q err=%v", len(page1), next, err)
	}
	page2, next, err := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{RootsOnly: true, Page: &nexusv1.PageRequest{PageSize: 3, PageToken: next}})
	if err != nil || len(page2) != 2 || next != "" || page2[1].Id != ids[0] {
		t.Fatalf("page2 = %d next=%q err=%v", len(page2), next, err)
	}
	cancelled, _, _ := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{States: []nexusv1.TaskState{stCancelled}})
	if len(cancelled) != 1 || cancelled[0].Id != ids[4] {
		t.Fatalf("cancelled = %v", cancelled)
	}
	byAgent, _, _ := f.e.ListTasks(ctx, &nexusv1.ListTasksRequest{AgentId: "smart"})
	if len(byAgent) != 1 {
		t.Fatalf("by agent = %d", len(byAgent))
	}
}

func TestConnectHandler(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.env.Host.Mux)
	defer srv.Close()
	c := nexusv1connect.NewTaskServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()
	resp, err := c.CreateTask(ctx, connect.NewRequest(&nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "helper"}))
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Msg.Task.Id
	got, err := c.GetTask(ctx, connect.NewRequest(&nexusv1.GetTaskRequest{Id: id, IncludeChildren: true}))
	if err != nil || got.Msg.Task.State != stAssigned {
		t.Fatalf("GetTask = %v, %v", got, err)
	}
	if _, err := c.GetTask(ctx, connect.NewRequest(&nexusv1.GetTaskRequest{Id: "tsk_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := c.FollowTask(ctx, connect.NewRequest(&nexusv1.FollowTaskRequest{TaskId: id, DeviceIds: []string{"d1"}})); err != nil {
		t.Fatal(err)
	}
	stop, err := c.StopAll(ctx, connect.NewRequest(&nexusv1.StopAllRequest{}))
	if err != nil || stop.Msg.Cancelled != 1 {
		t.Fatalf("StopAll = %v, %v", stop, err)
	}
}
