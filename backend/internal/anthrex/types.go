package anthrex

import (
	"encoding/json"
	"fmt"
)

// Typed views of the Anthrex snapshot types Nexus uses. They decode
// leniently: unknown fields are ignored and missing ones keep their zero
// value, so additive protocol changes (new #[serde(default)] fields) do not
// break the client. Enums are plain strings (serde's snake_case names).
// Values whose shape Nexus does not need are kept as json.RawMessage.

// RunsSnapshot is proto::RunsSnapshot.
type RunsSnapshot struct {
	Revision    uint64           `json:"revision"`
	Runs        []RunInfo        `json:"runs"`
	Now         uint64           `json:"now"`
	Proposals   json.RawMessage  `json:"proposals,omitempty"`
	QueuedGoals []QueuedGoalInfo `json:"queued_goals,omitempty"`
}

// Run returns the run with id, or nil.
func (s *RunsSnapshot) Run(id string) *RunInfo {
	if s == nil {
		return nil
	}
	for i := range s.Runs {
		if s.Runs[i].RunID == id {
			return &s.Runs[i]
		}
	}
	return nil
}

// QueuedGoalInfo is proto::QueuedGoalInfo: a goal waiting for its
// repository's profile.
type QueuedGoalInfo struct {
	ID       string     `json:"id"`
	Project  string     `json:"project"`
	Goal     string     `json:"goal"`
	QueuedAt uint64     `json:"queued_at"`
	Setup    SetupState `json:"setup"`
}

// SetupState is proto::SetupState (internally tagged by "state").
type SetupState struct {
	State  string `json:"state"` // reading | checking | needs_review | failed
	Reason string `json:"reason,omitempty"`
}

// Run states (proto::RunState).
const (
	RunAwaitingApproval = "awaiting_approval"
	RunRunning          = "running"
	RunPaused           = "paused"
	RunHalted           = "halted"
	RunComplete         = "complete"
	RunAccepted         = "accepted"
	RunDiscarded        = "discarded"
	RunFailed           = "failed"
	RunPlanning         = "planning"
	RunBrainstorming    = "brainstorming"
	RunSpecifying       = "specifying"
)

// RunStateTerminal reports a state the run never leaves on its own.
func RunStateTerminal(s string) bool {
	return s == RunAccepted || s == RunDiscarded || s == RunFailed
}

// RunInfo is proto::RunInfo (the subset Nexus reads).
type RunInfo struct {
	RunID          string            `json:"run_id"`
	Goal           string            `json:"goal"`
	Title          string            `json:"title,omitempty"`
	Project        string            `json:"project"`
	Root           string            `json:"root"`
	State          string            `json:"state"`
	PausedFrom     *string           `json:"paused_from,omitempty"`
	HaltedReason   *string           `json:"halted_reason,omitempty"`
	HaltUserOnly   bool              `json:"halt_user_only,omitempty"`
	HaltedPhase    *string           `json:"halted_phase,omitempty"`
	ApprovedBy     *string           `json:"approved_by,omitempty"`
	ApprovedAt     *uint64           `json:"approved_at,omitempty"`
	BaseBranch     string            `json:"base_branch"`
	BaseSHA        string            `json:"base_sha"`
	RunBranch      string            `json:"run_branch"`
	RunHead        string            `json:"run_head"`
	BaseMoved      *BaseMovedInfo    `json:"base_moved,omitempty"`
	Revision       uint64            `json:"revision"`
	Tasks          []TaskInfo        `json:"tasks"`
	CriticalPath   []string          `json:"critical_path,omitempty"`
	Attention      []string          `json:"attention,omitempty"`
	ReportPath     string            `json:"report_path"`
	Outcome        *string           `json:"outcome,omitempty"`
	CreatedAt      uint64            `json:"created_at"`
	Usage          *RunUsage         `json:"usage,omitempty"`
	Orchestrator   *OrchestratorInfo `json:"orchestrator,omitempty"`
	Holds          []HoldInfo        `json:"holds,omitempty"`
	Actions        []ActionInfo      `json:"actions,omitempty"`
	Delivery       *DeliveryInfo     `json:"delivery,omitempty"`
	Round          uint32            `json:"round,omitempty"`
	Chain          *string           `json:"chain,omitempty"`
	Design         string            `json:"design,omitempty"` // "" = off
	DocGate        *DocGateInfo      `json:"doc_gate,omitempty"`
	Docs           []DocInfo         `json:"docs,omitempty"`
	ResearchReport *string           `json:"research_report,omitempty"`
	Stages         json.RawMessage   `json:"stages,omitempty"`
}

