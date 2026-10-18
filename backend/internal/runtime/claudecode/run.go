package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop/cliproc"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
)

// exitGrace is how long the CLI may linger after its result before it is killed.
var exitGrace = 10 * time.Second

// ErrRunFinished is returned by Send after the result has arrived.
var ErrRunFinished = errors.New("claude-code: run finished")

type run struct {
	taskID, runID string
	nodeID        string // for user-facing messages
	proc          *cliproc.Process
	log           *slog.Logger
	events        chan *nexusv1.Event
	stderr        *cliproc.Tail

	// Nexus MCP bridge (nil when disabled).
	gate    *approvalgate.Gate
	cleanup func()

	mu          sync.Mutex
	stdin       io.WriteCloser
	stdinClosed bool
	cancelled   bool

	emitMu       sync.Mutex
	eventsClosed bool

	// reader-goroutine state
	terminal   bool
	model      string
	announced  string // model id last announced as a zero cost
	sessionID  string
	toolStarts map[string]time.Time
	toolNames  map[string]string

	// streaming (chat.go)
	startedAt   time.Time
	firstToken  bool
	sawOutput   bool
	curText     *strings.Builder // text block being streamed
	curSep      bool             // the block needs a "\n\n" separator
	streamed    []string         // streamed blocks awaiting their full message
	lastWasText bool
	dmu         sync.Mutex
	dbuf        strings.Builder
	dtimer      *time.Timer
	lastFlush   time.Time

	// chat session (nil for other runs) and resume fallback
	chat         *chatState
	resumeFailed bool

	// AskUserQuestion (chat.go)
	qmu      sync.Mutex
	qseq     int
	pendingQ map[string]chan *nexusv1.RunInput
	done     chan struct{}
	doneOnce sync.Once
}

func newRun(taskID, runID string, log *slog.Logger) *run {
	return &run{
		taskID: taskID, runID: runID, log: log,
		events:     make(chan *nexusv1.Event, 256),
		stderr:     &cliproc.Tail{N: 4096},
		toolStarts: map[string]time.Time{},
		toolNames:  map[string]string{},
		pendingQ:   map[string]chan *nexusv1.RunInput{},
		done:       make(chan struct{}),
	}
}

func (r *run) finish() { r.doneOnce.Do(func() { close(r.done) }) }

// start launches cmd and writes the prompt.
func (r *run) start(cmd *exec.Cmd, prompt string) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.stdin, r.stdinClosed = stdin, false
	r.mu.Unlock()
	cmd.Stderr = r.stderr
	r.startedAt = time.Now()
	proc, err := cliproc.Start(cmd)
	if err != nil {
		return fmt.Errorf("claude-code: start %s: %w", cmd.Path, err)
	}
	r.proc = proc
	if err := r.writeUser(prompt); err != nil {
		proc.Kill(time.Second)
	}
	go r.read(stdout)
	return nil
}

func (r *run) Events() <-chan *nexusv1.Event { return r.events }

// userMessage is a stream-json input line.
func userMessage(text string) ([]byte, error) {
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (r *run) writeUser(text string) error {
	line, err := userMessage(text)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stdinClosed {
		return ErrRunFinished
	}
	_, err = r.stdin.Write(line)
	return err
}

func (r *run) closeStdin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stdinClosed {
		r.stdinClosed = true
		_ = r.stdin.Close()
	}
}

func (r *run) Send(_ context.Context, in *nexusv1.RunInput) error {
	if r.deliverAnswer(in) { // answer to an AskUserQuestion
		return nil
	}
	text := agentloop.FormatInput(in)
	if text == "" {
		return errors.New("claude-code: empty input")
	}
	return r.writeUser(text)
}

// Resolve feeds a (node-verified) approval decision to the permission prompt
// or Nexus tool call waiting for it.
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
	r.finish()
	if r.gate != nil {
		r.gate.Close() // releases waiting permission prompts (deny)
	}
	r.closeStdin()
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

// stopBridge closes the approval gate and the MCP endpoint.
func (r *run) stopBridge() {
	if r.gate != nil {
		r.gate.Close()
	}
	if r.cleanup != nil {
		r.cleanup()
	}
}

