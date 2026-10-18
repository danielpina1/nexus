package architect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"github.com/danielpina1/nexus/backend/kernel"
)

// ---------------------------------------------------------------- fakes

// catalog records UpsertAgent (the owner path) on top of taskstest.Catalog.
type catalog struct {
	*taskstest.Catalog
	mu      sync.Mutex
	upserts []string
}

func (c *catalog) UpsertAgent(_ context.Context, md, msg string) (*nexusv1.Agent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upserts = append(c.upserts, md)
	return &nexusv1.Agent{}, nil
}

func (c *catalog) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.upserts)
}

// vault records owner-path writes.
type vault struct {
	mu     sync.Mutex
	writes []string
}

func (v *vault) Read(context.Context, string) (*nexusv1.Note, error) { return nil, errors.New("no") }
func (v *vault) Write(_ context.Context, path, _, _ string) (*nexusv1.Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.writes = append(v.writes, path)
	return &nexusv1.Note{}, nil
}
func (v *vault) Search(context.Context, string, []string, int) ([]*nexusv1.SearchHit, error) {
	return nil, nil
}
func (v *vault) Graph(context.Context, *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error) {
	return nil, nil
}
func (v *vault) Capture(context.Context, *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error) {
	return nil, nil
}
func (v *vault) Root() string { return "" }

// engine is a fake task engine: every task finishes at once with answer.
type engine struct {
	mu     sync.Mutex
	tasks  map[string]*nexusv1.Task
	reqs   []*nexusv1.CreateTaskRequest
	answer func(req *nexusv1.CreateTaskRequest) (nexusv1.TaskState, string)
	bus    kernel.EventBus
}

func (e *engine) CreateTask(ctx context.Context, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error) {
	e.mu.Lock()
	e.reqs = append(e.reqs, req)
	id := fmt.Sprintf("tsk_%d", len(e.reqs))
	st, sum := e.answer(req)
	t := &nexusv1.Task{Id: id, AgentId: req.AgentId, Prompt: req.Prompt, State: nexusv1.TaskState_TASK_STATE_RUNNING}
	if e.tasks == nil {
		e.tasks = map[string]*nexusv1.Task{}
	}
	done := proto.Clone(t).(*nexusv1.Task)
	done.State, done.Summary = st, sum
	e.tasks[id] = done
	e.mu.Unlock()
	go func() { // finish asynchronously, like a real run
		time.Sleep(10 * time.Millisecond)
		_, _ = e.bus.Publish(context.Background(), &nexusv1.Event{TaskId: id, Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: done}}})
	}()
	return t, nil
}

func (e *engine) GetTask(_ context.Context, id string) (*nexusv1.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[id]; ok {
		return proto.Clone(t).(*nexusv1.Task), nil
	}
	return nil, errors.New("not found")
}
func (e *engine) ListTasks(context.Context, *nexusv1.ListTasksRequest) ([]*nexusv1.Task, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*nexusv1.Task
	for _, t := range e.tasks {
		out = append(out, proto.Clone(t).(*nexusv1.Task))
	}
	return out, "", nil
}
func (e *engine) ChildTasks(context.Context, string) ([]*nexusv1.Task, error)    { return nil, nil }
func (e *engine) SendMessage(context.Context, *nexusv1.SendMessageRequest) error { return nil }
func (e *engine) CancelTask(context.Context, string) error                       { return nil }
func (e *engine) EscalateTask(context.Context, string, string) (*nexusv1.Task, error) {
	return nil, nil
}
func (e *engine) StopAll(context.Context, bool) (int, error)                      { return 0, nil }
func (e *engine) HandleRunEvent(context.Context, string, *nexusv1.RunEvent) error { return nil }

const goodAgent = `---
type: agent
id: price-watcher
name: Price watcher
description: Watches product prices and tells the owner when they drop. Use for "tell me when X gets cheaper".
examples: ["tell me when the AirPods drop below 150", "watch the price of this drill"]
runtime: auto
tier: simple
tools: [web.fetch, memory.search, notify, email.send, made.up]
policy:
  spend: auto
memory:
  writes: agents/
---

# Price watcher

## Goal
Notify the owner when a watched price drops.

## Playbook
1. Fetch the page.
`

const goodWorkflow = `---
type: workflow
id: price-check
name: Daily price check
enabled: true
triggers:
  - cron: "0 9 * * *"
steps:
  - id: check
    agent: price-watcher
    task: Check the watched prices.
---
`

