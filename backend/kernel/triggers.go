package kernel

import (
	"context"
	"fmt"
	"sync"
)

// Firing is emitted by a trigger source when something happens
// (cron tick, webhook, email, geofence, node alert…).
type Firing struct {
	Source  string            // trigger source name, e.g. "cron", "webhook"
	Key     string            // source-specific key (cron expr id, hook id…)
	Payload map[string]any    // data passed to the workflow/agent as inputs
	Meta    map[string]string // free-form
}

// TriggerSource produces firings. The workflow engine subscribes to all sources.
type TriggerSource interface {
	Name() string
	// Run blocks until ctx is done, calling emit for each firing.
	Run(ctx context.Context, emit func(Firing)) error
}

// TriggerRegistry holds trigger sources on the hub.
type TriggerRegistry struct {
	mu  sync.RWMutex
	src map[string]TriggerSource
}

func NewTriggerRegistry() *TriggerRegistry { return &TriggerRegistry{src: map[string]TriggerSource{}} }

func (r *TriggerRegistry) Register(s TriggerSource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.src[s.Name()]; dup {
		return fmt.Errorf("kernel: duplicate trigger source %q", s.Name())
	}
	r.src[s.Name()] = s
	return nil
}

func (r *TriggerRegistry) All() []TriggerSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TriggerSource, 0, len(r.src))
	for _, s := range r.src {
		out = append(out, s)
	}
	return out
}
