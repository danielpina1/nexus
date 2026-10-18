// Package anthrexrt is the node module "anthrex": it connects Nexus to a
// local Anthrex daemon (github.com/danielpina1/anthrex, a terminal
// multiplexer for coding agents with a deterministic orchestration engine).
//
// It provides
//
//   - the runtime "anthrex": a Nexus task whose agent has `runtime: anthrex`
//     becomes an Anthrex goal (RunRequest::StartGoal) in an allowed
//     repository; the run's progress is mirrored as RunOutput lines and one
//     ToolCall/ToolResult pair per Anthrex task, token usage as
//     CostRecorded, and every Anthrex gate becomes a Nexus Approval or
//     Question (plan approval, approval holds, design document gates, the
//     orchestrator's ask_user, a user-only halt, and the final accept, which
//     is a CODE_PUSH approval). The terminal TaskUpdated carries REPORT.md
//     as its summary, so the vault episode captures it;
//   - node tools anthrex.* (status, start_goal, message, approve, answer,
//     accept, cancel, windows, new_window, send_input, conversation,
//     profile, stats) for every Nexus agent whose allow-list names them.
//
// The module is a no-op when neither the daemon socket nor the anthrex
// binary is found: the runtime reports itself unavailable, so the hub never
// routes anthrex agents to this node. See docs/ANTHREX.md.
//
// Configuration (node.yaml, section "anthrex"):
//
//	anthrex:
//	  socket: ""              # default: ANTHREX_SOCKET, else Anthrex's own rule
//	  bin: anthrex            # CLI fallback (protocol mismatch / no socket)
//	  mode: auto              # auto | socket | cli
//	  env: {}                 # extra env for the CLI (ANTHREX_* passthrough)
//	  allowed_repos: [~/src/nexus]   # required: goals/windows only here
//	  default_delivery: local # local | pr
//	  default_orchestrator: ""  # claude | codex[:model]; "" = Anthrex's default
//	  design: off             # off | full (agents may override)
//	  plan_approval: approve  # approve (Nexus approval) | auto (--yes)
//	  trust_project: false    # pass --trust-project
//	  report_max_bytes: 65536
//	  tools: true             # register the anthrex.* node tools
package anthrexrt

import (
	"context"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/internal/anthrex"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Module is the node module "anthrex".
type Module struct {
	kernel.BaseModule
	cfg  Config
	rt   *Runtime
	host kernel.Host
}

// Info implements kernel.Module.
func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "anthrex",
		Description:    "Anthrex coding-agent daemon: runtime \"anthrex\" and anthrex.* node tools",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

// Init implements kernel.Module.
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	if err := h.Config(&m.cfg); err != nil {
		return err
	}
	m.host = h
	m.rt = New(m.cfg, h)
	if err := h.Runtimes().Register(m.rt); err != nil {
		return err
	}
	if m.cfg.Tools == nil || *m.cfg.Tools {
		for _, t := range m.rt.Tools() {
			if err := h.Tools().Register(t); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error {
	if m.rt != nil {
		m.rt.closeAPI()
	}
	return nil
}

// Runtime is the "anthrex" runtime. It also backs the node tools.
type Runtime struct {
	cfg  Config
	host kernel.Host

	mu      sync.Mutex
	api     anthrex.API
	lastErr error
	lastTry time.Time

	// open is anthrex.Open (replaced in tests).
	open func(ctx context.Context, o anthrex.OpenOptions) (anthrex.API, error)
}

// New builds the runtime.
func New(cfg Config, h kernel.Host) *Runtime {
	cfg.defaults()
	return &Runtime{cfg: cfg, host: h, open: anthrex.Open}
}

// reprobeAfter bounds how often a failed connection is retried.
const reprobeAfter = 10 * time.Second

// API returns the Anthrex client, opening it on first use (socket, else the
// CLI fallback).
func (r *Runtime) API(ctx context.Context) (anthrex.API, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.api != nil {
		return r.api, nil
	}
	if r.lastErr != nil && time.Since(r.lastTry) < reprobeAfter {
		return nil, r.lastErr
	}
	r.lastTry = time.Now()
	api, err := r.open(ctx, r.cfg.openOptions(r.host))
	if err != nil {
		r.lastErr = err
		return nil, err
	}
	r.api, r.lastErr = api, nil
	return api, nil
}

func (r *Runtime) closeAPI() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.api != nil {
		_ = r.api.Close()
		r.api = nil
	}
}

// Name implements kernel.Runtime.
func (r *Runtime) Name() string { return "anthrex" }

// Capabilities implements kernel.Runtime.
func (r *Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: true, Steering: true, NativeTools: true, ApprovalHooks: true}
}

// Available implements kernel.Runtime: the daemon answers our handshake (or
// the CLI fallback is installed). A runtime nobody configured repositories
// for is still available (tools work), but goals are refused with a clear
// message until anthrex.allowed_repos is set.
func (r *Runtime) Available(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api, err := r.API(ctx)
	if err != nil {
		return "", false
	}
	if p, ok := api.(interface{ Ping(context.Context) error }); ok {
		if err := p.Ping(ctx); err != nil {
			return "", false
		}
	}
	v := api.Version()
	if v == "" {
		v = "anthrex"
	}
	return v + " (" + api.Mode() + ", proto " + itoa(anthrex.ProtoVersion) + ")", true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
