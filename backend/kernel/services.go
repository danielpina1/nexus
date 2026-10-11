package kernel

import (
	"context"
	"net/http"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Well-known service names. A module Provides one of these; others Resolve it
// by the matching interface. Swapping an implementation = enabling a different
// module that provides the same name.
const (
	SvcEventBus   = "events"     // EventBus
	SvcStore      = "store"      // Store
	SvcAgents     = "agents"     // AgentCatalog
	SvcTasks      = "tasks"      // TaskEngine
	SvcDispatcher = "dispatcher" // Dispatcher (hub → nodes)
	SvcRouter     = "router"     // Router
	SvcPolicy     = "policy"     // PolicyEngine
	SvcApprovals  = "approvals"  // ApprovalBroker
	SvcNotifier   = "notifier"   // Notifier
	SvcVault      = "vault"      // Vault
	SvcLLM        = "llm"        // LLM
	SvcSecurity   = "security"   // Security
	SvcHubTools   = "hubtools"   // HubToolClient (node side: call hub tools)
)

// EventFilter narrows a subscription.
type EventFilter struct {
	TaskIDs   []string
	InboxOnly bool
}

// EventBus is the ordered, persistent event log with live fan-out.
type EventBus interface {
	// Publish assigns seq + timestamp, persists, and fans out. Returns the stored event.
	Publish(ctx context.Context, ev *nexusv1.Event) (*nexusv1.Event, error)
	// Subscribe replays events with seq > since, then streams live ones until ctx ends.
	// The channel is closed when ctx is done or the subscriber is too slow.
	Subscribe(ctx context.Context, since int64, f EventFilter) (<-chan *nexusv1.Event, error)
	// LastSeq returns the highest stored seq.
	LastSeq(ctx context.Context) (int64, error)
}

// AgentCatalog exposes agent definitions (backed by vault agents/*.md).
type AgentCatalog interface {
	ListAgents(ctx context.Context, includeDisabled bool) ([]*nexusv1.Agent, error)
	// GetAgent returns the parsed agent, its instructions (note body) and raw markdown.
	GetAgent(ctx context.Context, id string) (agent *nexusv1.Agent, instructions, markdown string, err error)
	UpsertAgent(ctx context.Context, markdown, commitMessage string) (*nexusv1.Agent, error)
}

// TaskEngine owns tasks and runs.
type TaskEngine interface {
	CreateTask(ctx context.Context, req *nexusv1.CreateTaskRequest) (*nexusv1.Task, error)
	GetTask(ctx context.Context, id string) (*nexusv1.Task, error)
	ListTasks(ctx context.Context, req *nexusv1.ListTasksRequest) ([]*nexusv1.Task, string, error)
	ChildTasks(ctx context.Context, id string) ([]*nexusv1.Task, error)
	SendMessage(ctx context.Context, req *nexusv1.SendMessageRequest) error
	CancelTask(ctx context.Context, id string) error
	EscalateTask(ctx context.Context, id, hint string) (*nexusv1.Task, error)
	StopAll(ctx context.Context, pauseTriggers bool) (int, error)
	// HandleRunEvent ingests an event produced by a runtime on a node.
	HandleRunEvent(ctx context.Context, nodeID string, ev *nexusv1.RunEvent) error
}

// NodeSelector picks nodes for a run.
type NodeSelector struct {
	NodeID  string
	Tags    []string
	Runtime string
}

// Dispatcher sends work to connected nodes.
type Dispatcher interface {
	Assign(ctx context.Context, sel NodeSelector, a *nexusv1.RunAssignment) (nodeID string, err error)
	SendInput(ctx context.Context, nodeID string, in *nexusv1.RunInput) error
	Resolve(ctx context.Context, nodeID string, r *nexusv1.ApprovalResolution) error
	Cancel(ctx context.Context, nodeID string, c *nexusv1.RunCancel) error
	Nodes(ctx context.Context) ([]*nexusv1.Node, error)
}

// Router classifies free-form input.
type Router interface {
	Route(ctx context.Context, input string, attachments []*nexusv1.Attachment, c *nexusv1.CommandContext) (*nexusv1.Route, error)
}

// PolicyEngine decides the trust level for an action.
type PolicyEngine interface {
	Level(ctx context.Context, agent *nexusv1.Agent, class nexusv1.ActionClass) (nexusv1.TrustLevel, error)
}

// ApprovalBroker creates and resolves approvals.
type ApprovalBroker interface {
	Request(ctx context.Context, a *nexusv1.Approval) (*nexusv1.Approval, error)
	List(ctx context.Context, states []nexusv1.ApprovalState) ([]*nexusv1.Approval, error)
	Decide(ctx context.Context, req *nexusv1.DecideRequest) (*nexusv1.Approval, error)
}

// Notification is a channel-agnostic notification.
type Notification struct {
	DeviceIDs     []string // empty = all devices
	Category      string   // APPROVAL, QUESTION, RESULT, ALERT, BRIEF…
	Title         string
	Body          string
	ThreadID      string // task id
	CollapseID    string
	Data          map[string]string
	TimeSensitive bool
}

// Notifier delivers notifications (APNs, Web Push, Telegram…).
// Multiple notifiers can be composed by a fan-out module.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Vault is the knowledge base.
type Vault interface {
	Read(ctx context.Context, path string) (*nexusv1.Note, error)
	Write(ctx context.Context, path, markdown, commitMessage string) (*nexusv1.Note, error)
	Search(ctx context.Context, query string, types []string, limit int) ([]*nexusv1.SearchHit, error)
	Graph(ctx context.Context, req *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error)
	Capture(ctx context.Context, req *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error)
	// Root returns the vault directory on disk.
	Root() string
}

// ChatMessage is an OpenAI-style message.
type ChatMessage struct {
	Role       string        `json:"role"` // system | user | assistant | tool
	Content    string        `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []LLMToolCall `json:"tool_calls,omitempty"`
}

// LLMToolCall is a model-requested tool invocation.
type LLMToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON
}

// ChatRequest is a provider-neutral chat completion request.
type ChatRequest struct {
	Model       string // alias resolved by the LLM service
	Messages    []ChatMessage
	Tools       []ToolSpec
	MaxTokens   int
	Temperature *float64
	JSON        bool // ask for a JSON object response
}

// ChatResponse is a provider-neutral response.
type ChatResponse struct {
	Message ChatMessage
	Cost    *nexusv1.Cost
}

// LLM talks to models (Unify Gateway, Ollama, any OpenAI-compatible endpoint).
type LLM interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// ChatStream calls onDelta with incremental text; returns the final message.
	ChatStream(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error)
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// HubToolClient lets node-side runtimes call tools that live on the hub
// (memory.search, vault.write, ask_user, request_approval, spawn_agent…).
type HubToolClient interface {
	CallHubTool(ctx context.Context, runID, tool string, input []byte) ([]byte, error)
	HubTools() []ToolSpec
}

// SvcAuth is provided by the security module. The hub server wraps every
// handler with it when present.
const SvcAuth = "auth"

// Authenticator authenticates API requests.
type Authenticator interface {
	// Middleware wraps the API handler (mTLS identity, signed requests, tokens).
	Middleware(next http.Handler) http.Handler
	// Public reports whether a path is reachable without authentication
	// (e.g. pairing endpoints).
	Public(path string) bool
}
