// Package host loads and runs kernel modules for a process (hub or node).
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Config is the process configuration file (nexus.yaml / node.yaml).
//
//	data_dir: ./data
//	modules:
//	  enable: [extra.module]
//	  disable: [router]
//	<module-name>:            # per-module section, decoded by Host.Config
//	  key: value
type Config struct {
	DataDir string `yaml:"data_dir"`
	Modules struct {
		Enable  []string `yaml:"enable"`
		Disable []string `yaml:"disable"`
	} `yaml:"modules"`
	sections map[string]yaml.Node
}

// ParseConfig parses YAML bytes. Empty input yields defaults.
func ParseConfig(b []byte) (*Config, error) {
	c := &Config{sections: map[string]yaml.Node{}}
	if len(b) == 0 {
		return c, nil
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var all map[string]yaml.Node
	if err := yaml.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for k, v := range all {
		c.sections[k] = v
	}
	return c, nil
}

// LoadConfigFile reads path; a missing file yields defaults.
func LoadConfigFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ParseConfig(nil)
	}
	if err != nil {
		return nil, err
	}
	return ParseConfig(b)
}

// Section decodes a module section into v.
func (c *Config) Section(name string, v any) error {
	n, ok := c.sections[name]
	if !ok {
		return nil
	}
	return n.Decode(v)
}

// Options configure a Process.
type Options struct {
	Side   kernel.Side
	Config *Config
	Logger *slog.Logger
	// Mux receives Host.Handle registrations (hub). May be nil on nodes.
	Mux *http.ServeMux
}

// Process is a set of running modules sharing registries and services.
type Process struct {
	side     kernel.Side
	cfg      *Config
	log      *slog.Logger
	mux      *http.ServeMux
	tools    *kernel.ToolRegistry
	runtimes *kernel.RuntimeRegistry
	triggers *kernel.TriggerRegistry

	mu       sync.RWMutex
	services map[string]any

	modules []kernel.Module // init order
	started []kernel.Module
}

// New creates a process; call Init then Start.
func New(o Options) *Process {
	if o.Config == nil {
		o.Config, _ = ParseConfig(nil)
	}
	if o.Config.DataDir == "" {
		o.Config.DataDir = "./data"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Process{
		side:     o.Side,
		cfg:      o.Config,
		log:      o.Logger,
		mux:      o.Mux,
		tools:    kernel.NewToolRegistry(),
		runtimes: kernel.NewRuntimeRegistry(),
		triggers: kernel.NewTriggerRegistry(),
		services: map[string]any{},
	}
}

func (p *Process) Tools() *kernel.ToolRegistry       { return p.tools }
func (p *Process) Runtimes() *kernel.RuntimeRegistry { return p.runtimes }
func (p *Process) Triggers() *kernel.TriggerRegistry { return p.triggers }

// Lookup exposes services to process owners (servers, CLIs).
func (p *Process) Lookup(name string) (any, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.services[name]
	return v, ok
}

// EnabledModules resolves which registered modules run, in dependency order.
func (p *Process) EnabledModules() ([]kernel.ModuleInfo, error) {
	all := kernel.RegisteredModules(p.side)
	byName := map[string]kernel.ModuleInfo{}
	for _, m := range all {
		byName[m.Name] = m
	}
	enabled := map[string]bool{}
	for _, m := range all {
		if m.DefaultEnabled {
			enabled[m.Name] = true
		}
	}
	for _, n := range p.cfg.Modules.Enable {
		if _, ok := byName[n]; !ok {
			return nil, fmt.Errorf("config enables unknown module %q", n)
		}
		enabled[n] = true
	}
	for _, n := range p.cfg.Modules.Disable {
		delete(enabled, n)
	}
	// Topological sort (Kahn), stable by name.
	var names []string
	for n := range enabled {
		names = append(names, n)
	}
	sort.Strings(names)
	var order []kernel.ModuleInfo
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	var visit func(n string, path []string) error
	visit = func(n string, path []string) error {
		switch state[n] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("module dependency cycle: %v", append(path, n))
		}
		state[n] = 1
		info := byName[n]
		for _, r := range info.Requires {
			if !enabled[r] {
				return fmt.Errorf("module %q requires %q, which is not enabled", n, r)
			}
			if err := visit(r, append(path, n)); err != nil {
				return err
			}
		}
		state[n] = 2
		order = append(order, info)
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Init instantiates and initialises enabled modules.
func (p *Process) Init(ctx context.Context) error {
	infos, err := p.EnabledModules()
	if err != nil {
		return err
	}
	for _, info := range infos {
		m, err := kernel.NewModule(info.Name)
		if err != nil {
			return err
		}
		v := &view{p: p, name: info.Name, log: p.log.With("module", info.Name)}
		if err := m.Init(ctx, v); err != nil {
			return fmt.Errorf("init %s: %w", info.Name, err)
		}
		p.modules = append(p.modules, m)
		p.log.Debug("module initialised", "module", info.Name)
	}
	return nil
}

// Start starts modules in order; on failure, already-started modules are stopped.
func (p *Process) Start(ctx context.Context) error {
	for _, m := range p.modules {
		if err := m.Start(ctx); err != nil {
			_ = p.Stop(context.Background())
			return fmt.Errorf("start %s: %w", m.Info().Name, err)
		}
		p.started = append(p.started, m)
	}
	return nil
}

// Stop stops started modules in reverse order.
func (p *Process) Stop(ctx context.Context) error {
	var errs []error
	for i := len(p.started) - 1; i >= 0; i-- {
		if err := p.started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", p.started[i].Info().Name, err))
		}
	}
	p.started = nil
	return errors.Join(errs...)
}

// ModuleNames lists initialised modules in order.
func (p *Process) ModuleNames() []string {
	out := make([]string, len(p.modules))
	for i, m := range p.modules {
		out[i] = m.Info().Name
	}
	return out
}

// view is the per-module kernel.Host.
type view struct {
	p    *Process
	name string
	log  *slog.Logger
}

func (v *view) Side() kernel.Side                 { return v.p.side }
func (v *view) Logger() *slog.Logger              { return v.log }
func (v *view) Config(out any) error              { return v.p.cfg.Section(v.name, out) }
func (v *view) Tools() *kernel.ToolRegistry       { return v.p.tools }
func (v *view) Runtimes() *kernel.RuntimeRegistry { return v.p.runtimes }
func (v *view) Triggers() *kernel.TriggerRegistry { return v.p.triggers }

func (v *view) DataDir() string {
	d := filepath.Join(v.p.cfg.DataDir, v.name)
	_ = os.MkdirAll(d, 0o700)
	return d
}

func (v *view) Handle(pattern string, h http.Handler) {
	if v.p.mux == nil {
		return
	}
	v.p.mux.Handle(pattern, h)
}

func (v *view) Provide(name string, svc any) {
	v.p.mu.Lock()
	defer v.p.mu.Unlock()
	if _, dup := v.p.services[name]; dup {
		v.log.Warn("service replaced", "service", name)
	}
	v.p.services[name] = svc
}

func (v *view) Lookup(name string) (any, bool) { return v.p.Lookup(name) }
