package claudecode

// Chat sessions, live token streaming and AskUserQuestion.
//
// Chat runs (run context chat=true + conversation_id, set by the command
// module for the web/app chat) keep one Claude Code session per
// conversation and agent on the node (internal/runtime/chatsession):
//
//   - the first message starts a session in a stable per-agent directory
//     (<data dir>/chat/claude-code/<agent>; Claude Code stores sessions per
//     cwd) unless the agent has a workdir/repo_dir; the session_id of the
//     init/result message is recorded with the final reply;
//   - follow-ups run `claude -p --resume <session_id>` in that directory, so
//     the CLI already holds the earlier turns (and starts faster);
//   - when resume fails (session gone: "No conversation found…", or the CLI
//     exits before any output) the run restarts at once as a fresh session
//     whose prompt carries the last turns (<earlier_conversation>).
//
// The chat prompt is chatsession.Prompt: <nexus_status> (live snapshot from
// the hub) + history (fresh sessions only) + the message, instead of the
// generic JSON context dump.
//
// Streaming (partial_messages, default true): --include-partial-messages
// makes the CLI emit stream_event lines; top-level content_block_delta
// text_delta chunks become RunOutput events, coalesced every flushEvery
// (≈20 events/s instead of one per token, each event is a hub write). The
// full assistant message that follows a streamed block is not emitted again
// (only a suffix the deltas missed). Consecutive text blocks are separated
// by a blank line.
//
// Model: the model id that actually serves the run (system/init "model",
// or a later assistant message.model when it differs) is announced at once
// as a zero CostRecorded{model, runtime:"claude-code"}, so clients can label
// the reply ("Assistant · Claude Code · claude-sonnet-4-5-20250929") before
// the result arrives; the final cost repeats it.
//
// Timing: the run logs "claude-code: session ready" (process start → init)
// and "claude-code: first token" (process start → first streamed text), in ms.
//
// AskUserQuestion: Claude Code's question tool reaches the permission
// prompt tool like any other tool. It is answered as Nexus Questions instead
// of an approval: one Question event per question (header, text, options
// with descriptions, multi_select, free text allowed); the run waits for the
// answer (Run.Send with that question_id) and allows the call with
// updatedInput {"questions": <original>, "answers": {<question>: <answer>}}
// (multi-select answers joined with ", "), which is how Claude Code takes
// answers from a permission handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/internal/runtime/chatsession"
)

// flushEvery is how often streamed text deltas are flushed as RunOutput.
var flushEvery = 50 * time.Millisecond

// chatState is a chat run's conversation bookkeeping.
type chatState struct {
	store   *chatsession.Store
	key     string
	dir     string
	user    string // the user's message (for the stored history)
	resumed bool   // started with --resume
	// retry restarts the run as a fresh session (set while resumed).
	retry func() error
}

// ---- streaming ----

type ccStreamEvent struct {
	Type         string `json:"type"`
	ContentBlock *struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// handleStreamEvent maps a top-level stream_event to text deltas.
func (r *run) handleStreamEvent(raw json.RawMessage) {
	var ev ccStreamEvent
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock != nil && ev.ContentBlock.Type == "text" {
			r.curText = &strings.Builder{}
			r.curSep = r.lastWasText
		}
	case "content_block_delta":
		if ev.Delta == nil || ev.Delta.Type != "text_delta" || ev.Delta.Text == "" {
			return
		}
		if r.curText == nil { // delta without a start: treat as a new block
			r.curText = &strings.Builder{}
			r.curSep = r.lastWasText
		}
		text := ev.Delta.Text
		if r.curText.Len() == 0 && r.curSep {
			text = "\n\n" + text
		}
		r.curText.WriteString(ev.Delta.Text)
		r.sawOutput = true
		r.emitDelta(text)
	case "content_block_stop":
		if r.curText != nil {
			if r.curText.Len() > 0 {
				r.streamed = append(r.streamed, r.curText.String())
				r.lastWasText = true
			}
			r.curText = nil
		}
		r.flushDeltas()
	}
}

// assistantText emits the text of a full assistant message block, skipping
// what the deltas already streamed.
func (r *run) assistantText(text string, topLevel bool) {
	if text == "" {
		return
	}
	r.sawOutput = true
	if topLevel {
		if len(r.streamed) > 0 {
			s := r.streamed[0]
			r.streamed = r.streamed[1:]
			if strings.HasPrefix(text, s) && len(text) > len(s) {
				r.emit(agentloop.OutputEvent(r.taskID, r.runID, text[len(s):], false))
			}
			return
		}
		if r.curText != nil && strings.HasPrefix(text, r.curText.String()) {
			// The full message arrived before the block's stop event.
			rest := text[r.curText.Len():]
			r.curText = nil
			r.lastWasText = true
			r.flushDeltas()
			if rest != "" {
				r.emit(agentloop.OutputEvent(r.taskID, r.runID, rest, false))
			}
			return
		}
	}
	r.flushDeltas()
	if r.lastWasText {
		text = "\n\n" + text
	}
	r.lastWasText = true
	r.markFirstToken()
	r.emit(agentloop.OutputEvent(r.taskID, r.runID, text, false))
}

func (r *run) markFirstToken() {
	if !r.firstToken {
		r.firstToken = true
		r.log.Info("claude-code: first token", "ms", time.Since(r.startedAt).Milliseconds(), "resumed", r.chat != nil && r.chat.resumed)
	}
}

