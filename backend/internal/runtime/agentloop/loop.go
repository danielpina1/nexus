// Package agentloop is Nexus's native agent loop: model → tool calls →
// execute → feed results → repeat, until the model answers without calling
// tools or a limit is hit. It is used by the "openai" and "ollama" runtimes
// and by anything else that wants a tool-calling agent over kernel.LLM.
//
// Every step is reported as *nexusv1.Event on Run.Events():
//
//	RunOutput      streamed text deltas; a final RunOutput{is_final:true} (empty
//	               text) marks the end of the answer
//	ToolCall       before each tool invocation (action_class from the ToolSpec)
//	ToolResult     after it (output, error, duration)
//	ArtifactAdded  for each artifact a tool returns
//	CostRecorded   after each model call that reported usage
//	TaskUpdated    exactly once, last: SUCCEEDED (summary = final answer),
//	               FAILED (summary/result.error = reason) or CANCELLED
//
// The channel is closed after the terminal event. Consumers must drain it:
// emission blocks when the buffer is full.
//
// Tool errors (unknown tool, bad arguments, Invoke errors, panics) are given
// back to the model as tool results, never fatal.
//
// Ask before acting: every tool call goes through an approvalgate.Gate with
// the tool's ToolSpec.ActionClass (unspecified classes fail safe to
// approve; ask_user and request_approval, which ask the user themselves, are
// never gated). AUTO runs at once, NOTIFY runs and emits a Notice, APPROVE and
// BIOMETRIC emit an Approval (payload {tool, input}) + TaskUpdated
// WAITING_APPROVAL and wait for Resolve; a denial is returned to the model as
// the tool result {"error":"denied by user"} (the run continues and a
// TaskUpdated RUNNING is emitted). Config.Agent supplies the agent policy
// that can tighten levels. ask_user and
// request_approval are ordinary (hub) tools from the loop's point of view:
// they block until answered. Send appends a user message that the model sees
// before its next step (an answer to a question is phrased as such).
package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

// DefaultMaxSteps bounds model calls per run when Limits.MaxSteps is zero.
const DefaultMaxSteps = 50

// maxToolResultBytes caps what is fed back to the model per tool call.
const maxToolResultBytes = 64 << 10

// ErrRunFinished is returned by Send on a finished run.
var ErrRunFinished = errors.New("agentloop: run finished")

// Limits bound a run.
type Limits struct {
	MaxSteps int            // model calls; 0 = DefaultMaxSteps
	MaxCost  *nexusv1.Money // nil or zero = unlimited; compared in the same currency
	Deadline time.Time      // zero = none
}

// Config describes one run.
type Config struct {
	LLM   kernel.LLM
	Model string // alias, resolved by the LLM
	// Instructions are the agent's instructions (note body); they are placed
	// after the Nexus preamble in the system prompt.
	Instructions string
	// SystemPrompt, when set, replaces the generated system prompt entirely.
	SystemPrompt string
	Prompt       string
	// History is prior conversation inserted between the system prompt and
	// Prompt (e.g. on escalation).
	History []kernel.ChatMessage
	Tools   []kernel.Tool
	Limits  Limits
	// Agent supplies the policy map used to gate tool calls (nil: built-in
	// defaults only).
	Agent *nexusv1.Agent
	// Gate overrides the approval gate (nil: one is created for the run and
	// closed when it ends).
	Gate *approvalgate.Gate

	TaskID, RunID, AgentID, NodeID string
	Runtime                        string // reported in Cost.runtime

	NoStream    bool // use Chat instead of ChatStream
	MaxTokens   int
	Temperature *float64
	Logger      *slog.Logger
	EventBuffer int // default 256
	// Now is used for the preamble's timestamp (tests).
	Now func() time.Time
}

// Run is a running loop. It implements kernel.Run.
type Run struct {
	cfg     Config
	tools   map[string]kernel.Tool
	gate    *approvalgate.Gate
	ownGate bool
	events  chan *nexusv1.Event
	cancel  context.CancelFunc
	done    chan struct{}
	log     *slog.Logger

	mu        sync.Mutex
	pending   []string
	finished  bool
	cancelled bool
}

