package store

import (
	"context"
	"path/filepath"

	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "store" section of nexus.yaml.
type Config struct {
	// Path overrides the database file (default <data_dir>/store/nexus.db).
	Path string `yaml:"path"`
}

// Module provides kernel.SvcStore.
type Module struct {
	kernel.BaseModule
	store *Store
}

// Info implements kernel.Module.
func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "store",
		Description:    "Operational SQLite database (kernel.Store)",
		Side:           kernel.SideHub,
		DefaultEnabled: true,
	}
}

// Init opens the database and provides it as kernel.SvcStore.
func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	path := cfg.Path
	if path == "" {
		path = filepath.Join(h.DataDir(), "nexus.db")
	}
	s, err := Open(path)
	if err != nil {
		return err
	}
	m.store = s
	h.Provide(kernel.SvcStore, kernel.Store(s))
	h.Logger().Info("store opened", "path", path)
	return nil
}

// Stop closes the database.
func (m *Module) Stop(context.Context) error {
	if m.store == nil {
		return nil
	}
	err := m.store.Close()
	m.store = nil
	return err
}
