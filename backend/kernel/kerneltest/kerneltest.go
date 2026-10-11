// Package kerneltest provides an in-memory kernel.Host for module unit tests.
package kerneltest

import (
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Host is a fake kernel.Host. Handlers are mounted on Mux so tests can use
// httptest.NewServer(h.Mux).
type Host struct {
	SideValue kernel.Side
	Mux       *http.ServeMux
	Dir       string
	// ConfigYAML is decoded by Config().
	ConfigYAML string

	tools    *kernel.ToolRegistry
	runtimes *kernel.RuntimeRegistry
	triggers *kernel.TriggerRegistry
	mu       sync.RWMutex
	services map[string]any
}

// New returns a fake hub host with a temp data dir.
func New(t testing.TB) *Host {
	t.Helper()
	return &Host{
		SideValue: kernel.SideHub,
		Mux:       http.NewServeMux(),
		Dir:       t.TempDir(),
		tools:     kernel.NewToolRegistry(),
		runtimes:  kernel.NewRuntimeRegistry(),
		triggers:  kernel.NewTriggerRegistry(),
		services:  map[string]any{},
	}
}

func (h *Host) Side() kernel.Side { return h.SideValue }
func (h *Host) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
func (h *Host) Config(v any) error {
	if h.ConfigYAML == "" {
		return nil
	}
	return yaml.Unmarshal([]byte(h.ConfigYAML), v)
}
func (h *Host) DataDir() string                   { return h.Dir }
func (h *Host) Handle(p string, hd http.Handler)  { h.Mux.Handle(p, hd) }
func (h *Host) Tools() *kernel.ToolRegistry       { return h.tools }
func (h *Host) Runtimes() *kernel.RuntimeRegistry { return h.runtimes }
func (h *Host) Triggers() *kernel.TriggerRegistry { return h.triggers }
func (h *Host) Provide(name string, svc any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.services[name] = svc
}
func (h *Host) Lookup(name string) (any, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	v, ok := h.services[name]
	return v, ok
}