var _ kernel.Run = (*Run)(nil)

// Start launches the loop in a goroutine.
func Start(ctx context.Context, cfg Config) (*Run, error) {
	if cfg.LLM == nil {
		return nil, errors.New("agentloop: no LLM")
	}
	if cfg.Model == "" {
		return nil, errors.New("agentloop: no model")
	}
	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = 256
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Run{
		cfg:    cfg,
		tools:  map[string]kernel.Tool{},
		events: make(chan *nexusv1.Event, cfg.EventBuffer),
		done:   make(chan struct{}),
		log:    cfg.Logger.With("run_id", cfg.RunID),
	}
	for _, t := range cfg.Tools {
		r.tools[t.Spec().Name] = t
	}
	r.gate = cfg.Gate
	if r.gate == nil {
		agent := cfg.Agent
		if agent == nil {
			agent = &nexusv1.Agent{Id: cfg.AgentID}
		}
		r.gate = approvalgate.New(approvalgate.Config{
			TaskID: cfg.TaskID, RunID: cfg.RunID, Agent: agent, NodeID: cfg.NodeID,
			Emit: r.emit, Log: r.log,
		})
		r.ownGate = true
	}
	// The run outlives the Start ctx only through its own context.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if !cfg.Limits.Deadline.IsZero() {
		var c2 context.CancelFunc
		runCtx, c2 = context.WithDeadline(runCtx, cfg.Limits.Deadline)
		prev := cancel
		cancel = func() { c2(); prev() }
	}
	r.cancel = cancel
	go r.loop(runCtx)
	return r, nil
}

// Events implements kernel.Run.
func (r *Run) Events() <-chan *nexusv1.Event { return r.events }

// Done is closed when the loop has exited (after Events is closed).
func (r *Run) Done() <-chan struct{} { return r.done }

// Send appends a user message (or an answer to a question).
func (r *Run) Send(_ context.Context, in *nexusv1.RunInput) error {
	text := FormatInput(in)
	if text == "" {
		return errors.New("agentloop: empty input")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return ErrRunFinished
	}
	r.pending = append(r.pending, text)
	return nil
}

// FormatInput renders a RunInput as a user message.
func FormatInput(in *nexusv1.RunInput) string {
	if in == nil {
		return ""
	}
	answer := strings.TrimSpace(in.GetChoice())
	if t := strings.TrimSpace(in.GetText()); t != "" {
		if answer != "" {
			answer += " — " + t
		} else {
			answer = t
		}
	}
	if in.GetQuestionId() != "" && answer != "" {
		return fmt.Sprintf("Answer to question %s: %s", in.GetQuestionId(), answer)
	}
	return answer
}

// Resolve delivers an approval decision (already verified by the node) to a
// gated tool call waiting for it.
func (r *Run) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	return r.gate.Resolve(ctx, res)
}

// Cancel stops the run; the terminal event is TaskUpdated CANCELLED.
func (r *Run) Cancel(context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.cancel()
	return nil
}

func (r *Run) emit(ev *nexusv1.Event) { r.events <- ev }

type outcome struct {
	state   nexusv1.TaskState
	summary string
	err     string
}

func (r *Run) loop(ctx context.Context) {
	defer close(r.done)
	defer r.cancel()
	if r.ownGate {
		defer r.gate.Close()
	}
	total := &nexusv1.Cost{Runtime: r.cfg.Runtime}
	out := r.run(ctx, total)
	if out.state == nexusv1.TaskState_TASK_STATE_UNSPECIFIED {
		out = r.ctxOutcome(ctx, failed("run stopped"))
	}
	r.mu.Lock()
	r.finished = true
	r.mu.Unlock()
	var cost *nexusv1.Cost
	if total.InputTokens != 0 || total.OutputTokens != 0 || total.Amount != nil {
		cost = total
	}
	r.emit(TerminalEvent(r.cfg.TaskID, r.cfg.RunID, out.state, out.summary, out.err, cost))
	close(r.events)
}

