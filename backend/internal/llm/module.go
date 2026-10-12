package llm

import (
	"context"

	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{name: "llm", side: kernel.SideHub} })
	kernel.RegisterModule(func() kernel.Module { return &Module{name: "node.llm", side: kernel.SideNode} })
}

// Module provides kernel.SvcLLM. The same implementation is registered as
// "llm" on the hub (router, memory pipeline) and "node.llm" on nodes (native
// agent runtimes). Both are enabled by default; with no providers configured
// they provide nothing.
type Module struct {
	kernel.BaseModule
	name   string
	side   kernel.Side
	Client *Client // nil when unconfigured
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           m.name,
		Description:    "Model access (OpenAI- or Anthropic-compatible APIs, Ollama) with aliases, fallbacks and health",
		Side:           m.side,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	log := h.Logger()
	if len(cfg.Providers) == 0 {
		log.Info("llm: no providers configured; model access disabled", "module", m.name)
		return nil
	}
	c, err := New(cfg, WithLogger(log))
	if err != nil {
		return err
	}
	for name, p := range cfg.Providers {
		if mode, _ := p.authMode(); mode != AuthNone && p.APIKeyEnv != "" && c.getenv(p.APIKeyEnv) == "" {
			log.Warn("llm: api key env var is empty", "provider", name, "env", p.APIKeyEnv)
		}
	}
	m.Client = c
	h.Provide(kernel.SvcLLM, c)
	log.Info("llm: ready", "providers", len(cfg.Providers), "aliases", len(cfg.Aliases),
		"default_provider", c.defaultProvider, "aliases_auto", cfg.AliasesAuto)
	return nil
}

// Start warms up aliases_auto discovery in the background so the first
// request does not pay for it. Failures are retried lazily on use.
func (m *Module) Start(context.Context) error {
	c := m.Client
	if c == nil || !c.cfg.AliasesAuto {
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
		defer cancel()
		auto, err := c.Discover(ctx)
		if err != nil {
			c.log.Warn("llm: model discovery failed (run `llm check` for details)", "err", err)
		}
		for alias, t := range auto {
			c.log.Info("llm: auto alias", "alias", alias, "target", t.String())
		}
	}()
	return nil
}
