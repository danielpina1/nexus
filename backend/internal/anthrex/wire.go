package anthrex

import (
	"errors"
	"fmt"
	"sort"
)

// ProtoVersion is the Anthrex wire protocol this client speaks
// (proto::PROTO_VERSION). The daemon refuses any other version at Hello.
const ProtoVersion = 21

// SupportedProtoVersions lists the versions this client can decode.
var SupportedProtoVersions = []uint32{21}

// serde's externally tagged enum encoding, as rmp_serde (to_vec_named)
// writes it:
//
//	unit variant          → "Name"
//	newtype variant       → {"Name": value}
//	struct variant        → {"Name": {"field": …}}
//
// Variant names are the Rust names unless the enum has rename_all
// (ClientMsg, DaemonMsg, RunRequest, RunReply have none: PascalCase).

// unit returns a unit variant.
func unit(name string) any { return name }

// variant returns a newtype or struct variant.
func variant(name string, body any) any { return map[string]any{name: body} }

// SplitVariant decodes an externally tagged enum value into its variant name
// and body (nil for a unit variant).
func SplitVariant(v any) (string, any, error) {
	switch t := v.(type) {
	case string:
		return t, nil, nil
	case map[string]any:
		if len(t) != 1 {
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return "", nil, fmt.Errorf("anthrex: enum value with %d keys %v", len(t), keys)
		}
		for k, b := range t {
			return k, b, nil
		}
	}
	return "", nil, fmt.Errorf("anthrex: %T is not an enum value", v)
}

// ---------------------------------------------------------------- ClientMsg

// ClientKind values (proto::ClientKind, lowercase).
const (
	ClientCLI = "cli"
	ClientTUI = "tui"
	ClientMCP = "mcp"
)

// MsgHello is ClientMsg::Hello.
func MsgHello(version uint32, client string) any {
	return variant("Hello", map[string]any{"proto_version": version, "client": client})
}

// MsgListWindows is ClientMsg::ListWindows.
func MsgListWindows() any { return unit("ListWindows") }

