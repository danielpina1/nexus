package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/internal/runtime/chatsession"
)

type run struct {
	taskID, runID, model string
	nodeID               string // for user-facing messages
	proc                 *cliproc.Process
	log                  *slog.Logger
	events               chan *nexusv1.Event
	stderr               *cliproc.Tail

	// Nexus MCP bridge (nil when disabled).
	gate    *approvalgate.Gate
	cleanup func()

	mu        sync.Mutex
	cancelled bool

	emitMu       sync.Mutex
	eventsClosed bool

	// reader-goroutine state
	lastMessage string
	completed   bool
	failure     string
	usage       *nexusv1.Cost // legacy token_count, emitted at exit
	costEmitted bool
	total       *nexusv1.Cost
	started     map[string]time.Time

	// chat run bookkeeping (nil for other runs): the reply is recorded as a
	// turn so the next message of the conversation carries the history.
	chat      *chatsession.Store
	chatKey   string
	chatUser  string
	announced bool
}

func newRun(taskID, runID, model string, log *slog.Logger) *run {
	return &run{
		taskID: taskID, runID: runID, model: model, log: log,
		events:  make(chan *nexusv1.Event, 256),
		stderr:  &cliproc.Tail{N: 4096},
		started: map[string]time.Time{},
	}
}

func (r *run) start(cmd *exec.Cmd, prompt string) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = r.stderr
	proc, err := cliproc.Start(cmd)
	if err != nil {
		return fmt.Errorf("codex: start %s: %w", cmd.Path, err)
	}
	r.proc = proc
	go func() {
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
	}()
	go r.read(stdout)
	return nil
}

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

func (r *run) Send(context.Context, *nexusv1.RunInput) error { return ErrSteeringUnsupported }

// Resolve feeds a (node-verified) approval decision to a gated Nexus MCP
// tool call.
func (r *run) Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	if r.gate == nil {
		return ErrApprovalsUnsupported
	}
	return r.gate.Resolve(ctx, res)
}

func (r *run) Cancel(context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.stopGate()
	r.proc.Kill(3 * time.Second)
	return nil
}

// emit is safe from any goroutine (the MCP bridge emits approvals); events
// after the channel is closed are dropped.
func (r *run) emit(ev *nexusv1.Event) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if r.eventsClosed {
		return
	}
	r.events <- ev
}

func (r *run) closeEvents() {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if !r.eventsClosed {
		r.eventsClosed = true
		close(r.events)
	}
}

func (r *run) stopGate() {
	if r.gate != nil {
		r.gate.Close()
	}
}

func (r *run) stopBridge() {
	r.stopGate()
	if r.cleanup != nil {
		r.cleanup()
	}
}

// announceModel emits a zero cost naming the model that serves the run
// (the --model we passed, or the one codex reports in session_configured;
// "" = the CLI default, shown as "Codex default").
func (r *run) announceModel(model string) {
	if model != "" {
		r.model = model
	}
	if r.announced && model == "" {
		return
	}
	r.announced = true
	r.emit(agentloop.CostEvent(r.taskID, r.runID, &nexusv1.Cost{Model: r.model, Runtime: "codex"}))
}

func (r *run) read(stdout io.Reader) {
	defer r.closeEvents()
	r.announceModel("")
	if err := cliproc.ReadLines(stdout, r.handleLine); err != nil {
		r.log.Warn("codex: reading stdout", "err", err)
	}
	waitErr := r.proc.Wait()
	r.stopBridge()
	if r.usage != nil && !r.costEmitted {
		r.recordCost(r.usage)
	}
	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	var cost *nexusv1.Cost
	if r.total != nil {
		cost = r.total
	}
	switch {
	case cancelled:
		r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled", "", cost))
	case r.failure != "":
		msg := r.failure
		if tail := r.stderr.String(); tail != "" && waitErr != nil {
			msg += "\n" + tail
		}
		r.emit(r.failedEvent(msg, cost))
	case r.completed || waitErr == nil:
		if r.chat != nil {
			if err := r.chat.Record(r.chatKey, "", "", r.model, chatsession.Turn{User: r.chatUser, Assistant: r.lastMessage}); err != nil {
				r.log.Warn("codex: recording chat turn", "err", err)
			}
		}
		r.emit(agentloop.OutputEvent(r.taskID, r.runID, "", true))
		r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_SUCCEEDED, strings.TrimSpace(r.lastMessage), "", cost))
	default:
		msg := "codex exited: " + waitErr.Error()
		if tail := r.stderr.String(); tail != "" {
			msg += "\n" + tail
		}
		r.emit(r.failedEvent(msg, cost))
	}
}