func (r *run) read(stdout io.Reader) {
	if err := cliproc.ReadLines(stdout, r.handleLine); err != nil {
		r.log.Warn("claude-code: reading stdout", "err", err)
	}
	r.flushDeltas()
	r.closeStdin()
	waitErr := r.proc.Wait()
	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
	// A resumed chat session that is gone: start again as a fresh session.
	if c := r.chat; c != nil && c.retry != nil && !cancelled && (r.resumeFailed || (!r.terminal && !r.sawOutput)) {
		retry := c.retry
		c.retry, c.resumed = nil, false
		r.log.Info("claude-code: resume failed; starting a fresh session with the recent turns",
			"session_id", r.sessionID, "stderr", truncate(r.stderr.String(), 300))
		if c.store != nil {
			_ = c.store.Forget(c.key)
		}
		r.terminal, r.resumeFailed, r.sessionID = false, false, ""
		r.stderr = &cliproc.Tail{N: 4096}
		if err := retry(); err == nil {
			return // the new reader goroutine owns the events now
		} else {
			r.log.Warn("claude-code: fresh session failed to start", "err", err)
		}
	}
	defer r.closeEvents()
	defer r.finish()
	r.stopBridge()
	if r.terminal {
		return
	}
	if cancelled {
		r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_CANCELLED, "cancelled", "", nil))
		return
	}
	msg := "claude exited without a result"
	if waitErr != nil {
		msg += ": " + waitErr.Error()
	}
	if tail := r.stderr.String(); tail != "" {
		msg += "\n" + tail
	}
	r.emit(r.failure(msg, nil))
}

// failure builds the FAILED terminal event for msg. Usage/rate limits and
// missing logins get a clear summary and result.error_kind (rate_limited /
// auth) so the hub can retry on the agent's next runtime.
func (r *run) failure(msg string, cost *nexusv1.Cost) *nexusv1.Event {
	kind, resets := cliproc.ClassifyFailure(msg)
	summary := ""
	extra := map[string]any{"runtime": "claude-code"}
	switch kind {
	case cliproc.KindRateLimited:
		summary = "Claude usage limit reached"
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
		summary = "Claude Code is not logged in " + where + ". Run `claude` once there and log in with your Claude subscription " +
			"(for a headless/always-on node: `claude setup-token` and set CLAUDE_CODE_OAUTH_TOKEN for nexus-node)."
	}
	return agentloop.FailedEvent(r.taskID, r.runID, summary, msg, kind, extra, cost)
}

// ---- stream-json parsing ----

type ccUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type ccLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Message   *struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result       string   `json:"result"`
	IsError      bool     `json:"is_error"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	Usage        *ccUsage `json:"usage"`
	NumTurns     int      `json:"num_turns"`
	Errors       []string `json:"errors"`
	// stream_event (--include-partial-messages)
	Event           json.RawMessage `json:"event"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
}

type ccBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func blocks(raw json.RawMessage) []ccBlock {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil // plain-string content (e.g. replayed user input)
	}
	var bs []ccBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

// blockText flattens tool_result content (string or array of text blocks).
func blockText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []ccBlock
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			switch p.Type {
			case "text":
				out = append(out, p.Text)
			case "":
			default:
				out = append(out, "["+p.Type+"]")
			}
		}
		return strings.Join(out, "\n")
	}
	return string(raw)
}