// emitDelta buffers streamed text and flushes it at most every flushEvery.
func (r *run) emitDelta(text string) {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	r.markFirstToken()
	r.dbuf.WriteString(text)
	if time.Since(r.lastFlush) >= flushEvery {
		r.flushLocked()
		return
	}
	if r.dtimer == nil {
		r.dtimer = time.AfterFunc(flushEvery, func() {
			r.dmu.Lock()
			defer r.dmu.Unlock()
			r.flushLocked()
		})
	}
}

func (r *run) flushLocked() {
	if r.dtimer != nil {
		r.dtimer.Stop()
		r.dtimer = nil
	}
	if r.dbuf.Len() == 0 {
		return
	}
	text := r.dbuf.String()
	r.dbuf.Reset()
	r.lastFlush = time.Now()
	r.emit(agentloop.OutputEvent(r.taskID, r.runID, text, false))
}

// flushDeltas emits buffered text now (before any other event, to keep order).
func (r *run) flushDeltas() {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	r.flushLocked()
}

// announceModel emits a zero cost carrying the serving model id.
func (r *run) announceModel(model string) {
	if model == "" || model == "<synthetic>" || model == r.announced {
		return
	}
	r.model, r.announced = model, model
	r.emit(agentloop.CostEvent(r.taskID, r.runID, &nexusv1.Cost{Model: model, Runtime: "claude-code"}))
}

// recordChat stores the session and the turn after a successful result.
func (r *run) recordChat(reply string) {
	c := r.chat
	if c == nil || c.store == nil {
		return
	}
	if err := c.store.Record(c.key, r.sessionID, c.dir, r.model, chatsession.Turn{User: c.user, Assistant: reply}); err != nil {
		r.log.Warn("claude-code: recording chat session", "err", err)
	}
}

// resumeFailed reports whether a result says the resumed session is gone.
func resumeGone(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "no conversation found") || strings.Contains(l, "session not found") ||
		strings.Contains(l, "could not find session")
}

// ---- AskUserQuestion ----

// AskUserQuestionTool is Claude Code's question tool.
const AskUserQuestionTool = "AskUserQuestion"

type askQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	MultiSelect bool `json:"multiSelect"`
}

// QuestionsFrom parses AskUserQuestion input into Nexus Questions (ids are
// prefix + index).
func QuestionsFrom(input json.RawMessage, prefix string) []*nexusv1.Question {
	var in struct {
		Questions []askQuestion `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []*nexusv1.Question
	for i, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		nq := &nexusv1.Question{
			QuestionId: fmt.Sprintf("%s%d", prefix, i), Text: q.Question, Header: q.Header,
			AllowFreeText: true, MultiSelect: q.MultiSelect,
		}
		for _, o := range q.Options {
			nq.Choices = append(nq.Choices, o.Label)
			nq.Options = append(nq.Options, &nexusv1.QuestionOption{Label: o.Label, Description: o.Description})
		}
		out = append(out, nq)
	}
	return out
}

// answerText is the answer to a question from a RunInput.
func answerText(in *nexusv1.RunInput) string {
	c, t := strings.TrimSpace(in.GetChoice()), strings.TrimSpace(in.GetText())
	switch {
	case c != "" && t != "":
		return c + ", " + t
	case c != "":
		return c
	}
	return t
}

// interceptPermission is the bridge's Intercept hook.
func (r *run) interceptPermission(ctx context.Context, tool string, input json.RawMessage) (mcp.PermissionDecision, bool) {
	if tool != AskUserQuestionTool {
		return mcp.PermissionDecision{}, false
	}
	r.qmu.Lock()
	r.qseq++
	prefix := fmt.Sprintf("q_%s_%d_", r.runID, r.qseq)
	r.qmu.Unlock()
	qs := QuestionsFrom(input, prefix)
	if len(qs) == 0 {
		return mcp.PermissionDecision{Behavior: "deny", Message: "AskUserQuestion needs at least one question"}, true
	}
	r.flushDeltas()
	answers := map[string]string{}
	for _, q := range qs {
		ch := make(chan *nexusv1.RunInput, 1)
		r.qmu.Lock()
		r.pendingQ[q.QuestionId] = ch
		r.qmu.Unlock()
		r.emit(&nexusv1.Event{TaskId: r.taskID, RunId: r.runID, Body: &nexusv1.Event_Question{Question: q}})
		var in *nexusv1.RunInput
		select {
		case in = <-ch:
		case <-r.done:
		case <-ctx.Done():
		}
		r.qmu.Lock()
		delete(r.pendingQ, q.QuestionId)
		r.qmu.Unlock()
		if in == nil {
			return mcp.PermissionDecision{Behavior: "deny", Message: "the question was not answered (run ended)"}, true
		}
		answers[q.Text] = answerText(in)
	}
	var orig struct {
		Questions json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal(input, &orig)
	upd, err := json.Marshal(map[string]any{"questions": orig.Questions, "answers": answers})
	if err != nil {
		return mcp.PermissionDecision{Behavior: "deny", Message: err.Error()}, true
	}
	return mcp.PermissionDecision{Behavior: "allow", UpdatedInput: upd}, true
}

// deliverAnswer hands a RunInput to a waiting question; false = none waits.
func (r *run) deliverAnswer(in *nexusv1.RunInput) bool {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	ch, ok := r.pendingQ[in.GetQuestionId()]
	if !ok && in.GetQuestionId() == "" && len(r.pendingQ) == 1 {
		for _, c := range r.pendingQ {
			ch, ok = c, true
		}
	}
	if !ok {
		return false
	}
	select {
	case ch <- in:
	default:
	}
	return true
}
