package agentloop

import (
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Event constructors shared by Nexus runtimes. Runtimes emit fully-formed
// events with task_id/run_id set; the node fills in seq, actor and timestamp.

// OutputEvent is a streamed chunk of agent text (final=true marks the end of
// the agent's answer; its text may be empty when the answer was streamed).
func OutputEvent(taskID, runID, text string, final bool) *nexusv1.Event {
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_RunOutput{
		RunOutput: &nexusv1.RunOutput{Text: text, IsFinal: final},
	}}
}

// ToolCallEvent announces a tool invocation.
func ToolCallEvent(taskID, runID, callID, tool string, input json.RawMessage, class nexusv1.ActionClass) *nexusv1.Event {
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_ToolCall{
		ToolCall: &nexusv1.ToolCall{CallId: callID, Tool: tool, Input: JSONToStruct(input), ActionClass: class},
	}}
}

// ToolResultEvent reports a tool outcome.
func ToolResultEvent(taskID, runID, callID string, output json.RawMessage, errMsg string, d time.Duration) *nexusv1.Event {
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_ToolResult{
		ToolResult: &nexusv1.ToolResult{CallId: callID, Output: JSONToStruct(output), Error: errMsg, DurationMs: d.Milliseconds()},
	}}
}

// CostEvent records model spend.
func CostEvent(taskID, runID string, c *nexusv1.Cost) *nexusv1.Event {
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_Cost{
		Cost: &nexusv1.CostRecorded{Cost: c},
	}}
}

// ArtifactEvent announces an artifact produced by a tool.
func ArtifactEvent(taskID, runID string, a *nexusv1.Attachment) *nexusv1.Event {
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_Artifact{
		Artifact: &nexusv1.ArtifactAdded{Attachment: a},
	}}
}

// TerminalEvent is the final TaskUpdated of a run. For FAILED runs pass the
// error as errMsg: it becomes the summary and result.error.
func TerminalEvent(taskID, runID string, state nexusv1.TaskState, summary, errMsg string, cost *nexusv1.Cost) *nexusv1.Event {
	t := &nexusv1.Task{Id: taskID, CurrentRunId: runID, State: state, Summary: summary, Cost: cost}
	if errMsg != "" {
		if t.Summary == "" {
			t.Summary = errMsg
		}
		t.Result, _ = structpb.NewStruct(map[string]any{"error": errMsg})
	}
	return &nexusv1.Event{TaskId: taskID, RunId: runID, Body: &nexusv1.Event_TaskUpdated{
		TaskUpdated: &nexusv1.TaskUpdated{Task: t},
	}}
}

// IsTerminal reports whether ev is a TaskUpdated with a terminal state.
func IsTerminal(ev *nexusv1.Event) bool {
	tu := ev.GetTaskUpdated()
	if tu == nil {
		return false
	}
	switch tu.GetTask().GetState() {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
		return true
	}
	return false
}

// JSONToStruct converts a JSON value to a Struct. Objects map directly;
// other values are wrapped as {"value": v}; invalid JSON as {"raw": "..."}.
// Empty input yields nil.
func JSONToStruct(raw json.RawMessage) *structpb.Struct {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		st, _ := structpb.NewStruct(map[string]any{"raw": s})
		return st
	}
	m, ok := v.(map[string]any)
	if !ok {
		m = map[string]any{"value": v}
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		st, _ = structpb.NewStruct(map[string]any{"raw": s})
	}
	return st
}

// AddCost accumulates c into total (tokens and amount when currencies match
// or total has none yet). Model/runtime of the latest cost win when set.
func AddCost(total, c *nexusv1.Cost) {
	if total == nil || c == nil {
		return
	}
	total.InputTokens += c.InputTokens
	total.OutputTokens += c.OutputTokens
	if c.Model != "" {
		total.Model = c.Model
	}
	if c.Runtime != "" {
		total.Runtime = c.Runtime
	}
	if c.Amount != nil {
		if total.Amount == nil {
			total.Amount = &nexusv1.Money{Currency: c.Amount.Currency}
		}
		if total.Amount.Currency == "" {
			total.Amount.Currency = c.Amount.Currency
		}
		if c.Amount.Currency == "" || c.Amount.Currency == total.Amount.Currency {
			total.Amount.MinorUnits += c.Amount.MinorUnits
		}
	}
}