// DisplayTitle is the title, or the goal when the run has none.
func (r *RunInfo) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Goal
}

// BaseMovedInfo is proto::BaseMovedInfo.
type BaseMovedInfo struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Commits []string `json:"commits"`
	Total   uint32   `json:"total"`
}

// TokenUsage is proto::TokenUsage.
type TokenUsage struct {
	Input      uint64 `json:"input"`
	Output     uint64 `json:"output"`
	CacheRead  uint64 `json:"cache_read"`
	CacheWrite uint64 `json:"cache_write"`
}

// Billable is input + cache_write + output (TokenUsage::billable).
func (u TokenUsage) Billable() uint64 { return u.Input + u.CacheWrite + u.Output }

// Add returns the field-wise sum.
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	return TokenUsage{u.Input + o.Input, u.Output + o.Output, u.CacheRead + o.CacheRead, u.CacheWrite + o.CacheWrite}
}

// RunUsage is proto::RunUsage.
type RunUsage struct {
	Total  TokenUsage            `json:"total"`
	ByRole map[string]TokenUsage `json:"by_role,omitempty"`
}

// Route is proto::Route.
type Route struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Effort  string `json:"effort,omitempty"`
}

// Spend is proto::Spend.
type Spend struct {
	ToolCalls uint32 `json:"tool_calls"`
	Secs      uint64 `json:"secs"`
	Tokens    uint64 `json:"tokens"`
}

// Task states (proto::TaskState).
const (
	TaskPending    = "pending"
	TaskQueued     = "queued"
	TaskPreparing  = "preparing"
	TaskWorking    = "working"
	TaskProof      = "proof"
	TaskCheck      = "check"
	TaskReview     = "review"
	TaskMergeQueue = "merge_queue"
	TaskMerged     = "merged"
	TaskBlocked    = "blocked"
	TaskCancelled  = "cancelled"
	TaskReported   = "reported"
)

// TaskStateFinal reports a task state that ends its work.
func TaskStateFinal(s string) bool {
	return s == TaskMerged || s == TaskCancelled || s == TaskReported
}

// TaskInfo is proto::TaskInfo (the subset Nexus reads).
type TaskInfo struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Epic         *string          `json:"epic,omitempty"`
	Kind         string           `json:"kind"`
	Size         string           `json:"size"`
	State        string           `json:"state"`
	Block        *BlockInfo       `json:"block,omitempty"`
	Route        Route            `json:"route"`
	SpentTotal   Spend            `json:"spent_total"`
	Branch       string           `json:"branch"`
	Worktree     string           `json:"worktree"`
	Head         *string          `json:"head,omitempty"`
	MergeCommit  *string          `json:"merge_commit,omitempty"`
	Rounds       []AgentRoundInfo `json:"rounds,omitempty"`
	LastCheck    *CheckInfo       `json:"last_check,omitempty"`
	DeciderUsage *TokenUsage      `json:"decider_usage,omitempty"`
	Diff         *DiffStats       `json:"diff,omitempty"`
	Hold         *string          `json:"hold,omitempty"`
	Activity     *string          `json:"activity,omitempty"`
	Stage        uint16           `json:"stage,omitempty"`
	Round        uint32           `json:"round,omitempty"`
	Actions      []ActionInfo     `json:"actions,omitempty"`
}

