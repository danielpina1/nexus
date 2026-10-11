package kernel

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Side says in which process a module runs.
type Side string

const (
	SideHub  Side = "hub"
	SideNode Side = "node"
)

// ModuleInfo describes a module.
type ModuleInfo struct {
	// Name is unique, lower-case, dot-separated (e.g. "api", "vault", "runtime.shell").
	Name        string
	Description string
	Side        Side
	// Requires lists modules that must be initialised before this one.
	Requires []string
	// DefaultEnabled modules run unless disabled in config.
	DefaultEnabled bool
}

// Module is the unit of extension.
//
// Lifecycle: Init (all modules, in dependency order) → Start (same order) →
// Stop (reverse order). Init must not block; long-running work starts in Start
// and must stop when ctx is cancelled or Stop is called.
type Module interface {
	Info() ModuleInfo
	Init(ctx context.Context, h Host) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Factory creates a fresh module instance.
type Factory func() Module

var (
	modMu     sync.RWMutex
	factories = map[string]registered{}
)

type registered struct {
	info    ModuleInfo
	factory Factory
}

// RegisterModule makes a module available. Call it from init().
// It panics on duplicate names, mirroring database/sql.Register.
func RegisterModule(f Factory) {
	info := f().Info()
	if info.Name == "" {
		panic("kernel: module with empty name")
	}
	modMu.Lock()
	defer modMu.Unlock()
	if _, dup := factories[info.Name]; dup {
		panic("kernel: duplicate module " + info.Name)
	}
	factories[info.Name] = registered{info: info, factory: f}
}

// RegisteredModules returns info for every registered module on the given side,
// sorted by name.
func RegisteredModules(side Side) []ModuleInfo {
	modMu.RLock()
	defer modMu.RUnlock()
	var out []ModuleInfo
	for _, r := range factories {
		if r.info.Side == side {
			out = append(out, r.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NewModule instantiates a registered module by name.
func NewModule(name string) (Module, error) {
	modMu.RLock()
	r, ok := factories[name]
	modMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("kernel: unknown module %q", name)
	}
	return r.factory(), nil
}

// BaseModule provides no-op Start/Stop for embedding.
type BaseModule struct{}

func (BaseModule) Start(context.Context) error { return nil }
func (BaseModule) Stop(context.Context) error  { return nil }