func output(agent, wf string) string {
	b, _ := json.Marshal(Output{Kind: "agent", AgentMarkdown: agent, WorkflowMarkdown: wf, Rationale: "small and safe",
		Risks:     []string{"sites block scraping"},
		TestCases: []TestCase{{Name: "drop", Input: "watch the price of the Bosch drill", Expect: []string{"contains: price", "no_calls: email.send", "be polite"}}}})
	return "Here is the draft:\n```json\n" + string(b) + "\n```"
}

type fixture struct {
	env    *taskstest.Env
	svc    *Service
	cat    *catalog
	eng    *engine
	broker *taskstest.Broker
}

func newFixture(t *testing.T, cfg string) *fixture {
	t.Helper()
	env := taskstest.New(t)
	env.Host.ConfigYAML = cfg
	cat := &catalog{Catalog: taskstest.NewCatalog(
		taskstest.Agent("agent-architect", "Agent Architect", "Designs agents", nexusv1.Tier_TIER_HEAVY),
		taskstest.Agent("researcher", "Researcher", "Researches questions", nexusv1.Tier_TIER_MEDIUM),
	)}
	env.Host.Provide(kernel.SvcAgents, kernel.AgentCatalog(cat))
	eng := &engine{bus: env.Bus, answer: func(*nexusv1.CreateTaskRequest) (nexusv1.TaskState, string) {
		return nexusv1.TaskState_TASK_STATE_SUCCEEDED, output(goodAgent, goodWorkflow)
	}}
	env.Host.Provide(kernel.SvcTasks, kernel.TaskEngine(eng))
	broker := &taskstest.Broker{}
	env.Host.Provide(kernel.SvcApprovals, kernel.ApprovalBroker(broker))
	reg := env.Host.Tools()
	for _, s := range []kernel.ToolSpec{
		{Name: "web.fetch", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
		{Name: "memory.search", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
		{Name: "notify", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL},
		{Name: "email.send", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL},
		{Name: "anthrex.run", ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE},
	} {
		spec := s
		_ = reg.Register(kernel.ToolFunc{S: spec, F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]any{"ok": spec.Name, "price": 129})
		}})
	}
	m := &Module{}
	if err := m.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Service.Close)
	return &fixture{env: env, svc: m.Service, cat: cat, eng: eng, broker: broker}
}

func (f *fixture) draft(t *testing.T, req string) *nexusv1.AgentDraft {
	t.Helper()
	resp, err := f.svc.Draft(context.Background(), connect.NewRequest(&nexusv1.DraftRequest{Request: req, Wait: true}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.Draft
}

// ---------------------------------------------------------------- tests

func TestDraftAgentPathValidatesAndAutoFixes(t *testing.T) {
	f := newFixture(t, "")
	d := f.draft(t, "tell me when prices drop")
	if d.State != stReady || d.Via != "agent:agent-architect" || d.TaskId == "" {
		t.Fatalf("draft %v via %s task %q problems %v err %q", d.State, d.Via, d.TaskId, d.Problems, d.Error)
	}
	if got := f.eng.reqs[0]; got.AgentId != "agent-architect" || !strings.Contains(got.Prompt, "web.fetch — read") {
		t.Fatalf("architect task: %s %q", got.AgentId, got.Prompt)
	}
	a := d.Notes[0]
	if a.Path != "agents/price-watcher.md" {
		t.Fatalf("path %s", a.Path)
	}
	for _, want := range []string{"spend: biometric", "write_external: approve", "episodes/price-watcher/", "max_runtime"} {
		if !strings.Contains(a.Markdown, want) {
			t.Fatalf("fixed note lacks %q:\n%s", want, a.Markdown)
		}
	}
	if strings.Contains(a.Markdown, "made.up") || strings.Contains(a.Markdown, "writes: agents/") {
		t.Fatalf("unknown tool / protected folder kept:\n%s", a.Markdown)
	}
	fixes := strings.Join(d.Fixes, "\n")
	for _, want := range []string{"removed unknown tool made.up", "looser than the default", "configuration folder"} {
		if !strings.Contains(fixes, want) {
			t.Fatalf("fixes lack %q: %s", want, fixes)
		}
	}
	if len(d.Notes) != 2 || d.Notes[1].Path != "workflows/price-check.md" || len(d.TestCases) != 1 {
		t.Fatalf("notes %v cases %v", d.Notes, d.TestCases)
	}
}

func TestDraftLLMPathAndDuplicateIDs(t *testing.T) {
	f := newFixture(t, "model: best\n")
	llm := &taskstest.LLM{Fn: func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
		md := strings.Replace(goodAgent, "id: price-watcher", "id: researcher", 1)
		return taskstest.Reply(output(md, "")), nil
	}}
	f.env.Host.Provide(kernel.SvcLLM, llm)
	d := f.draft(t, "a research agent")
	if d.Via != "llm:best" || len(f.eng.reqs) != 0 || llm.CallCount() != 1 {
		t.Fatalf("via %s tasks %d llm %d", d.Via, len(f.eng.reqs), llm.CallCount())
	}
	if d.Notes[0].Id != "researcher-2" || !strings.Contains(strings.Join(d.Fixes, " "), `agent "researcher" already exists`) {
		t.Fatalf("duplicate id not renamed: %s %v", d.Notes[0].Id, d.Fixes)
	}
}

func TestValidateRejectsCodeOnlyToolsForNonCodingAgents(t *testing.T) {
	env := &Env{Tools: map[string]kernel.ToolSpec{"anthrex.run": {Name: "anthrex.run"}, "notify": {Name: "notify"}},
		Agents: map[string]bool{}, Teams: map[string]bool{}, Workflows: map[string]bool{}}
	md := "---\nid: helper\nname: Helper\ndescription: Helps.\nruntime: auto\ntools: [notify, anthrex.run]\n---\n# Helper\nDo things.\n"
	res := Validate(env, []*nexusv1.DraftNote{{Kind: "agent", Markdown: md}})
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "reserved for coding agents") {
		t.Fatalf("problems %v", res.Problems)
	}
	// The same tool is fine on a coding agent (claude.coding: true or runtime anthrex).
	coding := strings.Replace(md, "runtime: auto", "runtime: auto\nclaude: {coding: true}", 1)
	if res := Validate(env, []*nexusv1.DraftNote{{Kind: "agent", Markdown: coding}}); len(res.Problems) != 0 {
		t.Fatalf("coding agent rejected: %v", res.Problems)
	}
	anth := strings.Replace(md, "runtime: auto", "runtime: anthrex", 1)
	if res := Validate(env, []*nexusv1.DraftNote{{Kind: "agent", Markdown: anth}}); len(res.Problems) != 0 {
		t.Fatalf("anthrex agent rejected: %v", res.Problems)
	}
}

