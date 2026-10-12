package kernel

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// SvcRunToolProviders is the node-side registry of RunToolProviders
// (*RunToolProviderRegistry). Use RunToolProvidersOf to get or create it.
const SvcRunToolProviders = "runtoolproviders"

// RunToolProvider contributes run-scoped tools to every run on a node,
// whatever its runtime (e.g. the browser module gives Claude Code, Codex and
// native agents browser.* tools backed by a per-run browser session).
//
// RunTools is called by the node's run manager before the runtime starts.
// spec.Tools already holds the run's node and hub tools (allow-listed);
// spec.Gate is the run's late-bound approval gate (set by the runtime when
// it starts) and spec.Emit sends side-channel events (artifacts, notices).
// The provider returns a RunToolSet (empty when the run does not use its
// tools). A returned tool whose name equals one of spec.Tools replaces it
// (e.g. a wrapper that observes shops.get); other tools are appended when
// the agent's allow-list permits them (see ToolAllowed).
type RunToolProvider interface {
	Name() string
	RunTools(ctx context.Context, spec RunSpec) (RunToolSet, error)
}

// RunToolSet is what a RunToolProvider contributes to one run.
type RunToolSet struct {
	Tools []Tool
	// Instructions are appended to the agent's instructions when at least
	// one of Tools is added (usage guidance, e.g. how to drive the browser).
	Instructions string
	// Cleanup is called once when the run ends (may be nil).
	Cleanup func()
}

// RunToolProviderRegistry holds the providers of a node.
type RunToolProviderRegistry struct {
	mu sync.RWMutex
	p  map[string]RunToolProvider
}

// NewRunToolProviderRegistry returns an empty registry.
func NewRunToolProviderRegistry() *RunToolProviderRegistry {
	return &RunToolProviderRegistry{p: map[string]RunToolProvider{}}
}

// Register adds a provider; names must be unique.
func (r *RunToolProviderRegistry) Register(p RunToolProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.p[p.Name()]; dup {
		return errors.New("kernel: duplicate run tool provider " + p.Name())
	}
	r.p[p.Name()] = p
	return nil
}

// Providers returns the providers sorted by name.
func (r *RunToolProviderRegistry) Providers() []RunToolProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RunToolProvider, 0, len(r.p))
	for _, p := range r.p {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

var providersMu sync.Mutex

// RunToolProvidersOf returns the host's provider registry, providing a new
// one under SvcRunToolProviders on first use (call it from Init).
func RunToolProvidersOf(h Host) *RunToolProviderRegistry {
	providersMu.Lock()
	defer providersMu.Unlock()
	if v, ok := h.Lookup(SvcRunToolProviders); ok {
		if r, ok := v.(*RunToolProviderRegistry); ok {
			return r
		}
	}
	r := NewRunToolProviderRegistry()
	h.Provide(SvcRunToolProviders, r)
	return r
}

// ToolAllowed reports whether an agent allow-list permits tool name. An
// entry allows the tool of that exact name, and a group entry "x" or "x.*"
// allows every "x.<tool>" (so `browser` allows browser.navigate).
func ToolAllowed(allow []string, name string) bool {
	for _, a := range allow {
		a = strings.TrimSpace(a)
		switch {
		case a == "":
		case a == name:
			return true
		case strings.HasSuffix(a, ".*") && strings.HasPrefix(name, strings.TrimSuffix(a, "*")):
			return true
		case !strings.Contains(a, ".") && strings.HasPrefix(name, a+"."):
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- approvals

// ApprovalRequest asks the run's owner whether an action may proceed.
type ApprovalRequest struct {
	Class   nexusv1.ActionClass
	Title   string
	Summary string
	// Payload is the exact action preview (JSON-compatible values); its
	// hash is what a device signs.
	Payload map[string]any
}

// ApprovalOutcome is the decision for an ApprovalRequest.
type ApprovalOutcome struct {
	Approved   bool
	Level      nexusv1.TrustLevel
	ApprovalID string
	// Reason explains a refusal ("denied", "expired"…).
	Reason string
}

// Approver gates actions of one run against the agent's policy (auto /
// notify proceed, approve / biometric wait for the user). After a refusal
// the approver has already put the run back to RUNNING. An error means the
// wait was abandoned (run ended or ctx done).
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalOutcome, error)
}

// ErrNoApprover is returned by RunGate.Approve before a runtime bound a gate.
var ErrNoApprover = errors.New("kernel: this run has no approval channel")

// RunGate is a late-bound holder for the run's Approver: run tools are built
// before the runtime starts, and the runtime that owns Resolve (agent loop,
// Claude Code / Codex MCP bridge, browser runtime) binds its gate with Set.
// A nil *RunGate is valid and refuses every request.
type RunGate struct {
	mu sync.RWMutex
	a  Approver
}

// NewRunGate returns an unbound gate.
func NewRunGate() *RunGate { return &RunGate{} }

// Set binds the approver (nil unbinds).
func (g *RunGate) Set(a Approver) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.a = a
	g.mu.Unlock()
}

// Bound reports whether an approver is set.
func (g *RunGate) Bound() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.a != nil
}

// Approve forwards to the bound approver; without one it returns
// ErrNoApprover (callers fail closed).
func (g *RunGate) Approve(ctx context.Context, req ApprovalRequest) (ApprovalOutcome, error) {
	if g == nil {
		return ApprovalOutcome{}, ErrNoApprover
	}
	g.mu.RLock()
	a := g.a
	g.mu.RUnlock()
	if a == nil {
		return ApprovalOutcome{}, ErrNoApprover
	}
	return a.Approve(ctx, req)
}