// failedEvent builds the FAILED terminal event; usage/rate limits and missing
// logins get a clear summary and result.error_kind (see claudecode).
func (r *run) failedEvent(msg string, cost *nexusv1.Cost) *nexusv1.Event {
	kind, resets := cliproc.ClassifyFailure(msg)
	summary := ""
	extra := map[string]any{"runtime": "codex"}
	switch kind {
	case cliproc.KindRateLimited:
		summary = "Codex (ChatGPT) usage limit reached"
		if resets != "" {
			summary += "; resets " + resets
			extra["resets_at"] = resets
		}
		summary += "."
	case cliproc.KindAuth:
		where := "on this machine"
		if r.nodeID != "" {
			where = "on node " + r.nodeID
		}
		summary = "Codex is not logged in " + where + ". Run `codex login` there and choose \"Sign in with ChatGPT\" " +
			"(headless machine: log in on a computer with a browser and copy ~/.codex/auth.json, or forward localhost:1455 over SSH during `codex login`)."
	}
	return agentloop.FailedEvent(r.taskID, r.runID, summary, msg, kind, extra, cost)
}

func (r *run) recordCost(c *nexusv1.Cost) {
	c.Model, c.Runtime = r.model, "codex"
	r.costEmitted = true
	if r.total == nil {
		r.total = &nexusv1.Cost{Model: r.model, Runtime: "codex"}
	}
	agentloop.AddCost(r.total, c)
	r.emit(agentloop.CostEvent(r.taskID, r.runID, c))
}

// ---- JSONL parsing ----

type cxUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

type cxError struct {
	Message string `json:"message"`
}

type cxItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"` // agent_message, reasoning, command_execution, file_change, mcp_tool_call, web_search, todo_list, error
	ItemType         string          `json:"item_type"`
	Text             string          `json:"text"`
	Command          json.RawMessage `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Changes          json.RawMessage `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Query            string          `json:"query"`
	Message          string          `json:"message"`
}

func (it *cxItem) kind() string {
	if it.Type != "" {
		return it.Type
	}
	return it.ItemType
}

type cxLine struct {
	Type  string   `json:"type"`
	Item  *cxItem  `json:"item"`
	Usage *cxUsage `json:"usage"`
	Error *cxError `json:"error"`
	// top-level "error" events carry message directly
	Message string `json:"message"`
	// legacy format
	Msg json.RawMessage `json:"msg"`
}

func (r *run) handleLine(line []byte) {
	var l cxLine
	if err := json.Unmarshal(line, &l); err != nil {
		r.log.Warn("codex: malformed JSONL line", "err", err, "line", truncate(string(line), 300))
		return
	}
	if len(l.Msg) > 0 {
		r.handleLegacy(l.Msg)
		return
	}
	switch l.Type {
	case "item.started":
		if l.Item != nil {
			r.itemStarted(l.Item)
		}
	case "item.completed":
		if l.Item != nil {
			r.itemCompleted(l.Item)
		}
	case "turn.completed":
		r.completed = true
		r.failure = ""
		if l.Usage != nil {
			r.recordCost(&nexusv1.Cost{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens})
		}
	case "turn.failed":
		msg := "turn failed"
		if l.Error != nil && l.Error.Message != "" {
			msg = l.Error.Message
		}
		r.failure = msg
	case "error":
		msg := l.Message
		if msg == "" && l.Error != nil {
			msg = l.Error.Message
		}
		r.failure = nonEmpty(msg, "codex error")
	}
}

func commandString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []string
	if json.Unmarshal(raw, &parts) == nil {
		return strings.Join(parts, " ")
	}
	return strings.TrimSpace(string(raw))
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// toolCall describes how an item maps to a Nexus tool call.
func itemCall(it *cxItem) (tool string, input json.RawMessage, class nexusv1.ActionClass, ok bool) {
	switch it.kind() {
	case "command_execution":
		return "shell", mustJSON(map[string]string{"command": commandString(it.Command)}), nexusv1.ActionClass_ACTION_CLASS_COMPUTE, true
	case "file_change":
		return "apply_patch", mustJSON(map[string]json.RawMessage{"changes": orNull(it.Changes)}), nexusv1.ActionClass_ACTION_CLASS_COMPUTE, true
	case "mcp_tool_call":
		name := it.Tool
		if it.Server != "" {
			name = it.Server + "." + it.Tool
		}
		return name, it.Arguments, nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED, true
	case "web_search":
		return "web_search", mustJSON(map[string]string{"query": it.Query}), nexusv1.ActionClass_ACTION_CLASS_READ, true
	}
	return "", nil, 0, false
}

func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func (r *run) itemStarted(it *cxItem) {
	tool, input, class, ok := itemCall(it)
	if !ok {
		return
	}
	r.started[it.ID] = time.Now()
	r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, it.ID, tool, input, class))
}