func (r *Run) ctxOutcome(ctx context.Context, fallback outcome) outcome {
	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	switch {
	case cancelled:
		return outcome{state: nexusv1.TaskState_TASK_STATE_CANCELLED, summary: "cancelled"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return outcome{state: nexusv1.TaskState_TASK_STATE_FAILED, err: "deadline exceeded (max runtime)"}
	case ctx.Err() != nil:
		return outcome{state: nexusv1.TaskState_TASK_STATE_CANCELLED, summary: "cancelled"}
	}
	return fallback
}

func (r *Run) takePending() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending
	r.pending = nil
	return p
}

func (r *Run) hasPending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending) > 0
}

func failed(format string, a ...any) outcome {
	return outcome{state: nexusv1.TaskState_TASK_STATE_FAILED, err: fmt.Sprintf(format, a...)}
}

func (r *Run) run(ctx context.Context, total *nexusv1.Cost) outcome {
	cfg := r.cfg
	maxSteps := cfg.Limits.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	specs := make([]kernel.ToolSpec, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		specs = append(specs, t.Spec())
	}
	sys := cfg.SystemPrompt
	if sys == "" {
		sys = SystemPrompt(cfg.Instructions, specs, cfg.Now())
	}
	msgs := []kernel.ChatMessage{{Role: "system", Content: sys}}
	msgs = append(msgs, cfg.History...)
	if strings.TrimSpace(cfg.Prompt) != "" {
		msgs = append(msgs, kernel.ChatMessage{Role: "user", Content: cfg.Prompt})
	}

	for step := 0; ; step++ {
		for _, p := range r.takePending() {
			msgs = append(msgs, kernel.ChatMessage{Role: "user", Content: p})
		}
		if ctx.Err() != nil {
			return outcome{}
		}
		if step >= maxSteps {
			return failed("step limit reached (%d model calls)", maxSteps)
		}
		req := kernel.ChatRequest{
			Model:       cfg.Model,
			Messages:    msgs,
			Tools:       specs,
			MaxTokens:   cfg.MaxTokens,
			Temperature: cfg.Temperature,
		}
		streamed := false
		var resp *kernel.ChatResponse
		var err error
		if cfg.NoStream {
			resp, err = cfg.LLM.Chat(ctx, req)
		} else {
			resp, err = cfg.LLM.ChatStream(ctx, req, func(d string) {
				if d == "" {
					return
				}
				streamed = true
				r.emit(OutputEvent(cfg.TaskID, cfg.RunID, d, false))
			})
		}
		if err != nil {
			if ctx.Err() != nil {
				return outcome{}
			}
			return failed("model error: %v", err)
		}
		if resp.Cost != nil {
			c := resp.Cost
			if c.Runtime == "" {
				c.Runtime = cfg.Runtime
			}
			AddCost(total, c)
			r.emit(CostEvent(cfg.TaskID, cfg.RunID, c))
			if over, msg := overBudget(total, cfg.Limits.MaxCost); over {
				return failed("%s", msg)
			}
		}
		msg := resp.Message
		msg.Role = "assistant"
		if !streamed && msg.Content != "" {
			r.emit(OutputEvent(cfg.TaskID, cfg.RunID, msg.Content, false))
		}
		msgs = append(msgs, msg)

		if len(msg.ToolCalls) == 0 {
			if r.hasPending() {
				continue // the user said something while the model was answering
			}
			r.emit(OutputEvent(cfg.TaskID, cfg.RunID, "", true))
			return outcome{state: nexusv1.TaskState_TASK_STATE_SUCCEEDED, summary: strings.TrimSpace(msg.Content)}
		}
		for i := range msg.ToolCalls {
			tc := &msg.ToolCalls[i]
			if tc.ID == "" {
				tc.ID = fmt.Sprintf("call_%d_%d", step, i)
			}
		}
		for _, tc := range msg.ToolCalls {
			if ctx.Err() != nil {
				return outcome{}
			}
			content := r.execTool(ctx, tc)
			msgs = append(msgs, kernel.ChatMessage{Role: "tool", ToolCallID: tc.ID, Name: tc.Name, Content: content})
		}
	}
}

