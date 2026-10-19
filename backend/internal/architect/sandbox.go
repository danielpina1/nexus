package architect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/kernel"
)

// SandboxMarker is put in the params of sandbox tasks and the prompt of
// simulated runs.
const SandboxMarker = "architect_sandbox"

// SandboxTaskCreator is the optional task-engine capability for running an
// ephemeral (unpublished) agent definition as a sandboxed task: a real task
// on a real runtime under a strict policy enforced by the hub (the tasks
// engine implements it, internal/tasks/sandbox.go). TestRun prefers it; without
// it TestRun uses the in-process loop or a simulation.
type SandboxTaskCreator interface {
	CreateSandboxTask(ctx context.Context, agent *nexusv1.Agent, instructions string, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error)
}

// SandboxReporter is the optional companion of SandboxTaskCreator: the tool
// calls a sandbox task made and the actions the hub refused (removed tools
// and every gated action it auto-denied), for the test results.
type SandboxReporter interface {
	SandboxReport(ctx context.Context, taskID string) (calls, blocked []string, err error)
}

// sandboxAllowed reports whether a tool class may really run in the
// sandbox: reads and computation only. Anything that writes (vault,
// external services), spends, destroys or controls devices is refused.
func sandboxAllowed(c nexusv1.ActionClass) bool {
	return c == nexusv1.ActionClass_ACTION_CLASS_READ || c == nexusv1.ActionClass_ACTION_CLASS_COMPUTE
}

// SandboxAgent returns a copy of a drafted agent fit for a sandbox run:
// only read/compute tools, and every class at or above write_external at
// biometric (so nothing proceeds without the owner).
func SandboxAgent(a *nexusv1.Agent, tools map[string]kernel.ToolSpec) *nexusv1.Agent {
	c := proto.Clone(a).(*nexusv1.Agent)
	c.Id = "sandbox-" + a.Id
	c.Tools = nil
	for _, t := range a.Tools {
		if spec, ok := tools[t]; ok && sandboxAllowed(spec.ActionClass) {
			c.Tools = append(c.Tools, t)
		}
	}
	if c.Policy == nil {
		c.Policy = map[string]nexusv1.TrustLevel{}
	}
	for _, k := range []string{"write_internal", "write_external", "code_push", "spend", "destructive", "device_control"} {
		c.Policy[k] = nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC
	}
	return c
}

// StartTestRun runs the draft's test cases in the background.
func (s *Service) StartTestRun(ctx context.Context, id string, extra []*nexusv1.DraftTestCase) (*nexusv1.AgentDraft, error) {
	d, err := s.update(ctx, id, func(d *nexusv1.AgentDraft) error {
		if d.State != stReady && d.State != stInvalid {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("draft is %s", stateName(d.State)))
		}
		if agentNote(d) == nil {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("the draft has no agent note to test"))
		}
		if len(extra) > 0 {
			d.TestCases = append(d.TestCases, extra...)
		}
		if len(d.TestCases) == 0 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("no test cases"))
		}
		d.State, d.TestResults = stTesting, nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	prevState := stReady
	if len(d.Problems) > 0 {
		prevState = stInvalid
	}
	snapshot := proto.Clone(d).(*nexusv1.AgentDraft)
	s.goBG(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, s.cfg.TestTimeout)
		defer cancel()
		var results []*nexusv1.DraftTestResult
		for _, tc := range snapshot.TestCases {
			results = append(results, s.runCase(ctx, snapshot, tc))
		}
		if _, err := s.update(context.Background(), id, func(d *nexusv1.AgentDraft) error {
			d.TestResults, d.State = results, prevState
			return nil
		}); err != nil {
			s.log.Warn("architect: store test results", "draft", id, "err", err)
		}
	})
	return d, nil
}

func agentNote(d *nexusv1.AgentDraft) *nexusv1.DraftNote {
	for _, n := range d.Notes {
		if n.Kind == "agent" {
			return n
		}
	}
	return nil
}

