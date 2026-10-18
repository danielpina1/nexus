package architect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// DraftInput starts a draft.
type DraftInput struct {
	Request       string
	Kind          nexusv1.DraftKind
	Context       string
	UpdateAgentID string
}

// Output is the JSON object the model / agent-architect answers with.
type Output struct {
	Kind             string     `json:"kind"`
	AgentMarkdown    string     `json:"agent_markdown"`
	WorkflowMarkdown string     `json:"workflow_markdown,omitempty"`
	TeamMarkdown     string     `json:"team_markdown,omitempty"`
	Rationale        string     `json:"rationale"`
	Risks            []string   `json:"risks"`
	TestCases        []TestCase `json:"test_cases"`
}

// TestCase is the JSON form of DraftTestCase.
type TestCase struct {
	Name   string   `json:"name"`
	Input  string   `json:"input"`
	Expect []string `json:"expect"`
}

// path reports which drafting path to use.
func (s *Service) path() (string, kernel.LLM) {
	llm, hasLLM := lookup[kernel.LLM](s.h, kernel.SvcLLM)
	switch s.cfg.Mode {
	case "llm":
		if hasLLM {
			return "llm", llm
		}
	case "agent":
		return "agent", nil
	default:
		if hasLLM && s.cfg.Model != "" {
			return "llm", llm
		}
	}
	return "agent", nil
}

func (s *Service) model() string {
	if s.cfg.Model != "" {
		return s.cfg.Model
	}
	return "best"
}

// StartDraft stores a new draft in DRAFTING and drafts it in the background.
func (s *Service) StartDraft(ctx context.Context, in DraftInput) (*nexusv1.AgentDraft, error) {
	in.Request = strings.TrimSpace(in.Request)
	if in.Request == "" && in.UpdateAgentID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request required"))
	}
	if in.Kind == nexusv1.DraftKind_DRAFT_KIND_UNSPECIFIED {
		in.Kind = nexusv1.DraftKind_DRAFT_KIND_AUTO
	}
	if in.UpdateAgentID != "" {
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			if _, _, _, err := cat.GetAgent(ctx, in.UpdateAgentID); err != nil {
				return nil, connect.NewError(connect.CodeNotFound, err)
			}
		}
		if in.Request == "" {
			in.Request = "Improve agent " + in.UpdateAgentID
		}
	}
	now := timestamppb.New(s.now())
	d := &nexusv1.AgentDraft{Id: newID(), Kind: in.Kind, State: stDrafting, Request: in.Request, Context: in.Context,
		UpdateAgentId: in.UpdateAgentID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	path, _ := s.path()
	if path == "llm" {
		d.Via = "llm:" + s.model()
	} else {
		d.Via = "agent:" + s.cfg.Agent
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			if _, _, _, err := cat.GetAgent(ctx, s.cfg.Agent); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("no model is configured for the architect (architect.model) and the %s agent is missing: add vault agents/%s.md from vault-template", s.cfg.Agent, s.cfg.Agent))
			}
		}
	}
	if err := s.insert(ctx, d); err != nil {
		return nil, err
	}
	s.runDraft(d.Id, s.prompt(ctx, d, nil))
	return proto.Clone(d).(*nexusv1.AgentDraft), nil
}

// StartRefine drafts a new revision of a draft with feedback.
func (s *Service) StartRefine(ctx context.Context, id, feedback string) (*nexusv1.AgentDraft, error) {
	if strings.TrimSpace(feedback) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("feedback required"))
	}
	var prev *nexusv1.AgentDraft
	d, err := s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
		switch d.State {
		case stDrafting, stTesting, stPending, stPublished, stDiscarded:
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("draft is %s", stateName(d.State)))
		}
		prev = proto.Clone(d).(*nexusv1.AgentDraft)
		d.Feedback = append(d.Feedback, feedback)
		d.Revision++
		d.State, d.Error, d.TestResults, d.TaskId = stDrafting, "", nil, ""
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.runDraft(d.Id, s.prompt(ctx, d, prev))
	return d, nil
}