func overBudget(total *nexusv1.Cost, max *nexusv1.Money) (bool, string) {
	if max == nil || max.MinorUnits <= 0 || total.Amount == nil {
		return false, ""
	}
	if max.Currency != "" && total.Amount.Currency != "" && max.Currency != total.Amount.Currency {
		return false, ""
	}
	if total.Amount.MinorUnits > max.MinorUnits {
		return true, fmt.Sprintf("cost limit exceeded (%d > %d %s minor units)", total.Amount.MinorUnits, max.MinorUnits, max.Currency)
	}
	return false, ""
}

// execTool runs one tool call, emitting ToolCall/ToolResult (and artifacts),
// and returns the content fed back to the model.
func (r *Run) execTool(ctx context.Context, tc kernel.LLMToolCall) string {
	cfg := r.cfg
	args := strings.TrimSpace(tc.Arguments)
	if args == "" {
		args = "{}"
	}
	tool, ok := r.tools[tc.Name]
	class := nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED
	if ok {
		class = tool.Spec().ActionClass
	}
	r.emit(ToolCallEvent(cfg.TaskID, cfg.RunID, tc.ID, tc.Name, json.RawMessage(args), class))
	start := time.Now()
	fail := func(msg string) string {
		r.emit(ToolResultEvent(cfg.TaskID, cfg.RunID, tc.ID, nil, msg, time.Since(start)))
		b, _ := json.Marshal(map[string]string{"error": msg})
		return string(b)
	}
	if !ok {
		return fail(fmt.Sprintf("unknown tool %q; available: %s", tc.Name, strings.Join(r.toolNames(), ", ")))
	}
	if !json.Valid([]byte(args)) {
		return fail("tool arguments are not valid JSON: " + truncate(args, 200))
	}
	if msg := r.gateTool(ctx, tc.Name, args, class); msg != "" {
		return fail(msg)
	}
	res, err := invokeSafe(ctx, tool, kernel.ToolCall{
		ID: tc.ID, TaskID: cfg.TaskID, RunID: cfg.RunID, AgentID: cfg.AgentID, NodeID: cfg.NodeID,
		Input: json.RawMessage(args),
	})
	if err != nil {
		return fail(err.Error())
	}
	for _, a := range res.Artifacts {
		r.emit(ArtifactEvent(cfg.TaskID, cfg.RunID, a))
	}
	r.emit(ToolResultEvent(cfg.TaskID, cfg.RunID, tc.ID, res.Output, "", time.Since(start)))
	out := strings.TrimSpace(string(res.Output))
	if out == "" {
		out = "{}"
	}
	if len(out) > maxToolResultBytes {
		out = out[:maxToolResultBytes] + "\n…[truncated]"
	}
	return out
}

// gateTool applies the approval gate to a tool call; it returns "" when the
// call may proceed, else the error given back to the model.
func (r *Run) gateTool(ctx context.Context, name, args string, class nexusv1.ActionClass) string {
	if approvalgate.HumanInTheLoopTools[name] {
		return ""
	}
	var input any
	_ = json.Unmarshal([]byte(args), &input)
	who := r.cfg.Agent.GetName()
	if who == "" {
		who = nonEmpty(r.cfg.AgentID, "Agent")
	}
	out, err := r.gate.Check(ctx, approvalgate.Request{
		Class:   class,
		Title:   fmt.Sprintf("%s wants to use %s", who, name),
		Summary: truncate(args, 500),
		Payload: map[string]any{"tool": name, "input": input},
	})
	switch {
	case err != nil:
		return "approval failed: " + err.Error()
	case !out.Approved:
		r.gate.Resume()
		if out.Reason == "" || out.Reason == "denied" {
			return "denied by user"
		}
		return "not approved: " + out.Reason
	}
	return ""
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func invokeSafe(ctx context.Context, t kernel.Tool, c kernel.ToolCall) (res kernel.ToolResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool panicked: %v", p)
		}
	}()
	return t.Invoke(ctx, c)
}

func (r *Run) toolNames() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