// runCase runs one test case in the best available sandbox.
func (s *Service) runCase(ctx context.Context, d *nexusv1.AgentDraft, tc *nexusv1.DraftTestCase) *nexusv1.DraftTestResult {
	n := agentNote(d)
	a, instr := agentdef.Parse(n.Path, []byte(n.Markdown))
	tools := map[string]kernel.ToolSpec{}
	for _, t := range s.h.Tools().List() {
		tools[t.Name] = t
	}
	r := &nexusv1.DraftTestResult{Name: tc.Name, Input: tc.Input}
	if r.Name == "" {
		r.Name = oneLine(tc.Input, 40)
	}
	for _, t := range a.Tools {
		if spec, ok := tools[t]; !ok || !sandboxAllowed(spec.ActionClass) {
			r.BlockedActions = append(r.BlockedActions, t)
		}
	}
	var out string
	var calls []string
	var err error
	te, hasTasks := lookup[kernel.TaskEngine](s.h, kernel.SvcTasks)
	llm, hasLLM := lookup[kernel.LLM](s.h, kernel.SvcLLM)
	if sc, ok := te.(SandboxTaskCreator); hasTasks && ok {
		r.Mode = "sandbox_task"
		var t *nexusv1.Task
		t, err = s.sandboxTask(ctx, sc, SandboxAgent(a, tools), instr, tc.Input)
		if t != nil {
			r.TaskId, out = t.Id, t.Summary
			if rep, ok := te.(SandboxReporter); ok {
				if c, b, rerr := rep.SandboxReport(context.WithoutCancel(ctx), t.Id); rerr == nil {
					calls = c
					r.BlockedActions = mergeUnique(r.BlockedActions, b)
				}
			}
			if err == nil && t.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
				err = fmt.Errorf("sandbox task ended %s: %s", strings.ToLower(strings.TrimPrefix(t.State.String(), "TASK_STATE_")), t.Summary)
			}
		}
	} else if hasLLM {
		r.Mode = "native_loop"
		var blocked []string
		out, calls, blocked, err = s.nativeLoop(ctx, llm, a, instr, tc.Input)
		r.BlockedActions = mergeUnique(r.BlockedActions, blocked)
	} else if hasTasks {
		r.Mode = "simulated"
		var t *nexusv1.Task
		t, err = s.runTask(ctx, &nexusv1.CreateTaskRequest{AgentId: s.cfg.Agent, Source: "architect", Prompt: simulatePrompt(n.Markdown, tc.Input)}, nil)
		if t != nil {
			r.TaskId, out = t.Id, t.Summary
			if err == nil && t.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
				err = fmt.Errorf("simulation task ended %s", strings.ToLower(strings.TrimPrefix(t.State.String(), "TASK_STATE_")))
			}
		}
	} else {
		err = errors.New("no sandbox available (needs the llm or tasks module)")
	}
	r.Output, r.ToolCalls = out, calls
	if err != nil {
		r.Error = err.Error()
	}
	r.Checks, r.Passed = Check(tc.Expect, out, calls)
	if err != nil {
		r.Passed = false
	}
	return r
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(a, b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) sandboxTask(ctx context.Context, sc SandboxTaskCreator, a *nexusv1.Agent, instr, input string) (*nexusv1.Task, error) {
	st, _ := structOf(map[string]any{SandboxMarker: true})
	t, err := sc.CreateSandboxTask(ctx, a, instr+"\n\n(Sandbox test run: actions that change anything are refused; describe what you would do.)",
		&nexusv1.CreateTaskRequest{Prompt: input, Source: "architect", Params: st})
	if err != nil {
		return nil, err
	}
	te, _ := lookup[kernel.TaskEngine](s.h, kernel.SvcTasks)
	for !terminal(t.State) {
		select {
		case <-ctx.Done():
			_ = te.CancelTask(context.Background(), t.Id)
			return t, ctx.Err()
		case <-sleepCh():
		}
		if nt, err := te.GetTask(ctx, t.Id); err == nil {
			t = nt
		}
	}
	return t, nil
}

func simulatePrompt(markdown, input string) string {
	return "SANDBOX SIMULATION (" + SandboxMarker + "). Do not call any tool that changes anything. " +
		"Act as the agent defined by this draft note and show, step by step, which tools you would call with which inputs, " +
		"then the final answer you would give.\n\nDraft note:\n```markdown\n" + markdown + "\n```\n\nUser request: " + input
}

