package main

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// enumName turns TASK_STATE_RUNNING into "running".
func enumName(s, prefix string) string {
	s = strings.TrimPrefix(s, prefix)
	if s == "UNSPECIFIED" {
		return "-"
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}

func stateName(s nexusv1.TaskState) string { return enumName(s.String(), "TASK_STATE_") }
func tierName(t nexusv1.Tier) string       { return enumName(t.String(), "TIER_") }

func terminal(s nexusv1.TaskState) bool {
	switch s {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED, nexusv1.TaskState_TASK_STATE_CANCELLED:
		return true
	}
	return false
}

func clock(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "--:--:--"
	}
	return ts.AsTime().Local().Format("15:04:05")
}

func ago(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	d := time.Since(ts.AsTime())
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return ts.AsTime().Local().Format("2006-01-02")
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func structJSON(s *structpb.Struct) string {
	if s == nil {
		return "{}"
	}
	b, err := protojson.Marshal(s)
	if err != nil {
		return "?"
	}
	return string(b)
}

func costString(c *nexusv1.Cost) string {
	if c == nil {
		return ""
	}
	var parts []string
	if m := c.GetAmount(); m != nil && m.GetMinorUnits() != 0 {
		parts = append(parts, fmt.Sprintf("%.2f %s", float64(m.GetMinorUnits())/100, m.GetCurrency()))
	}
	if c.GetInputTokens()+c.GetOutputTokens() > 0 {
		parts = append(parts, fmt.Sprintf("%d→%d tokens", c.GetInputTokens(), c.GetOutputTokens()))
	}
	if c.GetModel() != "" {
		parts = append(parts, c.GetModel())
	}
	return strings.Join(parts, ", ")
}

// summarize renders an event body as one short line (without seq/time/task).
func summarize(ev *nexusv1.Event) (kind, text string) {
	switch b := ev.GetBody().(type) {
	case *nexusv1.Event_TaskUpdated:
		t := b.TaskUpdated.GetTask()
		return "task", fmt.Sprintf("%s %s", stateName(t.GetState()), truncate(or(t.GetTitle(), t.GetSummary()), 60))
	case *nexusv1.Event_RunOutput:
		return "output", truncate(b.RunOutput.GetText(), 100)
	case *nexusv1.Event_ToolCall:
		return "tool", fmt.Sprintf("→ %s %s", b.ToolCall.GetTool(), truncate(structJSON(b.ToolCall.GetInput()), 80))
	case *nexusv1.Event_ToolResult:
		if e := b.ToolResult.GetError(); e != "" {
			return "tool", fmt.Sprintf("✗ %s (%dms)", truncate(e, 80), b.ToolResult.GetDurationMs())
		}
		return "tool", fmt.Sprintf("✓ %s (%dms)", truncate(structJSON(b.ToolResult.GetOutput()), 80), b.ToolResult.GetDurationMs())
	case *nexusv1.Event_Question:
		q := b.Question
		s := q.GetText()
		if len(q.GetChoices()) > 0 {
			s += " [" + strings.Join(q.GetChoices(), " / ") + "]"
		}
		return "question", truncate(s, 100)
	case *nexusv1.Event_Approval:
		a := b.Approval
		return "approval", fmt.Sprintf("%s %s (%s)", enumName(a.GetState().String(), "APPROVAL_STATE_"), truncate(a.GetTitle(), 70), a.GetId())
	case *nexusv1.Event_Cost:
		return "cost", costString(b.Cost.GetCost())
	case *nexusv1.Event_Artifact:
		at := b.Artifact.GetAttachment()
		return "artifact", fmt.Sprintf("%s (%s)", at.GetName(), at.GetMimeType())
	case *nexusv1.Event_NodeStatus:
		n := b.NodeStatus
		state := "offline"
		if n.GetOnline() {
			state = "online"
		}
		return "node", fmt.Sprintf("%s %s cpu %.0f%% mem %.0f%% runs %d", n.GetNodeId(), state, n.GetCpuPercent(), n.GetMemoryPercent(), n.GetActiveRuns())
	case *nexusv1.Event_Notice:
		return "notice", truncate(b.Notice.GetTitle()+": "+b.Notice.GetBody(), 100)
	case *nexusv1.Event_UserMessage:
		return "user", truncate(b.UserMessage.GetText(), 100)
	}
	return "event", ""
}

// eventLine is the compact one-line form used by `nexus events`.
func eventLine(ev *nexusv1.Event) string {
	kind, text := summarize(ev)
	return fmt.Sprintf("%6d  %s  %-26s  %-8s  %s", ev.GetSeq(), clock(ev.GetAt()), or(ev.GetTaskId(), "-"), kind, text)
}