// runDraft drafts in the background and stores the validated result.
func (s *Service) runDraft(id, prompt string) {
	s.goBG(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, s.cfg.DraftTimeout)
		defer cancel()
		path, llm := s.path()
		var out *Output
		var err error
		if path == "llm" {
			out, err = s.draftLLM(ctx, llm, prompt)
		} else {
			out, err = s.draftAgent(ctx, id, prompt)
		}
		d, uerr := s.update(context.Background(), id, func(d *nexusv1.AgentDraft) error {
			if d.State != stDrafting {
				return errors.New("draft no longer drafting")
			}
			if err != nil {
				d.State, d.Error = stFailed, err.Error()
				return nil
			}
			s.apply(context.Background(), d, out)
			return nil
		})
		if uerr != nil {
			s.log.Warn("architect: store draft", "draft", id, "err", uerr)
			return
		}
		switch d.State {
		case stReady:
			s.notice(context.Background(), d, "Agent draft ready: "+draftTitle(d), d.Rationale)
		case stInvalid:
			s.notice(context.Background(), d, "Agent draft needs changes: "+draftTitle(d), strings.Join(d.Problems, "\n"))
		case stFailed:
			s.notice(context.Background(), d, "Agent draft failed", d.Error)
		}
	})
}

func draftTitle(d *nexusv1.AgentDraft) string {
	for _, n := range d.Notes {
		if n.Id != "" {
			return n.Id
		}
	}
	return d.Id
}

// apply validates an output and stores it on the draft.
func (s *Service) apply(ctx context.Context, d *nexusv1.AgentDraft, out *Output) {
	var notes []*nexusv1.DraftNote
	if strings.TrimSpace(out.AgentMarkdown) != "" {
		notes = append(notes, &nexusv1.DraftNote{Kind: "agent", Markdown: out.AgentMarkdown})
	}
	if strings.TrimSpace(out.WorkflowMarkdown) != "" {
		notes = append(notes, &nexusv1.DraftNote{Kind: "workflow", Markdown: out.WorkflowMarkdown})
	}
	if strings.TrimSpace(out.TeamMarkdown) != "" {
		notes = append(notes, &nexusv1.DraftNote{Kind: "team", Markdown: out.TeamMarkdown})
	}
	res := Validate(s.BuildEnv(ctx, d.UpdateAgentId), notes)
	d.Notes, d.Fixes, d.Problems = res.Notes, res.Fixes, res.Problems
	d.Rationale, d.Risks = strings.TrimSpace(out.Rationale), out.Risks
	d.TestCases = nil
	for _, tc := range out.TestCases {
		if strings.TrimSpace(tc.Input) != "" {
			d.TestCases = append(d.TestCases, &nexusv1.DraftTestCase{Name: tc.Name, Input: tc.Input, Expect: tc.Expect})
		}
	}
	d.State = stReady
	if len(d.Problems) > 0 {
		d.State = stInvalid
	}
}

// ParseOutput extracts the draft JSON from a model answer (prose or code
// fences around it are tolerated).
func ParseOutput(text string) (*Output, error) {
	text = strings.TrimSpace(text)
	i, j := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if i < 0 || j <= i {
		return nil, errors.New("architect: the answer contains no JSON object")
	}
	var out Output
	if err := json.Unmarshal([]byte(text[i:j+1]), &out); err != nil {
		return nil, fmt.Errorf("architect: bad draft JSON: %w", err)
	}
	if strings.TrimSpace(out.AgentMarkdown+out.WorkflowMarkdown+out.TeamMarkdown) == "" {
		return nil, errors.New("architect: the draft has no note markdown")
	}
	return &out, nil
}

// ---------------------------------------------------------------- llm path

func (s *Service) draftLLM(ctx context.Context, llm kernel.LLM, prompt string) (*Output, error) {
	resp, err := llm.Chat(ctx, kernel.ChatRequest{Model: s.model(), JSON: true, MaxTokens: 6000, Messages: []kernel.ChatMessage{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: prompt},
	}})
	if err != nil {
		return nil, fmt.Errorf("architect: %s: %w", s.model(), err)
	}
	return ParseOutput(resp.Message.Content)
}

// ---------------------------------------------------------------- agent path