// Role is the role of the task's latest agent round ("worker", "reviewer"…),
// or "" before any round.
func (t *TaskInfo) Role() string {
	if n := len(t.Rounds); n > 0 {
		return t.Rounds[n-1].Role
	}
	return ""
}

// Usage sums the task's agent rounds and decider usage.
func (t *TaskInfo) Usage() TokenUsage {
	var u TokenUsage
	for _, r := range t.Rounds {
		u = u.Add(r.Usage)
	}
	if t.DeciderUsage != nil {
		u = u.Add(*t.DeciderUsage)
	}
	return u
}

// BlockInfo is proto::BlockInfo.
type BlockInfo struct {
	Reason   string `json:"reason"`
	Text     string `json:"text"`
	UserOnly bool   `json:"user_only,omitempty"`
}

// AgentRoundInfo is proto::AgentRoundInfo (subset).
type AgentRoundInfo struct {
	Role      string     `json:"role"`
	Session   uint32     `json:"session"`
	Round     uint32     `json:"round"`
	WindowID  *uint32    `json:"window_id,omitempty"`
	Route     Route      `json:"route"`
	StartedAt uint64     `json:"started_at"`
	EndedAt   *uint64    `json:"ended_at,omitempty"`
	ToolCalls uint32     `json:"tool_calls"`
	Usage     TokenUsage `json:"usage"`
}

// CheckInfo is proto::CheckInfo (subset).
type CheckInfo struct {
	At       uint64 `json:"at"`
	OK       bool   `json:"ok"`
	TimedOut bool   `json:"timed_out"`
	Summary  string `json:"summary"`
}

// DiffStats is proto::DiffStats.
type DiffStats struct {
	Files   uint32 `json:"files"`
	Hunks   uint32 `json:"hunks"`
	Added   uint32 `json:"added"`
	Removed uint32 `json:"removed"`
}

