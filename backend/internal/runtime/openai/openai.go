// Package openai provides the native agent runtimes "openai" and "ollama"
// (node module "runtime.openai"). Both run internal/runtime/agentloop over the
// node's kernel.LLM (module "node.llm"), resolved lazily per run, so model
// access can be configured or reloaded independently.
//
// The two runtimes share one implementation. The only difference: for model
// names that are not configured aliases, "ollama" sends them to the ollama
// provider (when the LLM supports provider scoping and has that provider),
// while "openai" uses the LLM's default provider (typically Unify Gateway).
//
// Configuration (module section "runtime.openai"):
//
//	runtime.openai:
//	  default_model: smart     # when the agent has no model
//	  max_steps: 50            # model calls per run
//	  max_tokens: 0            # per model call; 0 = provider default
//	  no_stream: false         # use non-streaming completions
//	  ollama:
//	    disabled: false        # don't register the "ollama" runtime
//	    provider: ollama       # provider for raw model names
//	    default_model: fast-local
package openai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the module configuration.
type Config struct {
	DefaultModel string `yaml:"default_model"`
	MaxSteps     int    `yaml:"max_steps"`
	MaxTokens    int    `yaml:"max_tokens"`
	NoStream     bool   `yaml:"no_stream"`
	Ollama       struct {
		Disabled     bool   `yaml:"disabled"`
		Provider     string `yaml:"provider"`
		DefaultModel string `yaml:"default_model"`
	} `yaml:"ollama"`
}

// Module registers the runtimes.
type Module struct {
	kernel.BaseModule
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "runtime.openai",
		Description:    "Native Nexus agent loop over OpenAI-compatible models (runtimes openai, ollama)",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "smart"
	}
	if cfg.Ollama.Provider == "" {
		cfg.Ollama.Provider = "ollama"
	}
	if cfg.Ollama.DefaultModel == "" {
		cfg.Ollama.DefaultModel = "fast-local"
	}
	if err := h.Runtimes().Register(&Runtime{name: "openai", host: h, cfg: cfg, defaultModel: cfg.DefaultModel}); err != nil {
		return err
	}
	if !cfg.Ollama.Disabled {
		if err := h.Runtimes().Register(&Runtime{
			name: "ollama", host: h, cfg: cfg,
			provider: cfg.Ollama.Provider, defaultModel: cfg.Ollama.DefaultModel,
		}); err != nil {
			return err
		}
	}
	return nil
}

// providerScoper matches llm.ProviderScoper without importing the package.
type providerScoper interface {
	WithDefaultProvider(name string) kernel.LLM
	HasProvider(name string) bool
}

// Runtime is a native agent runtime.
type Runtime struct {
	name         string
	provider     string // "" = LLM default
	defaultModel string
	host         kernel.Host
	cfg          Config
}

func (r *Runtime) Name() string { return r.name }

func (r *Runtime) Capabilities() kernel.RuntimeCapabilities {
	return kernel.RuntimeCapabilities{Streaming: !r.cfg.NoStream, Steering: true}
}

func (r *Runtime) llm() (kernel.LLM, error) {
	l, err := kernel.Resolve[kernel.LLM](r.host, kernel.SvcLLM)
	if err != nil {
		return nil, fmt.Errorf("%s runtime: no model access (configure node.llm providers): %w", r.name, err)
	}
	if r.provider != "" {
		if ps, ok := l.(providerScoper); ok {
			if !ps.HasProvider(r.provider) {
				return nil, fmt.Errorf("%s runtime: provider %q is not configured in node.llm", r.name, r.provider)
			}
			l = ps.WithDefaultProvider(r.provider)
		}
	}
	return l, nil
}

// Available reports whether model access is configured on this node.
func (r *Runtime) Available(context.Context) (string, bool) {
	if _, err := r.llm(); err != nil {
		return "", false
	}
	return "native", true
}

// Start launches an agent loop for the assignment.
func (r *Runtime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	a := spec.Assignment
	if a == nil {
		return nil, errors.New("openai runtime: no assignment")
	}
	l, err := r.llm()
	if err != nil {
		return nil, err
	}
	model := a.GetAgent().GetModel()
	if model == "" {
		model = r.defaultModel
	}
	limits := agentloop.Limits{MaxSteps: r.cfg.MaxSteps, MaxCost: a.GetAgent().GetLimits().GetMaxCost()}
	if s := a.GetAgent().GetLimits().GetMaxRuntimeSeconds(); s > 0 {
		limits.Deadline = time.Now().Add(time.Duration(s) * time.Second)
	}
	return agentloop.Start(ctx, agentloop.Config{
		LLM:          l,
		Model:        model,
		Instructions: a.GetInstructions(),
		Prompt:       agentloop.BuildPrompt(a),
		Tools:        spec.Tools,
		Limits:       limits,
		TaskID:       a.GetTaskId(),
		RunID:        a.GetRunId(),
		AgentID:      a.GetAgent().GetId(),
		Agent:        a.GetAgent(),
		Runtime:      r.name,
		NoStream:     r.cfg.NoStream,
		MaxTokens:    r.cfg.MaxTokens,
		Logger:       r.host.Logger().With("runtime", r.name),
	})
}
