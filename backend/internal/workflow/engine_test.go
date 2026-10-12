package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/internal/triggers"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// ---------------------------------------------------------------- fakes

type fakeTasks struct {
	mu       sync.Mutex
	tasks    map[string]*nexusv1.Task
	byKey    map[string]string
	created  []*nexusv1.CreateTaskRequest
	hold     bool
	fail     map[string]string // agent → failure summary
	canceled []string
}

func newFakeTasks() *fakeTasks {
	return &fakeTasks{tasks: map[string]*nexusv1.Task{}, byKey: map[string]string{}, fail: map[string]string{}}
}

func (f *fakeTasks) finish(t *nexusv1.Task) {
	if msg, ok := f.fail[t.AgentId]; ok {
		t.State, t.Summary = nexusv1.TaskState_TASK_STATE_FAILED, msg
		return
	}
	t.State, t.Summary = nexusv1.TaskState_TASK_STATE_SUCCEEDED, "done:"+t.AgentId
	t.Result, _ = structpb.NewStruct(map[string]any{"agent": t.AgentId})
}

func (f *fakeTasks) CreateTask(_ context.Context, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
		return proto.Clone(f.tasks[id]).(*nexusv1.Task), nil
	}
	f.created = append(f.created, proto.Clone(req).(*nexusv1.CreateTaskRequest))
	t := &nexusv1.Task{Id: fmt.Sprintf("tsk_%d", len(f.created)), AgentId: req.AgentId, Prompt: req.Prompt,
		Source: req.Source, State: nexusv1.TaskState_TASK_STATE_RUNNING}
	if !f.hold {
		f.finish(t)
	}
	f.tasks[t.Id] = t
	f.byKey[req.IdempotencyKey] = t.Id
	return proto.Clone(t).(*nexusv1.Task), nil
}

func (f *fakeTasks) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = false
	for _, t := range f.tasks {
		if t.State == nexusv1.TaskState_TASK_STATE_RUNNING {
			f.finish(t)
		}
	}
}

func (f *fakeTasks) requests() []*nexusv1.CreateTaskRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*nexusv1.CreateTaskRequest(nil), f.created...)
}

func (f *fakeTasks) GetTask(_ context.Context, id string) (*nexusv1.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return nil, errors.New("no task " + id)
	}
	return proto.Clone(t).(*nexusv1.Task), nil
}
func (f *fakeTasks) ListTasks(context.Context, *nexusv1.ListTasksRequest) ([]*nexusv1.Task, string, error) {
	return nil, "", nil
}
func (f *fakeTasks) ChildTasks(context.Context, string) ([]*nexusv1.Task, error)    { return nil, nil }
func (f *fakeTasks) SendMessage(context.Context, *nexusv1.SendMessageRequest) error { return nil }
func (f *fakeTasks) EscalateTask(context.Context, string, string) (*nexusv1.Task, error) {
	return nil, nil
}
func (f *fakeTasks) StopAll(context.Context, bool) (int, error)                      { return 0, nil }
func (f *fakeTasks) HandleRunEvent(context.Context, string, *nexusv1.RunEvent) error { return nil }
func (f *fakeTasks) CancelTask(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, id)
	if t, ok := f.tasks[id]; ok {
		t.State = nexusv1.TaskState_TASK_STATE_CANCELLED
	}
	return nil
}

type fakeBroker struct {
	mu sync.Mutex
	m  map[string]*nexusv1.Approval
}

func (b *fakeBroker) Request(_ context.Context, a *nexusv1.Approval) (*nexusv1.Approval, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a = proto.Clone(a).(*nexusv1.Approval)
	a.Id = fmt.Sprintf("apr_%d", len(b.m)+1)
	a.State = nexusv1.ApprovalState_APPROVAL_STATE_PENDING
	b.m[a.Id] = a
	return proto.Clone(a).(*nexusv1.Approval), nil
}
func (b *fakeBroker) Get(_ context.Context, id string) (*nexusv1.Approval, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.m[id]
	if !ok {
		return nil, ErrNotFound
	}
	return proto.Clone(a).(*nexusv1.Approval), nil
}
func (b *fakeBroker) List(context.Context, []nexusv1.ApprovalState) ([]*nexusv1.Approval, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*nexusv1.Approval
	for _, a := range b.m {
		out = append(out, proto.Clone(a).(*nexusv1.Approval))
	}
	return out, nil
}
func (b *fakeBroker) Decide(context.Context, *nexusv1.DecideRequest) (*nexusv1.Approval, error) {
	return nil, errors.New("unused")
}