func (r *run) itemCompleted(it *cxItem) {
	switch it.kind() {
	case "agent_message", "assistant_message":
		if it.Text != "" {
			r.lastMessage = it.Text
			r.emit(agentloop.OutputEvent(r.taskID, r.runID, it.Text, false))
		}
		return
	case "error":
		r.log.Info("codex: non-fatal error item", "message", it.Message)
		return
	}
	tool, input, class, ok := itemCall(it)
	if !ok {
		return
	}
	start, seen := r.started[it.ID]
	if !seen {
		start = time.Now()
		r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, it.ID, tool, input, class))
	}
	delete(r.started, it.ID)
	d := time.Since(start)

	var out json.RawMessage
	var errMsg string
	switch it.kind() {
	case "command_execution":
		res := map[string]any{"output": it.AggregatedOutput}
		if it.ExitCode != nil {
			res["exit_code"] = *it.ExitCode
			if *it.ExitCode != 0 {
				errMsg = fmt.Sprintf("exit code %d", *it.ExitCode)
			}
		}
		out = mustJSON(res)
	case "file_change":
		out = mustJSON(map[string]any{"status": it.Status, "changes": orNull(it.Changes)})
	case "mcp_tool_call":
		out = it.Result
		if len(it.Error) > 0 && string(it.Error) != "null" {
			var e cxError
			if json.Unmarshal(it.Error, &e) == nil && e.Message != "" {
				errMsg = e.Message
			} else {
				errMsg = string(it.Error)
			}
		}
	case "web_search":
		out = mustJSON(map[string]string{"query": it.Query})
	}
	if errMsg == "" && it.Status == "failed" {
		errMsg = "failed"
	}
	r.emit(agentloop.ToolResultEvent(r.taskID, r.runID, it.ID, out, errMsg, d))
}

// ---- legacy {"id","msg":{...}} format ----

type legacyMsg struct {
	Type             string          `json:"type"`
	Message          string          `json:"message"`
	LastAgentMessage string          `json:"last_agent_message"`
	CallID           string          `json:"call_id"`
	Command          json.RawMessage `json:"command"`
	Stdout           string          `json:"stdout"`
	Stderr           string          `json:"stderr"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Changes          json.RawMessage `json:"changes"`
	Success          *bool           `json:"success"`
	Invocation       *struct {
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"invocation"`
	Result       json.RawMessage `json:"result"`
	Model        string          `json:"model"` // session_configured
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	Info         *struct {
		Total *cxUsage `json:"total_token_usage"`
	} `json:"info"`
}

func (r *run) handleLegacy(raw json.RawMessage) {
	var m legacyMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		r.log.Warn("codex: malformed legacy msg", "err", err)
		return
	}
	switch m.Type {
	case "session_configured":
		if m.Model != "" && m.Model != r.model {
			r.announced = false
			r.announceModel(m.Model)
		}
	case "agent_message":
		if m.Message != "" {
			r.lastMessage = m.Message
			r.emit(agentloop.OutputEvent(r.taskID, r.runID, m.Message, false))
		}
	case "exec_command_begin":
		r.started[m.CallID] = time.Now()
		r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, m.CallID, "shell",
			mustJSON(map[string]string{"command": commandString(m.Command)}), nexusv1.ActionClass_ACTION_CLASS_COMPUTE))
	case "exec_command_end":
		output := m.AggregatedOutput
		if output == "" {
			output = m.Stdout + m.Stderr
		}
		res := map[string]any{"output": output}
		var errMsg string
		if m.ExitCode != nil {
			res["exit_code"] = *m.ExitCode
			if *m.ExitCode != 0 {
				errMsg = fmt.Sprintf("exit code %d", *m.ExitCode)
			}
		}
		r.legacyResult(m.CallID, mustJSON(res), errMsg)
	case "patch_apply_begin":
		r.started[m.CallID] = time.Now()
		r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, m.CallID, "apply_patch",
			mustJSON(map[string]json.RawMessage{"changes": orNull(m.Changes)}), nexusv1.ActionClass_ACTION_CLASS_COMPUTE))
	case "patch_apply_end":
		var errMsg string
		if m.Success != nil && !*m.Success {
			errMsg = nonEmpty(strings.TrimSpace(m.Stderr), "patch failed")
		}
		r.legacyResult(m.CallID, mustJSON(map[string]string{"stdout": m.Stdout}), errMsg)
	case "mcp_tool_call_begin":
		name, args := "mcp", json.RawMessage(nil)
		if m.Invocation != nil {
			name, args = m.Invocation.Server+"."+m.Invocation.Tool, m.Invocation.Arguments
		}
		r.started[m.CallID] = time.Now()
		r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, m.CallID, name, args, nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED))
	case "mcp_tool_call_end":
		r.legacyResult(m.CallID, m.Result, "")
	case "token_count":
		u := &nexusv1.Cost{InputTokens: m.InputTokens, OutputTokens: m.OutputTokens}
		if m.Info != nil && m.Info.Total != nil {
			u = &nexusv1.Cost{InputTokens: m.Info.Total.InputTokens, OutputTokens: m.Info.Total.OutputTokens}
		}
		r.usage = u // cumulative; emitted once at exit
	case "task_complete":
		r.completed = true
		if m.LastAgentMessage != "" {
			r.lastMessage = m.LastAgentMessage
		}
	case "error":
		r.failure = nonEmpty(m.Message, "codex error")
	}
}

func (r *run) legacyResult(callID string, out json.RawMessage, errMsg string) {
	var d time.Duration
	if t, ok := r.started[callID]; ok {
		d = time.Since(t)
		delete(r.started, callID)
	}
	r.emit(agentloop.ToolResultEvent(r.taskID, r.runID, callID, out, errMsg, d))
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
