package node

import (
	"context"
	"errors"
	"sync"

	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Version is reported to the hub; set by cmd/nexus-node.
var Version = "dev"

// Module is the node module "node": it runs the Daemon. Runtimes register
// themselves in their Init; the daemon probes them at connect time.
type Module struct {
	d      *Daemon
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "node",
		Description:    "Node daemon: hub link, run manager, node-local tools",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(ctx context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	cfg.Version = Version
	d, err := New(h, cfg, Options{})
	if err != nil {
		return err
	}
	if d.cfg.Token == "" {
		h.Logger().Warn("node token is empty (set node.token or NEXUS_NODE_TOKEN)")
	}
	if err := d.RegisterTools(); err != nil {
		return err
	}
	h.Provide(kernel.SvcHubTools, d)
	m.d = d
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	if m.d == nil {
		return errors.New("node: not initialised")
	}
	rctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		_ = m.d.Run(rctx)
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.cancel == nil {
		return nil
	}
	m.once.Do(m.cancel)
	select {
	case <-m.done:
	case <-ctx.Done():
	}
	return nil
}

// Daemon exposes the running daemon (nexus-node CLI, tests).
func (m *Module) Daemon() *Daemon { return m.d }
