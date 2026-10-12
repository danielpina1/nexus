package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// RunSpec is everything a runtime needs to execute one run on a node.
type RunSpec struct {
	Assignment *nexusv1.RunAssignment
	// Workdir is a fresh directory owned by this run.
	Workdir string
	// Tools available to the run: node-local tools plus hub tools proxied
	// over the node link, already filtered by the agent's allow-list.
	Tools []Tool
	// Env holds secrets/config resolved on the node.
	Env map[string]string
	// Gate is the run's late-bound approval gate (may be nil). Runtimes that
	// gate actions bind their gate with Gate.Set at Start, so run tools
	// (e.g. a browser checkout click) can ask for approvals through the same
	// channel that Run.Resolve serves.
	Gate *RunGate
	// Emit sends a side-channel event of the run (artifacts, notices) to the
	// hub, outside the runtime's own event stream (may be nil).
	Emit func(*nexusv1.Event)
}

// RunEventKind is a shorthand used by runtimes when emitting events.
// Runtimes emit fully-formed *nexusv1.Event values; the node fills in
// seq, ids, actor and timestamp.
type Run interface {
	// Events is closed when the run ends. The last event should be a
	// TaskUpdated with a terminal state, or the node synthesises one.
	Events() <-chan *nexusv1.Event
	// Send delivers a user message or an answer to an ask_user question.
	Send(ctx context.Context, in *nexusv1.RunInput) error
	// Resolve delivers an approval decision (already verified by the node).
	Resolve(ctx context.Context, res *nexusv1.ApprovalResolution) error
	Cancel(ctx context.Context) error
}

// RuntimeInfo is reported to the hub so the router knows where agents can run.
type RuntimeCapabilities struct {
	Streaming     bool // emits incremental output
	Steering      bool // accepts Send mid-run
	NativeTools   bool // brings its own tools (Claude Code, Codex)
	ApprovalHooks bool // can pause for approvals
}

// Runtime executes agents of one kind (claude-code, codex, openai, ollama, shell, browser…).
type Runtime interface {
	Name() string
	Capabilities() RuntimeCapabilities
	// Available probes the machine (binary on PATH, endpoint reachable…).
	Available(ctx context.Context) (version string, ok bool)
	Start(ctx context.Context, spec RunSpec) (Run, error)
}

// RuntimeRegistry holds runtimes on a node.
type RuntimeRegistry struct {
	mu sync.RWMutex
	rt map[string]Runtime
}

func NewRuntimeRegistry() *RuntimeRegistry { return &RuntimeRegistry{rt: map[string]Runtime{}} }

func (r *RuntimeRegistry) Register(rt Runtime) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.rt[rt.Name()]; dup {
		return fmt.Errorf("kernel: duplicate runtime %q", rt.Name())
	}
	r.rt[rt.Name()] = rt
	return nil
}

func (r *RuntimeRegistry) Get(name string) (Runtime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.rt[name]
	return rt, ok
}

func (r *RuntimeRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.rt))
	for n := range r.rt {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ToolInput decodes a tool call input into v.
func ToolInput(c ToolCall, v any) error {
	if len(c.Input) == 0 {
		return nil
	}
	return json.Unmarshal(c.Input, v)
}

// ToolOutput encodes v as a tool result.
func ToolOutput(v any) (ToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Output: b}, nil
}

// ConcurrencyLimited is implemented by runtimes that cap how many of their
// runs execute at once on a node (e.g. subscription CLIs, to stay clear of
// rate limits). The node queues runs beyond the limit. 0 = unlimited.
type ConcurrencyLimited interface {
	MaxConcurrent() int
}

// Machine-readable failure kinds a runtime may put in the terminal
// TaskUpdated's result as result.error_kind. The hub's task engine reacts to
// them (rate_limited: retry on the next runtime of the agent's list).
const (
	// ErrorKindRateLimited: the model/subscription usage or rate limit was hit.
	ErrorKindRateLimited = "rate_limited"
	// ErrorKindAuth: the runtime is not logged in / not authorised.
	ErrorKindAuth = "auth"
)

// RuntimeAuto is the agent runtime value that lets the hub pick a runtime
// from policies/models.md `runtimes:` (per tier) or the agent's `runtimes:`.
const RuntimeAuto = "auto"