// draftAgent runs the agent-architect as a task and waits for its answer.
func (s *Service) draftAgent(ctx context.Context, draftID, prompt string) (*Output, error) {
	t, err := s.runTask(ctx, &nexusv1.CreateTaskRequest{Prompt: prompt, AgentId: s.cfg.Agent, Source: "architect",
		IdempotencyKey: "architect/" + draftID + "/" + fmt.Sprint(time.Now().UnixNano())},
		func(taskID string) {
			if _, err := s.update(context.Background(), draftID, func(d *nexusv1.AgentDraft) error { d.TaskId = taskID; return nil }); err != nil {
				s.log.Warn("architect: link task", "draft", draftID, "err", err)
			}
		})
	if err != nil {
		return nil, err
	}
	if t.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		return nil, fmt.Errorf("architect: %s task %s ended %s: %s", s.cfg.Agent, t.Id,
			strings.ToLower(strings.TrimPrefix(t.State.String(), "TASK_STATE_")), t.Summary)
	}
	if res := t.GetResult(); res != nil && len(res.GetFields()) > 0 {
		if b, err := json.Marshal(res.AsMap()); err == nil {
			if out, err := ParseOutput(string(b)); err == nil {
				return out, nil
			}
		}
	}
	return ParseOutput(t.Summary)
}

// runTask creates a task, reports its id, and waits until it is terminal.
func (s *Service) runTask(ctx context.Context, req *nexusv1.CreateTaskRequest, created func(id string)) (*nexusv1.Task, error) {
	te, ok := lookup[kernel.TaskEngine](s.h, kernel.SvcTasks)
	if !ok {
		return nil, errors.New("architect: the tasks module is not enabled")
	}
	var ch <-chan *nexusv1.Event
	bus, hasBus := lookup[kernel.EventBus](s.h, kernel.SvcEventBus)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if hasBus {
		since, _ := bus.LastSeq(ctx)
		c, err := bus.Subscribe(wctx, since, kernel.EventFilter{})
		if err == nil {
			ch = c
		}
	}
	t, err := te.CreateTask(ctx, req)
	if err != nil {
		return nil, err
	}
	if created != nil {
		created(t.Id)
	}
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for !terminal(t.State) {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("architect: waiting for task %s: %w", t.Id, ctx.Err())
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			if tu := ev.GetTaskUpdated().GetTask(); tu != nil && tu.Id == t.Id {
				t = tu
			}
		case <-poll.C:
			if nt, err := te.GetTask(ctx, t.Id); err == nil {
				t = nt
			}
		}
	}
	if nt, err := te.GetTask(ctx, t.Id); err == nil {
		t = nt
	}
	return t, nil
}

func terminal(s nexusv1.TaskState) bool {
	return s == nexusv1.TaskState_TASK_STATE_SUCCEEDED || s == nexusv1.TaskState_TASK_STATE_FAILED ||
		s == nexusv1.TaskState_TASK_STATE_CANCELLED
}

// ---------------------------------------------------------------- prompt

// SystemPrompt is used on the llm path (the agent path's instructions are
// the agent-architect note).
const SystemPrompt = `You are the Agent Architect of Nexus, a personal agent OS. You design excellent agents as vault notes.
An agent note is Markdown with YAML frontmatter: type: agent, id, name, description (what it does and when to use it —
the router reads it), examples (3-5 sample requests), runtime: auto, tier (simple|medium|heavy), node: {tags: [...]},
tools (ONLY from the provided tool list — the fewest that do the job), skills (only existing ones), limits
(max_runtime, max_cost_eur, max_spend_eur when it buys), policy (tighten only: spend: biometric, write_external: approve,
destructive: biometric), memory (reads; writes to a folder outside agents/, policies/, workflows/, shops/), optional
output (JSON Schema of a structured result). The body has: # Name, ## Goal, ## Playbook (numbered steps), ## Rules
(safety, untrusted web content, when to ask_user), ## Output, ## Lessons learned.
anthrex.* tools are for coding agents only: give them only to an agent with runtime anthrex or coding: true in its
claude:/codex: block, never to other agents.
For requests with a schedule or trigger ("every morning", "when an email arrives") also write a workflow note
(type: workflow, triggers, steps using the agent). For big multi-specialist work you may write a team note instead.
Answer with ONE JSON object only:
{"kind":"agent|team|workflow","agent_markdown":"...","workflow_markdown":"","team_markdown":"","rationale":"why this design",
 "risks":["..."],"test_cases":[{"name":"...","input":"a realistic request","expect":["contains: ...","no_calls: ..."]}]}
Expectations: "contains: x", "not_contains: x", "calls: tool", "no_calls: tool", "json", or free text.`

