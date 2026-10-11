// Package events is the hub's ordered, persistent event log with live
// fan-out ("events" module). It provides kernel.SvcEventBus and serves
// nexus.v1.EventService.
package events

import (
	"context"

	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "events" section of nexus.yaml.
type Config struct {
	// SubscriberBuffer is the per-subscriber live buffer (default 4096).
	SubscriberBuffer int `yaml:"subscriber_buffer"`
}

// Module provides kernel.SvcEventBus and mounts EventService.
type Module struct {
	kernel.BaseModule
	bus *Bus
}

// Info implements kernel.Module.
func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "events",
		Description:    "Ordered event log with live fan-out (kernel.EventBus, EventService)",
		Side:           kernel.SideHub,
		Requires:       []string{"store"},
		DefaultEnabled: true,
	}
}

// Init creates the event table, provides the bus and mounts the API.
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	bus, err := New(st, Options{LiveBuffer: cfg.SubscriberBuffer, Logger: h.Logger()})
	if err != nil {
		return err
	}
	m.bus = bus
	h.Provide(kernel.SvcEventBus, kernel.EventBus(bus))
	h.Handle(nexusv1connect.NewEventServiceHandler(&Service{Bus: bus}))
	return nil
}

// Stop closes every live subscription.
func (m *Module) Stop(context.Context) error {
	if m.bus != nil {
		m.bus.Close()
	}
	return nil
}