// decideAll resolves every pending approval.
func (b *fakeBroker) decideAll(s nexusv1.ApprovalState) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, a := range b.m {
		if a.State == nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
			a.State = s
			n++
		}
	}
	return n
}

type env struct {
	h      *kerneltest.Host
	st     *taskstest.Store
	bus    *taskstest.Bus
	tasks  *fakeTasks
	broker *fakeBroker
	dir    string

	mu       sync.Mutex
	calls    map[string][]kernel.ToolCall
	blockExt bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{h: kerneltest.New(t), st: taskstest.NewStore(t), bus: taskstest.NewBus(), tasks: newFakeTasks(),
		broker: &fakeBroker{m: map[string]*nexusv1.Approval{}}, dir: t.TempDir(), calls: map[string][]kernel.ToolCall{}}
	e.h.Provide(kernel.SvcStore, e.st)
	e.h.Provide(kernel.SvcEventBus, e.bus)
	e.h.Provide(kernel.SvcTasks, e.tasks)
	e.h.Provide(kernel.SvcApprovals, e.broker)
	reg := func(name string, class nexusv1.ActionClass, f func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error)) {
		if err := e.h.Tools().Register(kernel.ToolFunc{S: kernel.ToolSpec{Name: name, ActionClass: class},
			F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
				e.mu.Lock()
				e.calls[name] = append(e.calls[name], c)
				e.mu.Unlock()
				return f(ctx, c)
			}}); err != nil {
			t.Fatal(err)
		}
	}
	echo := func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolResult{Output: c.Input}, nil
	}
	reg("test.echo", nexusv1.ActionClass_ACTION_CLASS_READ, echo)
	reg("notify", nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, echo)
	reg("nexus.status", nexusv1.ActionClass_ACTION_CLASS_READ, func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolOutput(map[string]any{"nodes": 2})
	})
	reg("vault.daily_append", nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, echo)
	reg("test.spend", nexusv1.ActionClass_ACTION_CLASS_SPEND, func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolOutput(map[string]any{"ok": true})
	})
	reg("test.external", nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL, func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
		e.mu.Lock()
		block := e.blockExt
		e.mu.Unlock()
		if block {
			<-ctx.Done()
			return kernel.ToolResult{}, ctx.Err()
		}
		return kernel.ToolOutput(map[string]any{"sent": true})
	})
	reg("test.fail", nexusv1.ActionClass_ACTION_CLASS_READ, func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolResult{}, errors.New("boom")
	})
	return e
}

func (e *env) ncalls(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls[name])
}

func (e *env) lastInput(t *testing.T, name string) map[string]any {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	cs := e.calls[name]
	if len(cs) == 0 {
		t.Fatalf("tool %s not called", name)
	}
	var m map[string]any
	if err := json.Unmarshal(cs[len(cs)-1].Input, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) write(t *testing.T, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) copyTemplate(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(templateDir, name))
	if err != nil {
		t.Fatal(err)
	}
	e.write(t, name, string(b))
}