func TestValidateProblems(t *testing.T) {
	env := &Env{Tools: map[string]kernel.ToolSpec{}, Agents: map[string]bool{}, Teams: map[string]bool{}, Workflows: map[string]bool{}}
	res := Validate(env, []*nexusv1.DraftNote{
		{Kind: "agent", Markdown: "---\nid: x\nname: X\n---\n"},
		{Kind: "workflow", Markdown: "---\nid: wf\nname: W\nsteps:\n  - id: a\n    agent: ghost\n    task: hi\n---\n"},
	})
	all := strings.Join(res.Problems, "\n")
	for _, want := range []string{"missing description", "instructions) is empty", `unknown agent "ghost"`} {
		if !strings.Contains(all, want) {
			t.Fatalf("problems lack %q:\n%s", want, all)
		}
	}
}

func TestPublishRequiresApproval(t *testing.T) {
	f := newFixture(t, "publish_level: biometric\n")
	d := f.draft(t, "tell me when prices drop")
	if _, err := f.svc.Publish(context.Background(), connect.NewRequest(&nexusv1.PublishRequest{DraftId: "drf_nope"})); err == nil {
		t.Fatal("unknown draft published")
	}
	resp, err := f.svc.Publish(context.Background(), connect.NewRequest(&nexusv1.PublishRequest{DraftId: d.Id}))
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Msg.Approval
	if resp.Msg.Draft.State != stPending || a.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC ||
		a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL {
		t.Fatalf("draft %v approval %v", resp.Msg.Draft.State, a)
	}
	notes := a.Payload.AsMap()["notes"].([]any)
	if len(notes) != 2 || !strings.Contains(notes[0].(map[string]any)["markdown"].(string), "Price watcher") {
		t.Fatalf("payload %v", a.Payload.AsMap())
	}
	if f.cat.count() != 0 {
		t.Fatal("written before approval")
	}
	// A denial writes nothing and returns the draft to ready.
	denied := proto.Clone(a).(*nexusv1.Approval)
	denied.State = nexusv1.ApprovalState_APPROVAL_STATE_DENIED
	_, _ = f.env.Bus.Publish(context.Background(), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: denied}})
	got := f.waitState(t, d.Id, stReady)
	if f.cat.count() != 0 || !strings.Contains(got.Error, "denied") {
		t.Fatalf("after denial: writes %d err %q", f.cat.count(), got.Error)
	}
	// Approve a new request: the notes are written through the owner path.
	resp, err = f.svc.Publish(context.Background(), connect.NewRequest(&nexusv1.PublishRequest{DraftId: d.Id}))
	if err != nil {
		t.Fatal(err)
	}
	ok := proto.Clone(resp.Msg.Approval).(*nexusv1.Approval)
	ok.State = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	// The old (denied) approval being approved later must not publish.
	stale := proto.Clone(a).(*nexusv1.Approval)
	stale.State = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	_, _ = f.env.Bus.Publish(context.Background(), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: stale}})
	_, _ = f.env.Bus.Publish(context.Background(), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: ok}})
	got = f.waitState(t, d.Id, stReady)
	if f.cat.count() != 0 || !strings.Contains(got.Error, "no vault") {
		t.Fatalf("partial publish without a vault: writes %d err %q", f.cat.count(), got.Error)
	}
	// With the vault service the approved notes are written.
	v := &vault{}
	f.env.Host.Provide(kernel.SvcVault, kernel.Vault(v))
	resp, err = f.svc.Publish(context.Background(), connect.NewRequest(&nexusv1.PublishRequest{DraftId: d.Id}))
	if err != nil {
		t.Fatal(err)
	}
	ok = proto.Clone(resp.Msg.Approval).(*nexusv1.Approval)
	ok.State = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	_, _ = f.env.Bus.Publish(context.Background(), &nexusv1.Event{Body: &nexusv1.Event_Approval{Approval: ok}})
	f.waitState(t, d.Id, stPublished)
	if f.cat.count() != 1 || len(v.writes) != 1 || v.writes[0] != "workflows/price-check.md" {
		t.Fatalf("agent upserts %d vault writes %v", f.cat.count(), v.writes)
	}
}