// nativeLoop runs the agent in-process on a model with sandboxed tools:
// read/compute hub tools really run; everything else is refused and
// recorded as blocked.
func (s *Service) nativeLoop(ctx context.Context, llm kernel.LLM, a *nexusv1.Agent, instr, input string) (string, []string, []string, error) {
	var specs []kernel.ToolSpec
	reg := s.h.Tools()
	for _, spec := range reg.List() {
		if kernel.ToolAllowed(a.Tools, spec.Name) {
			specs = append(specs, spec)
		}
	}
	model := s.cfg.TestModel
	if model == "" {
		model = s.cfg.Model
	}
	if model == "" {
		model = "smart"
	}
	msgs := []kernel.ChatMessage{
		{Role: "system", Content: instr + "\n\nThis is a sandbox test run: tools that write, send, spend, delete or control devices are refused."},
		{Role: "user", Content: input},
	}
	var calls, blocked []string
	for step := 0; step < 8; step++ {
		resp, err := llm.Chat(ctx, kernel.ChatRequest{Model: model, Messages: msgs, Tools: specs, MaxTokens: 1500})
		if err != nil {
			return "", calls, blocked, err
		}
		m := resp.Message
		if len(m.ToolCalls) == 0 {
			return m.Content, calls, blocked, nil
		}
		msgs = append(msgs, kernel.ChatMessage{Role: "assistant", Content: m.Content, ToolCalls: m.ToolCalls})
		for _, tc := range m.ToolCalls {
			calls = append(calls, tc.Name)
			result := s.sandboxCall(ctx, a, tc, &blocked)
			msgs = append(msgs, kernel.ChatMessage{Role: "tool", ToolCallID: tc.ID, Name: tc.Name, Content: result})
		}
	}
	return "", calls, blocked, errors.New("sandbox: step limit reached")
}

func (s *Service) sandboxCall(ctx context.Context, a *nexusv1.Agent, tc kernel.LLMToolCall, blocked *[]string) string {
	t, ok := s.h.Tools().Get(tc.Name)
	if !ok || !kernel.ToolAllowed(a.Tools, tc.Name) {
		*blocked = mergeUnique(*blocked, []string{tc.Name})
		return `{"error":"tool not available in the sandbox"}`
	}
	if !sandboxAllowed(t.Spec().ActionClass) {
		*blocked = mergeUnique(*blocked, []string{tc.Name})
		b, _ := json.Marshal(map[string]string{"sandbox": "blocked",
			"message": fmt.Sprintf("%s (%s) is not executed in a sandbox test run; assume it would succeed", tc.Name, className(t.Spec().ActionClass))})
		return string(b)
	}
	res, err := t.Invoke(ctx, kernel.ToolCall{ID: tc.ID, AgentID: "sandbox-" + a.Id, Input: json.RawMessage(nonEmptyJSON(tc.Arguments))})
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	out := string(res.Output)
	if len(out) > 8000 {
		out = out[:8000]
	}
	return out
}

func nonEmptyJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

// Check evaluates test expectations against an output and the tool calls.
// Free-text expectations are reported as "? <text>" (for the owner) and do
// not fail the case.
func Check(expect []string, output string, calls []string) ([]string, bool) {
	var out []string
	pass := true
	lower := strings.ToLower(output)
	called := func(t string) bool {
		for _, c := range calls {
			if c == t || strings.HasPrefix(c, strings.TrimSuffix(t, "*")) && strings.HasSuffix(t, "*") {
				return true
			}
		}
		return false
	}
	for _, e := range expect {
		e = strings.TrimSpace(e)
		k, v, has := strings.Cut(e, ":")
		v = strings.TrimSpace(v)
		ok, known := false, true
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "contains":
			ok = strings.Contains(lower, strings.ToLower(v))
		case "not_contains":
			ok = !strings.Contains(lower, strings.ToLower(v))
		case "calls":
			ok = called(v)
		case "no_calls":
			ok = !called(v)
		case "json":
			i, j := strings.IndexByte(output, '{'), strings.LastIndexByte(output, '}')
			ok = i >= 0 && j > i && json.Valid([]byte(output[i:j+1]))
		default:
			known = false
		}
		if !has && strings.EqualFold(e, "json") {
			known = true
			i, j := strings.IndexByte(output, '{'), strings.LastIndexByte(output, '}')
			ok = i >= 0 && j > i && json.Valid([]byte(output[i:j+1]))
		}
		switch {
		case !known:
			out = append(out, "? "+e)
		case ok:
			out = append(out, "✓ "+e)
		default:
			out = append(out, "✗ "+e)
			pass = false
		}
	}
	return out, pass
}