func (e *env) service(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(e.h, e.st, Config{PollInterval: 5 * time.Millisecond, ReloadInterval: 20 * time.Millisecond}, func() string { return e.dir })
	if err != nil {
		t.Fatal(err)
	}
	s.backoff = func(int) time.Duration { return time.Millisecond }
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitRun(t *testing.T, s *Service, id string) *RunInfo {
	t.Helper()
	var r *RunInfo
	eventually(t, "run "+id, func() bool {
		var err error
		r, err = s.GetRun(context.Background(), id)
		return err == nil && runTerminal(r.State)
	})
	return r
}

func stepOf(r *RunInfo, path string) StepInfo {
	for _, s := range r.Steps {
		if s.Path == path {
			return s
		}
	}
	return StepInfo{}
}

func start(t *testing.T, s *Service, o StartOptions) string {
	t.Helper()
	id, err := s.StartWith(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const hdr = "---\ntype: workflow\nid: %s\nname: %s\nsteps:\n"

func wf(id, steps string) string { return fmt.Sprintf(hdr, id, id) + steps + "---\n" }

// ---------------------------------------------------------------- tests

func TestGroceriesTemplate(t *testing.T) {
	e := newEnv(t)
	e.copyTemplate(t, "groceries.md")
	s := e.service(t)

	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "groceries", Inputs: map[string]any{"guidelines": "organic"}}))
	if r.State != RunSucceeded {
		t.Fatalf("run = %s %s", r.State, r.Error)
	}
	reqs := e.tasks.requests()
	if len(reqs) != 2 || reqs[0].AgentId != "assistant" || reqs[1].AgentId != "grocery-continente" {
		t.Fatalf("tasks = %v", reqs)
	}
	if !strings.Contains(reqs[1].Prompt, "List: done:assistant") || !strings.Contains(reqs[1].Prompt, "Guidelines: organic") {
		t.Errorf("shop prompt = %q", reqs[1].Prompt)
	}
	if reqs[1].Source != "workflow" || reqs[1].Params.AsMap()["workflow_run_id"] != r.ID || reqs[1].IdempotencyKey == "" {
		t.Errorf("shop request = %v", reqs[1])
	}
	if r.Output != "done:grocery-continente" {
		t.Errorf("output = %v", r.Output)
	}
	if st := stepOf(r, "shop"); st.TaskID == "" || st.Result.(map[string]any)["agent"] != "grocery-continente" {
		t.Errorf("shop step = %+v", st)
	}

	// With a list the "usual" step is skipped.
	r = waitRun(t, s, start(t, s, StartOptions{WorkflowID: "groceries", Inputs: map[string]any{"list": "milk, eggs"}}))
	if r.State != RunSucceeded || stepOf(r, "usual").State != StepSkipped {
		t.Fatalf("run = %s, usual = %+v", r.State, stepOf(r, "usual"))
	}
	reqs = e.tasks.requests()
	if len(reqs) != 3 || !strings.Contains(reqs[2].Prompt, "List: milk, eggs") {
		t.Errorf("prompt = %q", reqs[len(reqs)-1].Prompt)
	}
}

func TestMorningBriefTemplate(t *testing.T) {
	e := newEnv(t)
	e.copyTemplate(t, "morning-brief.md")
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "morning-brief"}))
	if r.State != RunSucceeded {
		t.Fatalf("run = %s %s", r.State, r.Error)
	}
	reqs := e.tasks.requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Prompt, `system: {"nodes":2}`) || !strings.Contains(reqs[1].Prompt, "calendar: done:assistant") {
		t.Fatalf("brief prompt: %v", reqs)
	}
	if reqs[1].Params.AsMap()["model"] != "fast-local" {
		t.Errorf("model param = %v", reqs[1].Params.AsMap())
	}
	if in := e.lastInput(t, "notify"); in["body"] != "done:assistant" || in["title"] != "Good morning" {
		t.Errorf("notify input = %v", in)
	}
	if in := e.lastInput(t, "vault.daily_append"); in["text"] != "done:assistant" {
		t.Errorf("daily_append input = %v", in)
	}
}

