package browser

import (
	"context"
	"errors"
	"strings"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Provider is the browser's kernel.RunToolProvider: it gives runs of any
// other runtime (claude-code, codex, openai, ollama…) whose agent allow-list
// names `browser` (or `browser.*`, or individual browser.<tool> names) the
// browser.* tools, backed by a per-run browser session with the same rules
// as the browser runtime — domain allow-list (agent note `browser:` block,
// agents.<id> config, allowed_domains), per-store profiles, shops.get
// wrapping, and the SPEND gate on checkout/payment clicks.
//
// The checkout gate goes through RunSpec.Gate, which the run's runtime binds
// to its own approval gate (agent loop; the Claude Code / Codex Nexus MCP
// bridge). So a Claude Code agent calling mcp__nexus__browser_click on
// "Finalizar compra" still produces a SPEND approval (Face ID by default)
// and the click only happens once approved. Without a bound gate (e.g. a CLI
// runtime with its MCP bridge disabled) checkout clicks are refused.
//
// Runs of the browser runtime itself are skipped: it builds the same kit
// internally. Chrome starts lazily on the first browser tool call and the
// session is closed when the run ends (Cleanup).
type Provider struct {
	rt *Runtime
}

// NewProvider returns the provider over a configured browser runtime.
func NewProvider(rt *Runtime) *Provider { return &Provider{rt: rt} }

// Name implements kernel.RunToolProvider.
func (p *Provider) Name() string { return "browser" }

// WantsBrowser reports whether an allow-list names browser tools.
func WantsBrowser(allow []string) bool {
	for _, n := range allow {
		if n == "browser" || strings.HasPrefix(n, "browser.") {
			return true
		}
	}
	return false
}

// RunTools implements kernel.RunToolProvider.
func (p *Provider) RunTools(_ context.Context, spec kernel.RunSpec) (kernel.RunToolSet, error) {
	a := spec.Assignment
	if a == nil || a.GetAgent().GetRuntime() == "browser" || !WantsBrowser(a.GetAgent().GetTools()) {
		return kernel.RunToolSet{}, nil
	}
	if p.rt.chrome() == "" {
		return kernel.RunToolSet{}, errors.New("browser tools requested but no Chrome/Chromium on this node (set runtime.browser.chrome_path or CHROME_PATH)")
	}
	log := p.rt.host.Logger().With("provider", "browser", "run_id", a.GetRunId(), "agent", a.GetAgent().GetId())
	var gate kernel.Approver
	if spec.Gate != nil {
		gate = spec.Gate
	}
	k := p.rt.newKit(a, spec.Tools, spec.Workdir, gate, spec.Emit, log)
	tools := selectBrowserTools(k.kit.tools(), a.GetAgent().GetTools())
	for _, t := range k.runTools {
		if t.Spec().Name == HubToolShopsGet {
			tools = append(tools, t) // replaces the run's shops.get
		}
	}
	return kernel.RunToolSet{
		Tools:        tools,
		Instructions: k.instructions(""),
		Cleanup:      k.pool.Close,
	}, nil
}