// OrchestratorInfo is proto::OrchestratorInfo (subset).
type OrchestratorInfo struct {
	Route         Route    `json:"route"`
	WindowID      *uint32  `json:"window_id,omitempty"`
	Live          bool     `json:"live"`
	PlanSubmitted bool     `json:"plan_submitted"`
	Summary       *string  `json:"summary,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	Ask           *AskInfo `json:"ask,omitempty"`
	Stuck         *struct {
		Kind  string  `json:"kind"`
		Since *uint64 `json:"since,omitempty"`
	} `json:"stuck,omitempty"`
}

// AskInfo is proto::AskInfo: the orchestrator's pending ask_user.
type AskInfo struct {
	ID       uint64   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
	AskedAt  uint64   `json:"asked_at"`
}

// Hold states (proto::HoldState).
const (
	HoldDrafting = "drafting"
	HoldAwaiting = "awaiting"
	HoldApproved = "approved"
	HoldRejected = "rejected"
	HoldMoot     = "moot"
)

// HoldInfo is proto::HoldInfo.
type HoldInfo struct {
	ID   string `json:"id"`
	Kind struct {
		Kind  string   `json:"kind"` // promotion | epic | fix
		Epic  string   `json:"epic,omitempty"`
		Stage uint16   `json:"stage,omitempty"`
		Paths []string `json:"paths,omitempty"`
	} `json:"kind"`
	State     string   `json:"state"`
	Tasks     []string `json:"tasks"`
	CreatedAt uint64   `json:"created_at"`
}

// ActionInfo is proto::ActionInfo: one entry of a node's action menu.
type ActionInfo struct {
	Kind struct {
		Kind  string `json:"kind"`
		Hold  string `json:"hold,omitempty"`
		Stage uint16 `json:"stage,omitempty"`
	} `json:"kind"`
	Label       string          `json:"label"`
	Effect      string          `json:"effect"`
	Needs       json.RawMessage `json:"needs,omitempty"`
	Destructive bool            `json:"destructive"`
	RefusedWhy  *string         `json:"refused_why,omitempty"`
}

// DeliveryInfo is proto::DeliveryInfo (pr mode only; nil in local mode).
type DeliveryInfo struct {
	Mode       string `json:"mode"`
	Remote     string `json:"remote"`
	Repo       string `json:"repo"`
	Watching   bool   `json:"watching"`
	Delivering bool   `json:"delivering"`
}

// DocGateInfo is proto::DocGateInfo: the open design gate.
type DocGateInfo struct {
	Kind           string          `json:"kind"` // brainstorm | spec | plan
	Version        uint32          `json:"version"`
	Revising       *string         `json:"revising,omitempty"`
	NotReviewed    *string         `json:"not_reviewed,omitempty"`
	ChangesSummary []string        `json:"changes_summary,omitempty"`
	Disputed       json.RawMessage `json:"disputed,omitempty"`
}

// DocInfo is proto::DocInfo.
type DocInfo struct {
	Kind         string          `json:"kind"`
	Version      uint32          `json:"version"`
	Author       json.RawMessage `json:"author"`
	Reason       string          `json:"reason"`
	Bytes        uint64          `json:"bytes"`
	Requirements []string        `json:"requirements,omitempty"`
}

// WindowInfo is proto::WindowInfo.
type WindowInfo struct {
	ID             uint32  `json:"id"`
	Name           string  `json:"name"`
	Runtime        string  `json:"runtime"`
	Cwd            string  `json:"cwd"`
	Project        string  `json:"project"`
	Worktree       *string `json:"worktree,omitempty"`
	Branch         *string `json:"branch,omitempty"`
	Status         string  `json:"status"`
	Tool           *string `json:"tool,omitempty"`
	SinceSecs      uint64  `json:"since_secs"`
	LastOutputSecs uint64  `json:"last_output_secs"`
	SessionID      *string `json:"session_id,omitempty"`
	Model          *string `json:"model,omitempty"`
	Exit           *struct {
		Code   *int32 `json:"code,omitempty"`
		Reason string `json:"reason"`
	} `json:"exit,omitempty"`
	Kind        string  `json:"kind,omitempty"` // pty | headless
	Run         *RunRef `json:"run,omitempty"`
	Placeholder bool    `json:"placeholder,omitempty"`
}

// RunRef is proto::RunRef.
type RunRef struct {
	RunID   string  `json:"run_id"`
	TaskID  *string `json:"task_id,omitempty"`
	Role    string  `json:"role"`
	Session uint32  `json:"session"`
}

// Conversation is proto::Conversation. Blocks are kept generic: their
// variant (text, tool_call, subagent_spawn, notice) is the single map key.
type Conversation struct {
	WindowID     uint32  `json:"window_id"`
	AgentID      *string `json:"agent_id,omitempty"`
	SessionID    *string `json:"session_id,omitempty"`
	Runtime      string  `json:"runtime"`
	Rev          uint64  `json:"rev"`
	Degraded     *string `json:"degraded,omitempty"`
	DroppedTurns uint32  `json:"dropped_turns"`
	Turns        []Turn  `json:"turns"`
}

// Turn is proto::Turn.
type Turn struct {
	ID         uint64           `json:"id"`
	Role       string           `json:"role"`
	AtUnixSecs uint64           `json:"at_unix_secs"`
	State      string           `json:"state"`
	Blocks     []map[string]any `json:"blocks"`
}

// fromGeneric converts a decoded generic value into a typed struct through
// JSON, leniently. []byte values become base64 strings on the way.
func fromGeneric(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("anthrex: re-encode: %w", err)
	}
	return json.Unmarshal(b, out)
}

// FromGeneric is fromGeneric for callers outside the package.
func FromGeneric(v any, out any) error { return fromGeneric(v, out) }