func TestAgentFailure(t *testing.T) {
	e := newEnv(t)
	e.tasks.fail["bad"] = "could not log in"
	e.write(t, "f.md", wf("f", `  - id: a
    agent: bad
    task: try
  - id: after
    tool: test.echo
`))
	e.write(t, "c.md", wf("c", `  - id: a
    agent: bad
    task: try
    on_error: continue
  - id: after
    tool: test.echo
    with: { prev: "{{ steps.a.state }}: {{ steps.a.error }}" }
`))
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "f"}))
	if r.State != RunFailed || !strings.Contains(r.Error, "could not log in") {
		t.Fatalf("run = %s %q", r.State, r.Error)
	}
	if e.ncalls("test.echo") != 0 {
		t.Error("step after a failure ran")
	}
	r = waitRun(t, s, start(t, s, StartOptions{WorkflowID: "c"}))
	if r.State != RunSucceeded {
		t.Fatalf("continue run = %s %q", r.State, r.Error)
	}
	if in := e.lastInput(t, "test.echo"); !strings.HasPrefix(in["prev"].(string), "failed: agent bad task") {
		t.Errorf("prev = %v", in["prev"])
	}
	// A failure notice is published for the failed root run.
	found := false
	for _, ev := range e.bus.Events() {
		if n := ev.GetNotice(); n != nil && n.Kind == nexusv1.NoticeKind_NOTICE_KIND_ALERT && strings.Contains(n.Title, "f failed") {
			found = true
		}
	}
	if !found {
		t.Error("no failure notice")
	}
}

func TestControlFlow(t *testing.T) {
	e := newEnv(t)
	e.write(t, "cf.md", `---
type: workflow
id: cf
inputs:
  n: { type: number, default: 3 }
steps:
  - id: list
    tool: test.echo
    with: { items: ["a", "b", "c"] }
  - id: loop
    foreach: "steps.list.output.items"
    as: letter
    parallel: 2
    do:
      - id: up
        tool: test.echo
        with: { v: "{{ letter.upperAscii() }}{{ index }}" }
  - id: par
    parallel:
      - id: left
        tool: test.echo
        with: { side: left }
      - id: right
        do:
          - id: r1
            tool: test.echo
            with: { side: right }
  - id: pick
    if: "size(steps.loop.output) == inputs.n"
    then:
      - id: yes
        tool: test.echo
        with: { got: "{{ steps.loop.output }} {{ steps.left.output.side }} {{ steps.r1.output.side }}" }
    else:
      - id: no
        tool: test.echo
---
`)
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "cf"}))
	if r.State != RunSucceeded {
		t.Fatalf("run = %s %q", r.State, r.Error)
	}
	if st := stepOf(r, "loop[2]/up"); st.State != StepSucceeded || st.Output.(map[string]any)["v"] != "C2" {
		t.Errorf("loop[2]/up = %+v", st)
	}
	want := `[{"v":"A0"},{"v":"B1"},{"v":"C2"}] left right`
	if got := r.Output.(map[string]any)["got"]; got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
	if stepOf(r, "pick/no").Path != "" {
		t.Error("else branch ran")
	}
	if st := stepOf(r, "pick"); st.Result.(map[string]any)["branch"] != "then" {
		t.Errorf("pick = %+v", st)
	}
}

func TestApprove(t *testing.T) {
	e := newEnv(t)
	e.write(t, "buy.md", wf("buy", `  - id: ok
    approve:
      title: "Checkout {{ inputs.total }} EUR"
      action_class: spend
      preview: { total: "{{ inputs.total }}" }
  - id: pay
    tool: test.spend
`))
	s := e.service(t)

	id := start(t, s, StartOptions{WorkflowID: "buy", Inputs: map[string]any{"total": 84.3}})
	eventually(t, "approval", func() bool { l, _ := e.broker.List(context.Background(), nil); return len(l) == 1 })
	l, _ := e.broker.List(context.Background(), nil)
	if a := l[0]; a.Title != "Checkout 84.3 EUR" || a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_SPEND ||
		a.Payload.AsMap()["total"] != 84.3 || a.Payload.AsMap()["workflow"].(map[string]any)["run_id"] != id {
		t.Errorf("approval = %v", a)
	}
	e.broker.decideAll(nexusv1.ApprovalState_APPROVAL_STATE_DENIED)
	r := waitRun(t, s, id)
	if r.State != RunFailed || !strings.Contains(r.Error, "approval denied") || e.ncalls("test.spend") != 0 {
		t.Fatalf("deny: run = %s %q, spend calls %d", r.State, r.Error, e.ncalls("test.spend"))
	}

	id = start(t, s, StartOptions{WorkflowID: "buy", Inputs: map[string]any{"total": 10}})
	eventually(t, "approval", func() bool { return e.broker.decideAll(nexusv1.ApprovalState_APPROVAL_STATE_APPROVED) == 1 })
	r = waitRun(t, s, id)
	if r.State != RunSucceeded || e.ncalls("test.spend") != 1 {
		t.Fatalf("approve: run = %s %q", r.State, r.Error)
	}
}

