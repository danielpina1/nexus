// Package wire holds the NodeLink conventions shared by the hub module
// (internal/nodelink) and the node daemon (internal/node).
//
// # Transport
//
// The node dials NodeLinkService.Connect (bidirectional stream, HTTP/2 only)
// with headers:
//
//	Authorization:   Bearer <node token>
//	X-Nexus-Node-Id: <node id>
//
// The first node message must be NodeHello; the hub answers with HubWelcome.
//
// # Sequencing and resume
//
// Hub → node: every ConnectResponse sent after HubWelcome on a stream is
// implicitly numbered: the first one has seq NodeHello.resume_seq+1, then +1
// for each message (whatever its kind). The node acknowledges cumulatively
// with Ack{seq} after processing. The hub keeps "reliable" messages
// (assign, input, resolution, cancel, envelope, owner_statement — see
// Reliable) in a per-node outbox until acknowledged. On reconnect, outbox
// entries with seq <= NodeHello.resume_seq are dropped (already processed);
// the rest are re-sent first, renumbered from resume_seq+1. Messages queued
// while the node was offline are sent the same way. Control calls (see
// below), tool responses and acks are numbered but never re-sent.
//
// Node → hub: RunEvent.node_seq is explicit and strictly increasing for the
// lifetime of the node process; it starts at the process start time in Unix
// microseconds so that it keeps increasing across node restarts. The hub
// acknowledges with Ack{seq: node_seq} (cumulative) and drops duplicates
// (node_seq <= last processed). HubWelcome.resume_seq carries the last
// node_seq the hub processed for this node; the node drops buffered events
// up to it and re-sends the rest. RunEvents with node_seq == 0 are
// unsequenced control replies (never buffered, never acknowledged).
//
// # Hub tools
//
// ToolRequest{run_id, tool, input} asks the hub to invoke a tool from its
// registry for a run assigned to that node; the tool must be in the agent's
// allow-list. The special tool ToolListHubTools (no run needed) returns the
// hub's tool specs as {"tools": [ToolSpec...]}. Outputs that are not JSON
// objects are wrapped as {"_value": <json>} (see JSONToStruct/StructToJSON).
// Pending calls fail when the link drops; they are not resumed.
//
// # Control calls (hub → node)
//
// The proto has no hub→node request message, so control calls reuse
// RunInput with an empty run_id: question_id = call id (prefix
// ControlCallPrefix), text = node tool name (only ControlTools are accepted),
// choice = JSON input. The node replies with an unsequenced RunEvent
// (node_seq 0) whose event body is a ToolResult{call_id, output, error}.
// NodeService.ListFiles uses this with the node tool "fs.list", confined to
// the node's workspaces directory.
package wire

import (
	"encoding/json"
	"errors"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

const (
	// HeaderNodeID carries the node identity next to the bearer token.
	HeaderNodeID = "X-Nexus-Node-Id"
	// ToolListHubTools is the pseudo-tool that lists hub tool specs.
	ToolListHubTools = "_hub.tools"
	// ControlCallPrefix prefixes control-call ids (RunInput.question_id).
	ControlCallPrefix = "ctl_"
	// ToolFSList is the node tool used by NodeService.ListFiles.
	ToolFSList = "fs.list"
	// ValueKey wraps non-object JSON values in a Struct.
	ValueKey = "_value"
)

// ControlTools are the node tools the hub may invoke via control calls.
// Only read-only tools belong here.
var ControlTools = map[string]bool{ToolFSList: true}

// IsControlInput reports whether a RunInput is a control call.
func IsControlInput(in *nexusv1.RunInput) bool {
	return in.GetRunId() == "" && strings.HasPrefix(in.GetQuestionId(), ControlCallPrefix)
}

// Reliable reports whether a hub→node message is kept until acknowledged and
// re-sent on reconnect.
func Reliable(m *nexusv1.ConnectResponse) bool {
	switch m.GetMsg().(type) {
	case *nexusv1.ConnectResponse_Assign,
		*nexusv1.ConnectResponse_Resolution,
		*nexusv1.ConnectResponse_Cancel,
		*nexusv1.ConnectResponse_Envelope,
		*nexusv1.ConnectResponse_OwnerStatement:
		return true
	case *nexusv1.ConnectResponse_Input:
		return !IsControlInput(m.GetInput())
	}
	return false
}

// JSONToStruct converts JSON into a Struct. Non-object values (and empty
// input → nil) are wrapped as {"_value": v}.
func JSONToStruct(b []byte) (*structpb.Struct, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		m = map[string]any{ValueKey: v}
	}
	return structpb.NewStruct(m)
}

// StructToJSON is the inverse of JSONToStruct. A nil Struct yields nil.
func StructToJSON(s *structpb.Struct) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	m := s.AsMap()
	if v, ok := m[ValueKey]; ok && len(m) == 1 {
		return json.Marshal(v)
	}
	return json.Marshal(m)
}

// ToolSpec is the JSON form of kernel.ToolSpec exchanged via ToolListHubTools.
//
// A spec with a MinLevel is sent with action_class 0 (unspecified, which
// every node gates at APPROVE) and its real class in declared_class, so a
// node that predates min_level never runs it at a looser level than its
// class alone (e.g. vault.rename at NOTIFY).
type ToolSpec struct {
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	ActionClass   int32           `json:"action_class,omitempty"`
	DeclaredClass int32           `json:"declared_class,omitempty"`
	MinLevel      int32           `json:"min_level,omitempty"`
}

// EncodeToolSpecs builds the ToolListHubTools output.
func EncodeToolSpecs(specs []kernel.ToolSpec) ([]byte, error) {
	out := struct {
		Tools []ToolSpec `json:"tools"`
	}{Tools: []ToolSpec{}}
	for _, s := range specs {
		ts := ToolSpec{
			Name: s.Name, Description: s.Description,
			InputSchema: s.InputSchema, ActionClass: int32(s.ActionClass),
		}
		if s.MinLevel != nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED {
			ts.ActionClass, ts.DeclaredClass, ts.MinLevel = 0, int32(s.ActionClass), int32(s.MinLevel)
		}
		out.Tools = append(out.Tools, ts)
	}
	return json.Marshal(out)
}

// DecodeToolSpecs parses the ToolListHubTools output.
func DecodeToolSpecs(b []byte) ([]kernel.ToolSpec, error) {
	var in struct {
		Tools []ToolSpec `json:"tools"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	out := make([]kernel.ToolSpec, 0, len(in.Tools))
	for _, t := range in.Tools {
		if t.Name == "" {
			return nil, errors.New("wire: tool spec without name")
		}
		class := t.ActionClass
		if t.DeclaredClass != 0 {
			class = t.DeclaredClass
		}
		out = append(out, kernel.ToolSpec{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
			ActionClass: nexusv1.ActionClass(class), MinLevel: nexusv1.TrustLevel(t.MinLevel),
		})
	}
	return out, nil
}

// FileEntry is the JSON entry returned by the fs.list tool.
type FileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at,omitempty"` // RFC 3339
}

// FSListOutput is the fs.list tool output.
type FSListOutput struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// TerminalState reports whether a task state is final.
func TerminalState(s nexusv1.TaskState) bool {
	switch s {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED,
		nexusv1.TaskState_TASK_STATE_CANCELLED:
		return true
	}
	return false
}