func (r *run) handleLine(line []byte) {
	if r.terminal {
		return // ignore anything after the result
	}
	var l ccLine
	if err := json.Unmarshal(line, &l); err != nil {
		r.log.Warn("claude-code: malformed stream-json line", "err", err, "line", truncate(string(line), 300))
		return
	}
	topLevel := l.ParentToolUseID == nil || *l.ParentToolUseID == ""
	if l.Type == "stream_event" {
		if topLevel {
			r.handleStreamEvent(l.Event)
		}
		return
	}
	r.flushDeltas() // keep streamed text ahead of whatever comes next
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			r.sessionID = l.SessionID
			r.log.Info("claude-code: session ready", "ms", time.Since(r.startedAt).Milliseconds(),
				"session_id", l.SessionID, "model", l.Model, "resumed", r.chat != nil && r.chat.resumed)
			r.announceModel(l.Model)
		}
	case "assistant":
		if l.Message == nil {
			return
		}
		if topLevel {
			r.announceModel(l.Message.Model)
		}
		for _, b := range blocks(l.Message.Content) {
			switch b.Type {
			case "text":
				r.assistantText(b.Text, topLevel)
			case "tool_use":
				r.sawOutput = true
				r.lastWasText = false
				r.toolStarts[b.ID] = time.Now()
				r.toolNames[b.ID] = b.Name
				r.emit(agentloop.ToolCallEvent(r.taskID, r.runID, b.ID, b.Name, b.Input, ActionClass(b.Name)))
			}
		}
	case "user":
		if l.Message == nil {
			return
		}
		for _, b := range blocks(l.Message.Content) {
			if b.Type != "tool_result" {
				continue
			}
			var d time.Duration
			if t, ok := r.toolStarts[b.ToolUseID]; ok {
				d = time.Since(t)
				delete(r.toolStarts, b.ToolUseID)
			}
			text := blockText(b.Content)
			if b.IsError {
				r.emit(agentloop.ToolResultEvent(r.taskID, r.runID, b.ToolUseID, nil, nonEmpty(text, "tool error"), d))
				continue
			}
			out, _ := json.Marshal(map[string]string{"content": text})
			r.emit(agentloop.ToolResultEvent(r.taskID, r.runID, b.ToolUseID, out, "", d))
		}
	case "result":
		if l.SessionID != "" {
			r.sessionID = l.SessionID
		}
		r.handleResult(l)
	}
}

func (r *run) handleResult(l ccLine) {
	r.terminal = true
	if c := r.chat; c != nil && c.resumed && !r.sawOutput && (l.IsError || l.Subtype != "success") &&
		resumeGone(strings.Join(append([]string{l.Result}, l.Errors...), " ")) {
		r.resumeFailed = true // read() restarts as a fresh session
		r.closeStdin()
		return
	}
	cost := &nexusv1.Cost{Model: r.model, Runtime: "claude-code"}
	if l.Usage != nil {
		cost.InputTokens = l.Usage.InputTokens + l.Usage.CacheCreationInputTokens + l.Usage.CacheReadInputTokens
		cost.OutputTokens = l.Usage.OutputTokens
	}
	if l.TotalCostUSD != nil {
		cost.Amount = &nexusv1.Money{Currency: "USD", MinorUnits: int64(math.Ceil(*l.TotalCostUSD*100 - 1e-9))}
	}
	hasCost := l.Usage != nil || l.TotalCostUSD != nil
	if hasCost {
		r.emit(agentloop.CostEvent(r.taskID, r.runID, cost))
	} else {
		cost = nil
	}
	if l.Subtype == "success" && !l.IsError && !cliproc.LimitMessage(l.Result) {
		r.recordChat(strings.TrimSpace(l.Result))
		r.emit(agentloop.OutputEvent(r.taskID, r.runID, "", true))
		r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_SUCCEEDED, strings.TrimSpace(l.Result), "", cost))
	} else {
		msg := strings.TrimSpace(l.Result)
		if msg == "" && len(l.Errors) > 0 {
			msg = strings.Join(l.Errors, "; ")
		}
		if msg == "" {
			msg = "claude-code: " + nonEmpty(l.Subtype, "error")
		} else if l.Subtype != "" && l.Subtype != "success" {
			msg = l.Subtype + ": " + msg
		}
		r.emit(r.failure(msg, cost))
	}
	// The run is over: let the CLI exit, and make sure it does.
	r.closeStdin()
	go func() {
		t := time.NewTimer(exitGrace)
		defer t.Stop()
		select {
		case <-r.proc.Exited():
		case <-t.C:
			r.proc.Kill(time.Second)
		}
	}()
}

// ActionClass classifies Claude Code's built-in tools. Workspace edits and
// shell commands are "compute" (they stay in the run's workspace, sandboxed
// by Claude Code's own permissions); MCP tools are left unspecified so the
// hub classifies them from its own registry.
func ActionClass(tool string) nexusv1.ActionClass {
	switch tool {
	case "Read", "Glob", "Grep", "LS", "WebFetch", "WebSearch", "NotebookRead", "TodoRead", "BashOutput":
		return nexusv1.ActionClass_ACTION_CLASS_READ
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "Bash", "KillShell", "KillBash", "Task":
		return nexusv1.ActionClass_ACTION_CLASS_COMPUTE
	case "TodoWrite", "ExitPlanMode":
		return nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL
	}
	return nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED
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
