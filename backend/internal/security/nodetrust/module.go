// Package nodetrust is the node module that enforces edge verification
// (SECURITY.md Layer 5): it loads the owner TrustStore and provides
// owner.EdgeVerifier under node.SvcApprovalVerifier, so approvals reach a
// runtime only with a valid Face ID signature from an owner-enrolled device.
package nodetrust

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/internal/security/owner"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the `node.trust` section of node.yaml.
type Config struct {
	// TrustStore is the owner trust file written at node pairing
	// (default <data_dir>/node.trust/trust.json).
	TrustStore string `yaml:"trust_store"`
}

// Module provides the edge approval verifier when a trust store exists.
type Module struct {
	kernel.BaseModule
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "node.trust",
		Description:    "Edge verification of owner-signed approvals",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

func (*Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	if cfg.TrustStore == "" {
		cfg.TrustStore = filepath.Join(h.DataDir(), "trust.json")
	}
	ts, err := owner.LoadTrustStore(cfg.TrustStore)
	if errors.Is(err, os.ErrNotExist) {
		h.Logger().Warn("no owner trust store yet; approvals use the node's fallback verifier (pair this node to enable edge verification)",
			"path", cfg.TrustStore)
		return nil
	}
	if err != nil {
		return err
	}
	h.Provide(node.SvcApprovalVerifier, owner.EdgeVerifier{Trust: ts})
	h.Logger().Info("edge verification enabled", "owner_sequence", ts.Sequence())
	return nil
}