// prompt builds the request for the model / agent-architect.
func (s *Service) prompt(ctx context.Context, d *nexusv1.AgentDraft, prev *nexusv1.AgentDraft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Design request: %s\nKind: %s\n", d.Request, strings.ToLower(strings.TrimPrefix(d.Kind.String(), "DRAFT_KIND_")))
	if d.Context != "" {
		fmt.Fprintf(&b, "Context: %s\n", d.Context)
	}
	b.WriteString("\nAvailable tools (name — action class — description):\n")
	specs := s.h.Tools().List()
	for _, t := range specs {
		if strings.HasPrefix(t.Name, "architect.") {
			continue
		}
		fmt.Fprintf(&b, "- %s — %s — %s\n", t.Name, className(t.ActionClass), oneLine(t.Description, 140))
	}
	var node []string
	for n := range KnownNodeTools {
		node = append(node, n)
	}
	sort.Strings(node)
	fmt.Fprintf(&b, "Node tools (groups): %s (anthrex: coding agents only)\n", strings.Join(node, ", "))
	if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
		if list, err := cat.ListAgents(ctx, false); err == nil && len(list) > 0 {
			b.WriteString("\nExisting agents (do not duplicate; reuse ids only for an update):\n")
			for _, a := range list {
				fmt.Fprintf(&b, "- %s: %s\n", a.Id, oneLine(a.Description, 160))
			}
		}
	}
	if sc, ok := lookup[kernel.SkillCatalog](s.h, kernel.SvcSkills); ok {
		if list, err := sc.ListSkills(ctx, false); err == nil && len(list) > 0 {
			b.WriteString("\nSkills:\n")
			for _, sk := range list {
				fmt.Fprintf(&b, "- %s: %s\n", sk.Id, oneLine(sk.Description, 140))
			}
		}
	}
	b.WriteString("\nDefault policy: read auto, write_internal notify, write_external approve, code_push approve, spend biometric, destructive biometric. Agents may only tighten it.\n")
	if d.UpdateAgentId != "" {
		if cat, ok := lookup[kernel.AgentCatalog](s.h, kernel.SvcAgents); ok {
			if _, _, md, err := cat.GetAgent(ctx, d.UpdateAgentId); err == nil {
				fmt.Fprintf(&b, "\nThis is an UPDATE of agent %s (keep its id). Current note:\n```markdown\n%s\n```\n", d.UpdateAgentId, md)
			}
		}
	}
	if prev != nil {
		b.WriteString("\nPrevious revision:\n")
		for _, n := range prev.Notes {
			fmt.Fprintf(&b, "```markdown\n%s\n```\n", n.Markdown)
		}
		if len(prev.Problems) > 0 {
			fmt.Fprintf(&b, "Validation problems: %s\n", strings.Join(prev.Problems, "; "))
		}
		for _, tr := range prev.TestResults {
			if !tr.Passed {
				fmt.Fprintf(&b, "Failed test %q: %s\n", tr.Name, strings.Join(tr.Checks, "; "))
			}
		}
	}
	if len(d.Feedback) > 0 {
		fmt.Fprintf(&b, "\nOwner feedback (apply it): %s\n", strings.Join(d.Feedback, " / "))
	}
	b.WriteString("\nYou can check a note with the architect.validate tool before answering. Answer with the JSON object only.")
	return b.String()
}

func className(c nexusv1.ActionClass) string {
	if c == nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED {
		return "unspecified"
	}
	return strings.ToLower(strings.TrimPrefix(c.String(), "ACTION_CLASS_"))
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		s = string([]rune(s)[:n-1]) + "…"
	}
	return s
}

func structOf(m map[string]any) (*structpb.Struct, error) { return structpb.NewStruct(m) }