// WindowSpec is proto::WindowSpec.
type WindowSpec struct {
	Name           string `json:"name,omitempty"`
	Runtime        string `json:"runtime"` // claude | codex | shell
	Cwd            string `json:"cwd"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	Model          string `json:"model,omitempty"`
	InitialPrompt  string `json:"initial_prompt,omitempty"`
}

func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MsgCreateWindow is ClientMsg::CreateWindow.
func MsgCreateWindow(spec WindowSpec, cols, rows uint16) any {
	return variant("CreateWindow", map[string]any{
		"spec": map[string]any{
			"name":            optStr(spec.Name),
			"runtime":         spec.Runtime,
			"cwd":             spec.Cwd,
			"worktree_branch": optStr(spec.WorktreeBranch),
			"model":           optStr(spec.Model),
			"initial_prompt":  optStr(spec.InitialPrompt),
		},
		"cols": cols, "rows": rows,
	})
}

// MsgSubscribe is ClientMsg::Subscribe (PTY view of one window).
func MsgSubscribe(windowID uint32, cols, rows uint16) any {
	return variant("Subscribe", map[string]any{"window_id": windowID, "cols": cols, "rows": rows})
}

// MsgUnsubscribe is ClientMsg::Unsubscribe.
func MsgUnsubscribe() any { return unit("Unsubscribe") }

// MsgInput is ClientMsg::Input (bytes travel as MessagePack bin).
func MsgInput(windowID uint32, b []byte) any {
	if b == nil {
		b = []byte{}
	}
	return variant("Input", map[string]any{"window_id": windowID, "bytes": b})
}

// MsgResize is ClientMsg::Resize.
func MsgResize(windowID uint32, cols, rows uint16) any {
	return variant("Resize", map[string]any{"window_id": windowID, "cols": cols, "rows": rows})
}

// MsgKill is ClientMsg::Kill.
func MsgKill(windowID uint32) any { return variant("Kill", map[string]any{"window_id": windowID}) }

// MsgRemove is ClientMsg::Remove.
func MsgRemove(windowID uint32, removeWorktree, force bool) any {
	return variant("Remove", map[string]any{"window_id": windowID, "remove_worktree": removeWorktree, "force": force})
}

// MsgRestart is ClientMsg::Restart.
func MsgRestart(windowID uint32) any {
	return variant("Restart", map[string]any{"window_id": windowID})
}

// MsgSubscribeConversation is ClientMsg::SubscribeConversation.
func MsgSubscribeConversation(windowID uint32, agentID string, fromRev *uint64) any {
	var rev any
	if fromRev != nil {
		rev = *fromRev
	}
	return variant("SubscribeConversation", map[string]any{"window_id": windowID, "agent_id": optStr(agentID), "from_rev": rev})
}

// MsgUnsubscribeConversation is ClientMsg::UnsubscribeConversation.
func MsgUnsubscribeConversation(windowID uint32, agentID string) any {
	return variant("UnsubscribeConversation", map[string]any{"window_id": windowID, "agent_id": optStr(agentID)})
}

// MsgRunTagged is ClientMsg::RunTagged: a run request whose reply echoes id.
func MsgRunTagged(id uint64, request any) any {
	return variant("RunTagged", map[string]any{"id": id, "request": request})
}

// ---------------------------------------------------------------- RunRequest

// StartGoalRequest is RunRequest::StartGoal.
type StartGoalRequest struct {
	Goal string `json:"goal"`
	Dir  string `json:"dir"`
	// Yes approves the plan at once (no plan gate).
	Yes              bool   `json:"yes"`
	TrustProject     bool   `json:"trust_project"`
	UnconfinedChecks bool   `json:"unconfined_checks"`
	Orchestrator     string `json:"orchestrator,omitempty"` // "claude", "codex:gpt-5" …
	Delivery         string `json:"delivery,omitempty"`     // local | pr
	ContinueFrom     string `json:"continue_from,omitempty"`
	Design           string `json:"design,omitempty"` // full | off
}

// ParseOrchestrator turns "<runtime>[:<model>]" into an OrchestratorChoice
// value (nil for "").
func ParseOrchestrator(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	rt, model := s, ""
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			rt, model = s[:i], s[i+1:]
			break
		}
	}
	if rt != "claude" && rt != "codex" {
		return nil, fmt.Errorf("anthrex: orchestrator runtime %q (want claude or codex)", rt)
	}
	return map[string]any{"runtime": rt, "model": optStr(model)}, nil
}

func checkEnum(field, v string, allowed ...string) error {
	if v == "" {
		return nil
	}
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("anthrex: %s %q (want one of %v)", field, v, allowed)
}

// ReqStartGoal builds RunRequest::StartGoal.
func ReqStartGoal(r StartGoalRequest) (any, error) {
	if r.Goal == "" {
		return nil, errors.New("anthrex: empty goal")
	}
	if r.Dir == "" {
		return nil, errors.New("anthrex: goal needs a repository dir")
	}
	if err := checkEnum("delivery", r.Delivery, "local", "pr"); err != nil {
		return nil, err
	}
	if err := checkEnum("design", r.Design, "full", "off"); err != nil {
		return nil, err
	}
	orch, err := ParseOrchestrator(r.Orchestrator)
	if err != nil {
		return nil, err
	}
	return variant("StartGoal", map[string]any{
		"goal": r.Goal, "dir": r.Dir, "yes": r.Yes, "trust_project": r.TrustProject,
		"unconfined_checks": r.UnconfinedChecks, "orchestrator": orch,
		"delivery": optStr(r.Delivery), "continue_from": optStr(r.ContinueFrom), "design": optStr(r.Design),
	}), nil
}

func runIDReq(name, runID string) any { return variant(name, map[string]any{"run_id": runID}) }

// ReqApprove is RunRequest::Approve (the plan gate).
func ReqApprove(runID string) any { return runIDReq("Approve", runID) }

// ReqReject is RunRequest::Reject (the plan gate).
func ReqReject(runID string) any { return runIDReq("Reject", runID) }

// ReqCancel is RunRequest::Cancel.
func ReqCancel(runID string) any { return runIDReq("Cancel", runID) }

// ReqResume is RunRequest::Resume.
func ReqResume(runID string, rebaseline bool) any {
	return variant("Resume", map[string]any{"run_id": runID, "rebaseline": rebaseline})
}

// ReqRetry is RunRequest::Retry.
func ReqRetry(runID, taskID string) any {
	return variant("Retry", map[string]any{"run_id": runID, "task_id": taskID})
}

// ReqOverride is RunRequest::Override.
func ReqOverride(runID, taskID, reason string) any {
	return variant("Override", map[string]any{"run_id": runID, "task_id": taskID, "reason": reason})
}

// Finish actions (proto::FinishAction, lowercase).
const (
	FinishAccept  = "accept"
	FinishDiscard = "discard"
)

// ReqFinish is RunRequest::Finish. confirm "" sends None (the daemon then
// answers ConfirmNeeded); the second call confirms "<run id>" or
// "<run id>@<moved base sha>".
func ReqFinish(runID, action, confirm string) any {
	return variant("Finish", map[string]any{"run_id": runID, "action": action, "confirm": optStr(confirm)})
}

// ReqList is RunRequest::List (answered by an untagged Snapshot).
func ReqList() any { return unit("List") }

// ReqSubscribe is RunRequest::Subscribe: the current snapshot, then every
// later one, until Unsubscribe or the connection ends.
func ReqSubscribe() any { return unit("Subscribe") }

// ReqUnsubscribe is RunRequest::Unsubscribe.
func ReqUnsubscribe() any { return unit("Unsubscribe") }

// ReqApproveHold is RunRequest::ApproveHold.
func ReqApproveHold(runID, hold string) any {
	return variant("ApproveHold", map[string]any{"run_id": runID, "hold": hold})
}

// ReqRejectHold is RunRequest::RejectHold.
func ReqRejectHold(runID, hold string) any {
	return variant("RejectHold", map[string]any{"run_id": runID, "hold": hold})
}

// ReqTaskDetail is RunRequest::TaskDetail.
func ReqTaskDetail(runID, taskID string) any {
	return variant("TaskDetail", map[string]any{"run_id": runID, "task_id": taskID})
}

// ReqAnswerAsk is RunRequest::AnswerAsk; choice nil = answered in the window.
func ReqAnswerAsk(runID string, ask uint64, choice *uint32) any {
	var c any
	if choice != nil {
		c = *choice
	}
	return variant("AnswerAsk", map[string]any{"run_id": runID, "ask": ask, "choice": c})
}

// Message kinds (proto::MessageKind).
const (
	MessageInfo        = "info"
	MessageChange      = "change"
	MessageStopAndWait = "stop_and_wait"
)

// MessageTarget encodes proto::MessageTarget from the CLI's spelling:
// "t1,t2" (task ids), "stage:<n>" or "running".
func MessageTarget(to string) (any, error) {
	switch {
	case to == "running":
		return "running", nil
	case len(to) > 6 && to[:6] == "stage:":
		return to, nil
	case to == "":
		return nil, errors.New("anthrex: empty message target")
	}
	var ids []any
	start := 0
	for i := 0; i <= len(to); i++ {
		if i == len(to) || to[i] == ',' {
			if id := to[start:i]; id != "" {
				ids = append(ids, id)
			}
			start = i + 1
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("anthrex: empty message target")
	}
	return ids, nil
}

// EditMessage is PlanEdit::Message (internally tagged by "op").
func EditMessage(to, text, kind string) (any, error) {
	t, err := MessageTarget(to)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = MessageInfo
	}
	if err := checkEnum("message kind", kind, MessageInfo, MessageChange, MessageStopAndWait); err != nil {
		return nil, err
	}
	return map[string]any{"op": "message", "to": t, "text": text, "kind": kind}, nil
}

// EditAnswer is PlanEdit::Answer (a blocked task's question).
func EditAnswer(taskID, text string) any {
	return map[string]any{"op": "answer", "task_id": taskID, "text": text}
}

// ReqEdit is RunRequest::Edit.
func ReqEdit(runID string, edits []any, submit bool) any {
	if edits == nil {
		edits = []any{}
	}
	return variant("Edit", map[string]any{"run_id": runID, "edits": edits, "submit": submit})
}

// Doc gate kinds (proto::DocGateKind) and document kinds (proto::DocKind).
const (
	GateBrainstorm = "brainstorm"
	GateSpec       = "spec"
	GatePlan       = "plan"
)

// DocGateAction is proto::DocGateAction.
type DocGateAction struct {
	// Kind: approve | changes | edit | rethink | back | reject.
	Kind    string
	Version *uint32 // approve: the version reviewed
	Note    string  // changes, rethink, back
	Review  bool    // changes: send the revision to the reviewer again
	Text    string  // edit: the next version's full text
}

// Value encodes the action. An approve without version is the bare string
// "approve" (design.rs' custom Serialize), Reject is a unit variant.
func (a DocGateAction) Value() (any, error) {
	switch a.Kind {
	case "approve":
		if a.Version == nil {
			return "approve", nil
		}
		return variant("approve", map[string]any{"version": *a.Version}), nil
	case "changes":
		return variant("changes", map[string]any{"note": a.Note, "review": a.Review}), nil
	case "edit":
		return variant("edit", map[string]any{"text": a.Text}), nil
	case "rethink":
		return variant("rethink", map[string]any{"note": a.Note}), nil
	case "back":
		return variant("back", map[string]any{"note": a.Note}), nil
	case "reject":
		return "reject", nil
	}
	return nil, fmt.Errorf("anthrex: doc gate action %q", a.Kind)
}

// ReqDocGate is RunRequest::DocGate.
func ReqDocGate(run, kind string, action DocGateAction) (any, error) {
	if err := checkEnum("gate", kind, GateBrainstorm, GateSpec, GatePlan); err != nil {
		return nil, err
	}
	a, err := action.Value()
	if err != nil {
		return nil, err
	}
	return variant("DocGate", map[string]any{"run": run, "kind": kind, "action": a}), nil
}

// ReqShowDoc is RunRequest::ShowDoc (kind: brainstorm_draft | brainstorm |
// spec | plan; version nil = latest).
func ReqShowDoc(run, kind string, version *uint32) any {
	var v any
	if version != nil {
		v = *version
	}
	return variant("ShowDoc", map[string]any{"run": run, "kind": kind, "version": v, "diff": false, "findings": false})
}

// ReqStats is RunRequest::Stats (read-only summary of a repository's history).
func ReqStats(dir string) any {
	return variant("Stats", map[string]any{"dir": dir, "apply": []any{}, "dismiss": []any{}, "read_only": true})
}

// ReqProfileStatus is RunRequest::Profile(ProfileRequest::Status).
func ReqProfileStatus(dir string) any {
	return variant("Profile", variant("Status", map[string]any{"dir": dir}))
}

// ReqPromote is RunRequest::Promote.
func ReqPromote(runID, orchestrator string) (any, error) {
	o, err := ParseOrchestrator(orchestrator)
	if err != nil {
		return nil, err
	}
	return variant("Promote", map[string]any{"run_id": runID, "orchestrator": o}), nil
}