func (f *fixture) waitState(t *testing.T, id string, st nexusv1.DraftState) *nexusv1.AgentDraft {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		d, err := f.svc.load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == st {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("draft state %v, want %v (err %q)", d.State, st, d.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSandboxBlocksExternalActions(t *testing.T) {
	f := newFixture(t, "")
	d := f.draft(t, "tell me when prices drop")
	// Native loop: the model tries to email and to fetch; only the read runs.
	llm := &taskstest.LLM{}
	step := 0
	llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
		step++
		switch step {
		case 1:
			return &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", ToolCalls: []kernel.LLMToolCall{
				{ID: "1", Name: "email.send", Arguments: `{"to":"x@y.z"}`},
				{ID: "2", Name: "web.fetch", Arguments: `{"url":"https://shop"}`},
			}}}, nil
		default:
			var sawBlocked, sawFetch bool
			for _, m := range req.Messages {
				sawBlocked = sawBlocked || strings.Contains(m.Content, `"sandbox":"blocked"`)
				sawFetch = sawFetch || strings.Contains(m.Content, `"price":129`)
			}
			if !sawBlocked || !sawFetch {
				return nil, errors.New("tool results missing")
			}
			return taskstest.Reply("The price is 129, below your target."), nil
		}
	}
	f.env.Host.Provide(kernel.SvcLLM, llm)
	resp, err := f.svc.TestRun(context.Background(), connect.NewRequest(&nexusv1.TestRunRequest{DraftId: d.Id, Wait: true}))
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Msg.Draft.TestResults[0]
	if r.Mode != "native_loop" || r.Error != "" {
		t.Fatalf("result %+v", r)
	}
	if !contains(r.BlockedActions, "email.send") || contains(r.BlockedActions, "web.fetch") {
		t.Fatalf("blocked %v", r.BlockedActions)
	}
	// email.send was attempted, so "no_calls: email.send" fails the case.
	if r.Passed || !strings.Contains(strings.Join(r.Checks, "\n"), "✗ no_calls: email.send") ||
		!strings.Contains(strings.Join(r.Checks, "\n"), "✓ contains: price") || !strings.Contains(strings.Join(r.Checks, "\n"), "? be polite") {
		t.Fatalf("checks %v passed %v", r.Checks, r.Passed)
	}
	if resp.Msg.Draft.State != stReady {
		t.Fatalf("state after test %v", resp.Msg.Draft.State)
	}
	// SandboxAgent strips anything above compute and forces biometric.
	sa := SandboxAgent(&nexusv1.Agent{Id: "x", Tools: []string{"web.fetch", "email.send", "notify"}},
		map[string]kernel.ToolSpec{"web.fetch": {ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
			"email.send": {ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL},
			"notify":     {ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL}})
	if len(sa.Tools) != 1 || sa.Policy["spend"] != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC || sa.Id != "sandbox-x" {
		t.Fatalf("sandbox agent %v", sa)
	}
}