func TestDryRun(t *testing.T) {
	e := newEnv(t)
	e.write(t, "d.md", wf("d", `  - id: a
    agent: assistant
    task: "plan {{ inputs.x }}"
  - id: pay
    tool: test.spend
    with: { amount: 5 }
  - id: ok
    approve: Pay?
  - id: q
    ask: Which?
    choices: [first, second]
  - id: read
    tool: test.echo
    with: { a: "{{ steps.a.output }}", q: "{{ steps.q.output }}" }
`))
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "d", DryRun: true, Inputs: map[string]any{"x": "y"}}))
	if r.State != RunSucceeded || !r.DryRun {
		t.Fatalf("run = %s %q", r.State, r.Error)
	}
	if len(e.tasks.requests()) != 0 || e.ncalls("test.spend") != 0 || len(e.broker.m) != 0 {
		t.Error("dry run executed side effects")
	}
	for _, p := range []string{"a", "pay", "ok", "q"} {
		if st := stepOf(r, p); st.State != StepDryRun || st.Detail == "" {
			t.Errorf("%s = %+v", p, st)
		}
	}
	if !strings.Contains(stepOf(r, "a").Detail, `"task":"plan y"`) || !strings.Contains(stepOf(r, "pay").Detail, `"amount":5`) {
		t.Errorf("details: %s / %s", stepOf(r, "a").Detail, stepOf(r, "pay").Detail)
	}
	if in := e.lastInput(t, "test.echo"); in["a"] != "[dry run: agent assistant]" || in["q"] != "first" {
		t.Errorf("read step input = %v", in)
	}
}

func TestAskAnswer(t *testing.T) {
	e := newEnv(t)
	e.write(t, "a.md", wf("a", `  - id: q
    ask: "Sub for {{ inputs.item }}?"
    choices: [yes, no]
  - id: echo
    tool: test.echo
    with: { answer: "{{ steps.q.output }}" }
`))
	s := e.service(t)
	id := start(t, s, StartOptions{WorkflowID: "a", Inputs: map[string]any{"item": "milk"}})
	var q *nexusv1.Question
	eventually(t, "question", func() bool {
		for _, ev := range e.bus.Events() {
			if ev.GetQuestion() != nil {
				q = ev.GetQuestion()
				return true
			}
		}
		return false
	})
	if q.Text != "Sub for milk?" || len(q.Choices) != 2 || !s.OwnsQuestion(context.Background(), q.QuestionId) {
		t.Fatalf("question = %v", q)
	}
	if err := s.Answer(context.Background(), "q_unknown", "yes", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown question: %v", err)
	}
	if err := s.Answer(context.Background(), q.QuestionId, "no", ""); err != nil {
		t.Fatal(err)
	}
	r := waitRun(t, s, id)
	if r.State != RunSucceeded || r.Output.(map[string]any)["answer"] != "no" {
		t.Fatalf("run = %s %v %q", r.State, r.Output, r.Error)
	}
}

