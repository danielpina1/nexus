package anthrex

import (
	"fmt"
)

// DaemonMsg is one decoded proto::DaemonMsg. Kind is the variant name;
// the fields of that variant are set, the others are zero. Body keeps the
// variant's generic body for anything not lifted into fields.
type DaemonMsg struct {
	Kind string
	Body any

	// Welcome
	DaemonVersion string
	// Welcome, WindowsChanged
	Windows []WindowInfo
	// Created, Snapshot, Output, RemoveDirty, Conversation*
	WindowID uint32
	// Snapshot
	Cols, Rows uint16
	// Snapshot, Output
	Bytes []byte
	// Ack, Error: the request label ("create", "kill", "remove", "hello"…)
	Request string
	// Error, RemoveDirty
	Message string
	// Bye, ConversationGone
	Reason string
	// ConversationSnapshot
	Conversation *Conversation
	// ConversationDelta
	AgentID   *string
	FromRev   uint64
	ToRev     uint64
	TurnPatch []any
	// Run
	Run *RunReply
}

// RunReply is one decoded proto::RunReply.
type RunReply struct {
	Kind      string // Started | Done | Refused | ConfirmNeeded | Snapshot | ToolResult | Triaged | Profile | Stats | TaskDetail | Settings | Doc | Queued
	RequestID *uint64
	Body      any

	RunID     string // Started, ConfirmNeeded, Triaged (may be "")
	State     string // Started
	Request   string // Done, Refused
	Message   string // Done, Refused, Triaged, Queued
	Prompt    string // ConfirmNeeded
	BaseMoved *BaseMovedInfo
	Snapshot  *RunsSnapshot
	GoalID    string // Queued
	Project   string // Queued
	// Profile, Stats, TaskDetail, Settings, Doc: the generic payload.
	Payload any
}

// Err returns an error for a Refused reply (nil otherwise).
func (r *RunReply) Err() error {
	if r == nil {
		return fmt.Errorf("anthrex: no reply")
	}
	if r.Kind == "Refused" {
		return &RefusedError{Request: r.Request, Message: r.Message}
	}
	return nil
}

// RefusedError is a RunReply::Refused.
type RefusedError struct{ Request, Message string }

func (e *RefusedError) Error() string {
	if e.Request == "" {
		return "anthrex refused: " + e.Message
	}
	return "anthrex refused " + e.Request + ": " + e.Message
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func u64(m map[string]any, k string) uint64 {
	switch t := m[k].(type) {
	case int64:
		if t >= 0 {
			return uint64(t)
		}
	case uint64:
		return t
	case float64:
		if t >= 0 {
			return uint64(t)
		}
	}
	return 0
}

func optU64(m map[string]any, k string) *uint64 {
	if _, ok := m[k]; !ok || m[k] == nil {
		return nil
	}
	v := u64(m, k)
	return &v
}

func bytesOf(v any) []byte {
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return []byte(t)
	case []any: // a peer that wrote a seq of u8
		out := make([]byte, 0, len(t))
		for _, x := range t {
			if i, ok := x.(int64); ok {
				out = append(out, byte(i))
			}
		}
		return out
	}
	return nil
}

// DecodeDaemonMsg decodes one frame body (already MessagePack-decoded).
func DecodeDaemonMsg(v any) (*DaemonMsg, error) {
	kind, body, err := SplitVariant(v)
	if err != nil {
		return nil, err
	}
	m := &DaemonMsg{Kind: kind, Body: body}
	f, _ := body.(map[string]any)
	switch kind {
	case "Welcome":
		m.DaemonVersion = str(f, "daemon_version")
		if err := fromGeneric(f["windows"], &m.Windows); err != nil {
			return nil, fmt.Errorf("anthrex: Welcome windows: %w", err)
		}
	case "WindowsChanged":
		if err := fromGeneric(f["windows"], &m.Windows); err != nil {
			return nil, fmt.Errorf("anthrex: WindowsChanged: %w", err)
		}
	case "Created":
		m.WindowID = uint32(u64(f, "window_id"))
	case "Snapshot":
		m.WindowID = uint32(u64(f, "window_id"))
		m.Cols, m.Rows = uint16(u64(f, "cols")), uint16(u64(f, "rows"))
		m.Bytes = bytesOf(f["bytes"])
	case "Output":
		m.WindowID = uint32(u64(f, "window_id"))
		m.Bytes = bytesOf(f["bytes"])
	case "Ack":
		m.Request = str(f, "request")
	case "Error":
		m.Request, m.Message = str(f, "request"), str(f, "message")
	case "RemoveDirty":
		m.WindowID, m.Message = uint32(u64(f, "window_id")), str(f, "message")
	case "Bye":
		m.Reason = str(f, "reason")
	case "ConversationSnapshot":
		m.WindowID = uint32(u64(f, "window_id"))
		var c Conversation
		if err := fromGeneric(f["conversation"], &c); err != nil {
			return nil, fmt.Errorf("anthrex: ConversationSnapshot: %w", err)
		}
		m.Conversation = &c
		m.AgentID = c.AgentID
	case "ConversationDelta":
		m.WindowID = uint32(u64(f, "window_id"))
		if a, ok := f["agent_id"].(string); ok {
			m.AgentID = &a
		}
		m.FromRev, m.ToRev = u64(f, "from_rev"), u64(f, "to_rev")
		m.TurnPatch, _ = f["turns"].([]any)
	case "ConversationGone":
		m.WindowID = uint32(u64(f, "window_id"))
		if a, ok := f["agent_id"].(string); ok {
			m.AgentID = &a
		}
		m.Reason = str(f, "reason")
	case "Run":
		r, err := DecodeRunReply(body)
		if err != nil {
			return nil, err
		}
		m.Run = r
	}
	return m, nil
}

// DecodeRunReply decodes a proto::RunReply value.
func DecodeRunReply(v any) (*RunReply, error) {
	kind, body, err := SplitVariant(v)
	if err != nil {
		return nil, fmt.Errorf("anthrex: run reply: %w", err)
	}
	r := &RunReply{Kind: kind, Body: body}
	if kind == "Snapshot" {
		var s RunsSnapshot
		if err := fromGeneric(body, &s); err != nil {
			return nil, fmt.Errorf("anthrex: runs snapshot: %w", err)
		}
		r.Snapshot = &s
		return r, nil
	}
	f, _ := body.(map[string]any)
	r.RequestID = optU64(f, "request_id")
	switch kind {
	case "Started":
		r.RunID, r.State = str(f, "run_id"), str(f, "state")
	case "Done", "Refused":
		r.Request, r.Message = str(f, "request"), str(f, "message")
	case "ConfirmNeeded":
		r.RunID, r.Prompt = str(f, "run_id"), str(f, "prompt")
		if bm := f["base_moved"]; bm != nil {
			var b BaseMovedInfo
			if err := fromGeneric(bm, &b); err == nil {
				r.BaseMoved = &b
			}
		}
	case "ToolResult":
		r.Message = str(f, "text")
	case "Triaged":
		r.RunID, r.Message = str(f, "run_id"), str(f, "message")
		r.Payload = f["triage"]
	case "Queued":
		r.GoalID, r.Project, r.Message = str(f, "goal_id"), str(f, "project"), str(f, "message")
	case "Profile", "Settings":
		r.Payload = f["reply"]
	case "Stats":
		r.Payload = f["stats"]
	case "TaskDetail":
		r.Payload = f["detail"]
	case "Doc":
		r.Payload = f["doc"]
	}
	return r, nil
}
