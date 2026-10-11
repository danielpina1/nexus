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
)

// exitGrace is how long the CLI may linger after its result before it is killed.
var exitGrace = 10 * time.Second

// ErrRunFinished is returned by Send after the result has arrived.
var ErrRunFinished = errors.New("claude-code: run finished")

type run struct {
	taskID, runID string
	proc          *cliproc.Process
	log           *slog.Logger
	events        chan *nexusv1.Event
	stderr        *cliproc.Tail

	mu          sync.Mutex
	stdin       io.WriteCloser
	stdinClosed bool
	cancelled   bool

	// reader-goroutine state
	terminal   bool
	model      string
	sessionID  string
	toolStarts map[string]time.Time
	toolNames  map[string]string
}

func startRun(cmd *exec.Cmd, taskID, runID, prompt string, log *slog.Logger) (*run, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	r := &run{
		taskID: taskID, runID: runID, log: log,
		events:     make(chan *nexusv1.Event, 256),
		stderr:     &cliproc.Tail{N: 4096},
		stdin:      stdin,
		toolStarts: map[string]time.Time{},
		toolNames:  map[string]string{},
	}
	cmd.Stderr = r.stderr
	proc, err := cliproc.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("claude-code: start %s: %w", cmd.Path, err)
	}
	r.proc = proc
	if err := r.writeUser(prompt); err != nil {
		proc.Kill(time.Second)
	}
	go r.read(stdout)
	return r, nil
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
	text := agentloop.FormatInput(in)
	if text == "" {
		return errors.New("claude-code: empty input")
	}
	return r.writeUser(text)
}

func (r *run) Resolve(context.Context, *nexusv1.ApprovalResolution) error {
	return ErrApprovalsUnsupported
}

func (r *run) Cancel(context.Context) error {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	r.closeStdin()
	r.proc.Kill(3 * time.Second)
	return nil
}

func (r *run) emit(ev *nexusv1.Event) { r.events <- ev }

func (r *run) read(stdout io.Reader) {
	defer close(r.events)
	if err := cliproc.ReadLines(stdout, r.handleLine); err != nil {
		r.log.Warn("claude-code: reading stdout", "err", err)
	}
	r.closeStdin()
	waitErr := r.proc.Wait()
	if r.terminal {
		return
	}
	r.mu.Lock()
	cancelled := r.cancelled
	r.mu.Unlock()
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
	r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_FAILED, "", msg, nil))
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
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			r.sessionID, r.model = l.SessionID, l.Model
			r.log.Debug("claude-code: session started", "session_id", l.SessionID, "model", l.Model)
		}
	case "assistant":
		if l.Message == nil {
			return
		}
		if r.model == "" {
			r.model = l.Message.Model
		}
		for _, b := range blocks(l.Message.Content) {
			switch b.Type {
			case "text":
				if b.Text != "" {
					r.emit(agentloop.OutputEvent(r.taskID, r.runID, b.Text, false))
				}
			case "tool_use":
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
		r.handleResult(l)
	}
}

func (r *run) handleResult(l ccLine) {
	r.terminal = true
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
	if l.Subtype == "success" && !l.IsError {
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
		r.emit(agentloop.TerminalEvent(r.taskID, r.runID, nexusv1.TaskState_TASK_STATE_FAILED, "", msg, cost))
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
