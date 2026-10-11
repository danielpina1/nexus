package kernel

import (
	"fmt"
	"log/slog"
	"net/http"
)

// Host is what a module sees of the running process.
type Host interface {
	// Side reports whether this is the hub or a node.
	Side() Side
	Logger() *slog.Logger
	// Config decodes this module's config section (YAML key = module name) into v.
	// Missing sections leave v untouched.
	Config(v any) error
	// DataDir is a private, persistent directory for the module.
	DataDir() string

	// Handle mounts an HTTP handler on the public API server (hub only;
	// no-op on nodes). Connect handlers are mounted with their path prefix.
	Handle(pattern string, h http.Handler)

	Tools() *ToolRegistry
	Runtimes() *RuntimeRegistry
	Triggers() *TriggerRegistry

	// Provide publishes a service under a well-known name (see services.go).
	Provide(name string, svc any)
	// Lookup returns a service published by another module.
	Lookup(name string) (any, bool)
}

// Resolve looks up a service and asserts its type.
func Resolve[T any](h Host, name string) (T, error) {
	var zero T
	v, ok := h.Lookup(name)
	if !ok {
		return zero, fmt.Errorf("kernel: service %q not provided (is its module enabled?)", name)
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("kernel: service %q has type %T, want %T", name, v, zero)
	}
	return t, nil
}