func TestWaitAndSubWorkflow(t *testing.T) {
	e := newEnv(t)
	e.write(t, "child.md", `---
type: workflow
id: child
inputs:
  who: { type: text, required: true }
steps:
  - id: pause
    wait: 20ms
  - id: hello
    tool: test.echo
    with: { msg: "hi {{ inputs.who }}" }
---
`)
	e.write(t, "parent.md", wf("parent", `  - id: call
    workflow: child
    inputs: { who: "{{ inputs.name }}" }
  - id: out
    tool: test.echo
    with: { got: "{{ steps.call.output.msg }}", inner: "{{ steps.call.result.hello.state }}" }
`))
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "parent", Inputs: map[string]any{"name": "Ana"}}))
	if r.State != RunSucceeded {
		t.Fatalf("run = %s %q", r.State, r.Error)
	}
	if out := r.Output.(map[string]any); out["got"] != "hi Ana" || out["inner"] != "succeeded" {
		t.Errorf("output = %v", out)
	}
	child := stepOf(r, "call").ChildRunID
	cr, err := s.GetRun(context.Background(), child)
	if err != nil || cr.ParentRunID != r.ID || cr.State != RunSucceeded {
		t.Errorf("child = %+v %v", cr, err)
	}
	if _, err := s.Start(context.Background(), "child", nil, ""); err == nil || !strings.Contains(err.Error(), "who") {
		t.Errorf("missing required input: %v", err)
	}
}

func TestResumeAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.write(t, "r.md", wf("r", `  - id: first
    tool: test.echo
    with: { n: 1 }
  - id: agent
    agent: assistant
    task: work
  - id: last
    tool: test.echo
    with: { got: "{{ steps.agent.output }}", first: "{{ steps.first.output.n }}" }
`))
	e.tasks.hold = true
	s1, err := NewService(e.h, e.st, Config{PollInterval: 5 * time.Millisecond}, func() string { return e.dir })
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Run(); err != nil {
		t.Fatal(err)
	}
	id := start(t, s1, StartOptions{WorkflowID: "r"})
	eventually(t, "agent step waiting", func() bool {
		st, _ := s1.st.getStep(context.Background(), id, "agent")
		return st != nil && st.State == StepWaiting && st.TaskID != ""
	})
	if err := s1.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := s1.GetRun(context.Background(), id); r.State != RunRunning {
		t.Fatalf("after stop: %s", r.State)
	}

	e.tasks.release()
	s2 := e.service(t)
	r := waitRun(t, s2, id)
	if r.State != RunSucceeded {
		t.Fatalf("resumed run = %s %q", r.State, r.Error)
	}
	if n := len(e.tasks.requests()); n != 1 {
		t.Errorf("tasks created = %d, want 1", n)
	}
	if e.ncalls("test.echo") != 2 {
		t.Errorf("echo calls = %d, want 2 (first step must not re-run)", e.ncalls("test.echo"))
	}
	if out := r.Output.(map[string]any); out["got"] != "done:assistant" || out["first"] != float64(1) {
		t.Errorf("output = %v", out)
	}
}

func TestExternalToolInterruptedThenRetry(t *testing.T) {
	e := newEnv(t)
	e.write(t, "x.md", wf("x", `  - id: send
    tool: test.external
    with: { to: "a@b.c" }
`))
	e.blockExt = true
	s1, err := NewService(e.h, e.st, Config{PollInterval: 5 * time.Millisecond}, func() string { return e.dir })
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Run(); err != nil {
		t.Fatal(err)
	}
	id := start(t, s1, StartOptions{WorkflowID: "x"})
	eventually(t, "external call", func() bool { return e.ncalls("test.external") == 1 })
	_ = s1.Stop(context.Background())

	e.mu.Lock()
	e.blockExt = false
	e.mu.Unlock()
	s2 := e.service(t)
	r := waitRun(t, s2, id)
	if r.State != RunFailed || !strings.Contains(r.Error, "outcome is unknown") || e.ncalls("test.external") != 1 {
		t.Fatalf("interrupted run = %s %q (calls %d)", r.State, r.Error, e.ncalls("test.external"))
	}
	if err := s2.Retry(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s2, id)
	if r.State != RunSucceeded || e.ncalls("test.external") != 2 {
		t.Fatalf("retried run = %s %q", r.State, r.Error)
	}
	e.mu.Lock()
	k1, k2 := e.calls["test.external"][0].ID, e.calls["test.external"][1].ID
	e.mu.Unlock()
	if k1 == "" || k1 != k2 {
		t.Errorf("idempotency keys %q / %q", k1, k2)
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t)
	e.tasks.hold = true
	e.write(t, "c.md", wf("c", `  - id: a
    agent: assistant
    task: long
`))
	s := e.service(t)
	id := start(t, s, StartOptions{WorkflowID: "c"})
	eventually(t, "task", func() bool { return len(e.tasks.requests()) == 1 })
	eventually(t, "waiting", func() bool {
		st, _ := s.st.getStep(context.Background(), id, "a")
		return st != nil && st.TaskID != ""
	})
	if err := s.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	r := waitRun(t, s, id)
	if r.State != RunCancelled || stepOf(r, "a").State != StepCancelled {
		t.Fatalf("run = %s, step = %+v", r.State, stepOf(r, "a"))
	}
	e.tasks.mu.Lock()
	n := len(e.tasks.canceled)
	e.tasks.mu.Unlock()
	if n != 1 {
		t.Errorf("agent task not cancelled")
	}
}

