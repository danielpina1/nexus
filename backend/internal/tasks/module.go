// Package tasks is the hub task engine: task/run state machine, routing,
// dispatch to nodes, run-event ingestion, escalation, sub-tasks and the
// ask_user / notify / spawn_agent / schedule hub tools.
//
// Module "tasks" (hub, default enabled). Requires store and events. Provides
// kernel.SvcTasks (*Engine, a kernel.TaskEngine) and mounts TaskService.
// Optional services, resolved per call: router, agents, dispatcher, approvals.
//
// Config (nexus.yaml):
//
//	tasks:
//	  route_threshold: 0.6   # router confidence below this asks the user
//	  max_depth: 5           # max sub-task depth for spawn_agent
//	  max_choices: 6         # agents offered when asking which agent to use
package tasks

import (
	"context"
	"fmt"

	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Config is the "tasks" config section.
type Config struct {
	RouteThreshold float64 `yaml:"route_threshold"`
	MaxDepth       int     `yaml:"max_depth"`
	MaxChoices     int     `yaml:"max_choices"`
}

func (c *Config) defaults() {
	if c.RouteThreshold <= 0 {
		c.RouteThreshold = 0.6
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = 5
	}
	if c.MaxChoices <= 0 {
		c.MaxChoices = 6
	}
}

// Module is the tasks module.
type Module struct {
	kernel.BaseModule
	Engine *Engine
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "tasks",
		Description:    "Task engine: state machine, routing, dispatch, escalation, sub-tasks",
		Side:           kernel.SideHub,
		Requires:       []string{"store", "events"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("tasks: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	bus, err := kernel.Resolve[kernel.EventBus](h, kernel.SvcEventBus)
	if err != nil {
		return err
	}
	e, err := NewEngine(h, st, bus, cfg)
	if err != nil {
		return err
	}
	for _, t := range e.hubTools() {
		if err := h.Tools().Register(t); err != nil {
			return fmt.Errorf("tasks: %w", err)
		}
	}
	m.Engine = e
	h.Provide(kernel.SvcTasks, e)
	h.Handle(nexusv1connect.NewTaskServiceHandler(Handler{E: e}))
	return nil
}