func TestSimulatedTestRunWithoutLLM(t *testing.T) {
	f := newFixture(t, "")
	d := f.draft(t, "tell me when prices drop")
	f.eng.answer = func(req *nexusv1.CreateTaskRequest) (nexusv1.TaskState, string) {
		return nexusv1.TaskState_TASK_STATE_SUCCEEDED, "I would call web.fetch, then report the price."
	}
	resp, err := f.svc.TestRun(context.Background(), connect.NewRequest(&nexusv1.TestRunRequest{DraftId: d.Id, Wait: true}))
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Msg.Draft.TestResults[0]
	last := f.eng.reqs[len(f.eng.reqs)-1]
	if r.Mode != "simulated" || !strings.Contains(last.Prompt, SandboxMarker) || last.AgentId != "agent-architect" || !r.Passed {
		t.Fatalf("result %+v prompt %q", r, last.Prompt)
	}
}

func TestRefineAndTune(t *testing.T) {
	f := newFixture(t, "")
	d := f.draft(t, "tell me when prices drop")
	resp, err := f.svc.Refine(context.Background(), connect.NewRequest(&nexusv1.RefineRequest{DraftId: d.Id, Feedback: "also track Amazon", Wait: true}))
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Msg.Draft; r.Revision != 2 || r.State != stReady || !strings.Contains(f.eng.reqs[1].Prompt, "also track Amazon") ||
		!strings.Contains(f.eng.reqs[1].Prompt, "Previous revision") {
		t.Fatalf("refine: rev %d state %v", r.Revision, r.State)
	}
	// Tune drafts an update for an agent with many failures.
	f.eng.mu.Lock()
	for i := 0; i < 4; i++ {
		st := nexusv1.TaskState_TASK_STATE_FAILED
		if i == 3 {
			st = nexusv1.TaskState_TASK_STATE_SUCCEEDED
		}
		f.eng.tasks[fmt.Sprintf("r%d", i)] = &nexusv1.Task{Id: fmt.Sprintf("r%d", i), AgentId: "researcher", State: st, Summary: "site blocked"}
	}
	f.eng.mu.Unlock()
	rep, err := f.svc.Tune(context.Background(), 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drafts) != 1 || rep.Drafts[0].AgentID != "researcher" {
		t.Fatalf("tune %+v", rep)
	}
	got := f.waitNotDrafting(t, rep.Drafts[0].DraftID)
	if got.UpdateAgentId != "researcher" {
		t.Fatalf("update id %q", got.UpdateAgentId)
	}
	// A second review does not open another draft for the same agent.
	if rep, _ := f.svc.Tune(context.Background(), 7, 3); len(rep.Drafts) != 0 {
		t.Fatalf("duplicate tune draft %+v", rep)
	}
}

func (f *fixture) waitNotDrafting(t *testing.T, id string) *nexusv1.AgentDraft {
	t.Helper()
	d, err := f.svc.waitFor(context.Background(), id, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDraftFromCommandAndMissingArchitect(t *testing.T) {
	f := newFixture(t, "agent: nobody\n")
	if _, err := f.svc.DraftFromCommand(context.Background(), "water my plants", nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("want failed precondition without the architect agent, got %v", err)
	}
	f2 := newFixture(t, "")
	d, err := f2.svc.DraftFromCommand(context.Background(), "water my plants", &nexusv1.CommandContext{Source: "siri"})
	if err != nil || d.State != stDrafting || !strings.Contains(d.Context, "siri") {
		t.Fatalf("draft %v err %v", d, err)
	}
}

func TestParseOutput(t *testing.T) {
	if _, err := ParseOutput("no json here"); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseOutput(`{"kind":"agent"}`); err == nil {
		t.Fatal("want error for empty notes")
	}
	o, err := ParseOutput(output(goodAgent, ""))
	if err != nil || !strings.Contains(o.AgentMarkdown, "price-watcher") {
		t.Fatalf("%v %v", o, err)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestTemplateNotesParse(t *testing.T) {
	b, err := os.ReadFile("../../../vault-template/agents/agent-architect.md")
	if err != nil {
		t.Fatal(err)
	}
	a, instr := agentdef.Parse("agents/agent-architect.md", b)
	if len(a.Errors) > 0 || instr == "" || a.Id != DefaultAgent {
		t.Fatalf("agent-architect note: %v", a.Errors)
	}
	for _, tool := range a.Tools {
		if tool == "architect.tune" {
			t.Fatal("the architect must not be able to start drafts itself")
		}
	}
	b, err = os.ReadFile("../../../vault-template/workflows/agent-tuning.md")
	if err != nil {
		t.Fatal(err)
	}
	if w := workflow.Parse("workflows/agent-tuning.md", b); !w.Valid() {
		t.Fatalf("agent-tuning workflow: %v", w.Errors)
	}
}