func TestRetriesAndTimeout(t *testing.T) {
	e := newEnv(t)
	e.tasks.hold = true
	e.write(t, "t.md", wf("t", `  - id: slow
    agent: assistant
    task: long
    timeout: 30ms
`))
	e.write(t, "f.md", wf("f", `  - id: flaky
    tool: test.fail
    retries: 1
`))
	s := e.service(t)
	r := waitRun(t, s, start(t, s, StartOptions{WorkflowID: "t"}))
	if r.State != RunFailed || !strings.Contains(r.Error, "timed out") {
		t.Fatalf("timeout run = %s %q", r.State, r.Error)
	}
	r = waitRun(t, s, start(t, s, StartOptions{WorkflowID: "f"}))
	if r.State != RunFailed || e.ncalls("test.fail") != 2 || stepOf(r, "flaky").Attempt != 2 {
		t.Fatalf("retry run = %s, calls %d, step %+v", r.State, e.ncalls("test.fail"), stepOf(r, "flaky"))
	}
}

func TestTriggersAndIntents(t *testing.T) {
	e := newEnv(t)
	e.copyTemplate(t, "server-watchdog.md")
	e.copyTemplate(t, "weekly-review.md")
	e.copyTemplate(t, "groceries.md")
	e.copyTemplate(t, "morning-brief.md")
	cron := triggers.NewCronSource(nil, nil)
	hooks := triggers.NewWebhookSource(0, nil)
	for _, src := range []kernel.TriggerSource{cron, hooks} {
		if err := e.h.Triggers().Register(src); err != nil {
			t.Fatal(err)
		}
	}
	e.write(t, "hook.md", `---
type: workflow
id: hook
triggers:
  - webhook: { id: deploy, secret_env: TEST_HOOK_SECRET }
inputs:
  ref: text
steps:
  - id: a
    tool: test.echo
    with: { ref: "{{ inputs.ref }}", src: "{{ trigger.source }}" }
---
`)
	s := e.service(t)

	keys := cron.Keys()
	if _, ok := keys["workflow:weekly-review#0"]; !ok {
		t.Errorf("weekly-review cron not registered: %v", keys)
	}
	if _, ok := keys["workflow:morning-brief#0"]; ok {
		t.Error("disabled morning-brief registered")
	}

	// Event firing → server-watchdog with inputs from the event.
	ev := &nexusv1.Event{Seq: 7, Body: &nexusv1.Event_NodeStatus{NodeStatus: &nexusv1.NodeStatus{NodeId: "mac-mini"}}}
	es := triggers.NewEventSource(nil, nil)
	for _, f := range es.Firings(ev) {
		s.OnFiring(context.Background(), f)
		s.OnFiring(context.Background(), f) // duplicate delivery → same run
	}
	runs, _ := s.ListRuns(context.Background(), "server-watchdog", 10)
	if len(runs) != 1 {
		t.Fatalf("watchdog runs = %d", len(runs))
	}
	r := waitRun(t, s, runs[0].ID)
	if r.State != RunSucceeded || r.Inputs["node"] != "mac-mini" || r.Source != "trigger:event" {
		t.Fatalf("watchdog run = %+v", r)
	}
	reqs := e.tasks.requests()
	if !strings.Contains(reqs[len(reqs)-1].Prompt, "Alert: node offline on node mac-mini.") {
		t.Errorf("ops prompt = %q", reqs[len(reqs)-1].Prompt)
	}
	if in := e.lastInput(t, "notify"); in["title"] != "Watchdog: mac-mini — node offline" || in["kind"] != "alert" {
		t.Errorf("notify = %v", in)
	}

	// Webhook firing → payload keys matching inputs become inputs.
	s.OnFiring(context.Background(), kernel.Firing{Source: "webhook", Key: "deploy", Payload: map[string]any{"ref": "main"}})
	runs, _ = s.ListRuns(context.Background(), "hook", 10)
	if len(runs) != 1 {
		t.Fatalf("hook runs = %d", len(runs))
	}
	r = waitRun(t, s, runs[0].ID)
	if out := r.Output.(map[string]any); out["ref"] != "main" || out["src"] != "webhook" {
		t.Errorf("hook output = %v", out)
	}

	// Paused triggers drop firings.
	s.PauseTriggers(true)
	s.OnFiring(context.Background(), kernel.Firing{Source: "webhook", Key: "deploy", Payload: map[string]any{"ref": "x"}})
	if runs, _ = s.ListRuns(context.Background(), "hook", 10); len(runs) != 1 {
		t.Error("paused trigger started a run")
	}
	s.PauseTriggers(false)

	// Intents.
	cases := []struct {
		in   string
		want string
		min  float64
	}{
		{"weekly review", "weekly-review", 1},
		{"Buy groceries: milk, eggs", "groceries", 0.9},
		{"can you check the servers please", "server-watchdog", 0.8},
		{"what's my day", "", 0}, // morning-brief is disabled
	}
	for _, c := range cases {
		id, score := s.MatchIntent(c.in)
		if id != c.want || score < c.min {
			t.Errorf("MatchIntent(%q) = %q %.2f, want %q ≥ %.2f", c.in, id, score, c.want, c.min)
		}
	}
	if in := s.IntentInputs("groceries", "Buy groceries: milk, eggs"); in["list"] != "milk, eggs" {
		t.Errorf("IntentInputs = %v", in)
	}
}

