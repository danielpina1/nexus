package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// ToolSpec describes a tool to models and to the policy engine.
type ToolSpec struct {
	// Name is dot-separated, e.g. "memory.search", "vault.write", "ask_user".
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema object
	// ActionClass drives policy (auto / notify / approve / biometric).
	ActionClass nexusv1.ActionClass `json:"-"`
}

// ToolCall is one invocation.
type ToolCall struct {
	ID      string
	TaskID  string
	RunID   string
	AgentID string
	NodeID  string
	Input   json.RawMessage
}

// ToolResult is the outcome of a call.
type ToolResult struct {
	Output json.RawMessage
	// Artifacts produced (screenshots, files).
	Artifacts []*nexusv1.Attachment
}

// Tool is anything an agent can call.
type Tool interface {
	Spec() ToolSpec
	Invoke(ctx context.Context, call ToolCall) (ToolResult, error)
}

// ToolFunc adapts a function into a Tool.
type ToolFunc struct {
	S ToolSpec
	F func(ctx context.Context, call ToolCall) (ToolResult, error)
}

func (t ToolFunc) Spec() ToolSpec { return t.S }
func (t ToolFunc) Invoke(ctx context.Context, c ToolCall) (ToolResult, error) {
	return t.F(ctx, c)
}

// ToolRegistry is a concurrency-safe set of tools.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry { return &ToolRegistry{tools: map[string]Tool{}} }

func (r *ToolRegistry) Register(t Tool) error {
	name := t.Spec().Name
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("kernel: duplicate tool %q", name)
	}
	r.tools[name] = t
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tool specs sorted by name.
func (r *ToolRegistry) List() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Select returns the tools whose names are in allow (agent allow-list).
// Unknown names are ignored.
func (r *ToolRegistry) Select(allow []string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Tool
	for _, n := range allow {
		if t, ok := r.tools[n]; ok {
			out = append(out, t)
		}
	}
	return out
}
