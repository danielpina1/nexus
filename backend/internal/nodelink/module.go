// Package nodelink is the hub side of the Hub↔Node link.
//
// It serves NodeLinkService.Connect (one bidirectional HTTP/2 stream per
// node), keeps the registry of nodes and their sessions, implements
// kernel.Dispatcher (provided as kernel.SvcDispatcher) and serves the
// client-facing NodeService. Protocol details (sequencing, resume, hub tools,
// control calls) are documented in package wire.
package nodelink

import (
	"context"
	"time"

	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "nodelink" section of nexus.yaml.
//
//	nodelink:
//	  tokens:               # node id -> bearer token (used when no security service authenticates nodes)
//	    mac: s3cret
//	  heartbeat_timeout: 90s
//	  outbox_limit: 1024
//	  control_timeout: 15s
type Config struct {
	Tokens           map[string]string `yaml:"tokens"`
	HeartbeatTimeout time.Duration     `yaml:"heartbeat_timeout"`
	OutboxLimit      int               `yaml:"outbox_limit"`
	ControlTimeout   time.Duration     `yaml:"control_timeout"`
}

func (c *Config) defaults() {
	if c.HeartbeatTimeout <= 0 {
		c.HeartbeatTimeout = 90 * time.Second
	}
	if c.OutboxLimit <= 0 {
		c.OutboxLimit = 1024
	}
	if c.ControlTimeout <= 0 {
		c.ControlTimeout = 15 * time.Second
	}
}

// NodeAuthenticator may be implemented by the service published under
// kernel.SvcSecurity; when present it replaces the static token map.
type NodeAuthenticator interface {
	AuthenticateNode(ctx context.Context, nodeID, token string) error
}

// Module is the "nodelink" hub module.
type Module struct {
	hub *Hub
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "nodelink",
		Description:    "Hub↔Node link: node sessions, dispatcher, NodeService",
		Side:           kernel.SideHub,
		Requires:       []string{"events"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(ctx context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	m.hub = NewHub(h, cfg)
	h.Provide(kernel.SvcDispatcher, m.hub)
	h.Handle(nexusv1connect.NewNodeLinkServiceHandler(m.hub))
	h.Handle(nexusv1connect.NewNodeServiceHandler(&NodeService{hub: m.hub}))
	return nil
}

func (m *Module) Start(ctx context.Context) error { return m.hub.Start(ctx) }
func (m *Module) Stop(ctx context.Context) error  { return m.hub.Stop(ctx) }

// Hub returns the module's hub (tests, integrators).
func (m *Module) Hub() *Hub { return m.hub }