func TestCatalogReload(t *testing.T) {
	e := newEnv(t)
	s := e.service(t)
	if l, _ := s.List(context.Background()); len(l) != 0 {
		t.Fatal("expected empty catalog")
	}
	e.write(t, "n.md", wf("n", "  - id: a\n    tool: test.echo\n"))
	eventually(t, "reload", func() bool { _, err := s.Get(context.Background(), "n"); return err == nil })
	e.write(t, "bad.md", "---\ntype: workflow\nid: bad\nsteps: []\n---\n")
	eventually(t, "reload bad", func() bool { w, err := s.Get(context.Background(), "bad"); return err == nil && !w.Valid() })
	if _, err := s.Start(context.Background(), "bad", nil, ""); err == nil {
		t.Error("invalid workflow started")
	}
	if _, err := s.Start(context.Background(), "nope", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown workflow: %v", err)
	}
}

func TestHubTools(t *testing.T) {
	e := newEnv(t)
	e.write(t, "n.md", wf("n", "  - id: a\n    tool: test.echo\n    with: { v: \"{{ inputs.v }}\" }\n"))
	s := e.service(t)
	var run kernel.Tool
	for _, tl := range s.hubTools() {
		if tl.Spec().Name == "workflow.run" {
			run = tl
		}
	}
	res, err := run.Invoke(context.Background(), kernel.ToolCall{Input: json.RawMessage(`{"workflow_id":"n","inputs":{"v":"x"},"wait":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if out["state"] != RunSucceeded || out["output"].(map[string]any)["v"] != "x" {
		t.Errorf("workflow.run = %v", out)
	}
	res, err = s.statusTool().Invoke(context.Background(), kernel.ToolCall{})
	if err != nil || !strings.Contains(string(res.Output), "running_workflows") {
		t.Errorf("nexus.status = %s %v", res.Output, err)
	}
}
