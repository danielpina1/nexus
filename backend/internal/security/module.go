// Package security is the hub "security" module and the root of the
// security library (subpackages):
//
//	keys        P-256 / X25519 encodings, signatures, signed-message builder
//	pki         private CA, client/server certificates, TLS 1.3 configs
//	envelope    HPKE + ECDSA sealed envelopes (SealedEnvelope)
//	replay      ReplayGuard (timestamp window, nonce cache, counters)
//	approval    canonical action hash (RFC 8785 JCS), SignedApproval sign/verify
//	owner       OwnerStatement sign/verify, TrustStore (rollback-protected)
//	requestsig  signed-request middleware (Layer 3)
//	audit       hash-chained audit log + signed checkpoints
//	pairing     pairing secrets, QR payload, pairing proof, emoji safety code
//
// The module provides:
//
//   - kernel.SvcSecurity → *Security (VerifyApproval, NewPairing, CA, TLS
//     configs, audit, device lookups; see Security methods)
//   - kernel.SvcAuth     → kernel.Authenticator (the same *Security)
//   - nexusv1connect.DeviceService
//
// Configuration (nexus.yaml):
//
//	security:
//	  dev_token: "long-random-string"   # DEV ONLY: full access bearer token
//	  node_tokens: ["tok1"]              # static node bearer tokens (until mTLS)
//	  hub_urls: ["https://hub.tailnet.ts.net:7700"]  # put in pairing QRs
//	  server_names: ["hub.tailnet.ts.net", "127.0.0.1"] # hub cert SANs
//	  session_ttl: 1h
//	  require_app_attest: false
//	  require_signed_requests: false     # require Layer 3 for device sessions
//	  public_paths: []                   # extra unauthenticated path prefixes
package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/security/audit"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
	"github.com/danielpina1/nexus/backend/internal/security/requestsig"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{} })
}

// Config is the "security" section of nexus.yaml.
type Config struct {
	DevToken              string        `yaml:"dev_token"`
	NodeTokens            []string      `yaml:"node_tokens"` // "node-id:token" entries
	HubURLs               []string      `yaml:"hub_urls"`
	ServerNames           []string      `yaml:"server_names"`
	SessionTTL            time.Duration `yaml:"session_ttl"`
	RequireAppAttest      bool          `yaml:"require_app_attest"`
	RequireSignedRequests bool          `yaml:"require_signed_requests"`
	PublicPaths           []string      `yaml:"public_paths"`
	CAName                string        `yaml:"ca_name"`
}

// Module is the hub security module.
type Module struct {
	kernel.BaseModule
	sec *Security
}

// Info implements kernel.Module.
func (m *Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "security",
		Description:    "PKI, pairing, device sessions, signed requests, approvals verification, audit",
		Side:           kernel.SideHub,
		Requires:       []string{"store"},
		DefaultEnabled: true,
	}
}

// Init implements kernel.Module.
func (m *Module) Init(ctx context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("security: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	sec, err := New(ctx, Options{Config: cfg, Store: st, DataDir: h.DataDir(), Logger: h.Logger(), Host: h})
	if err != nil {
		return err
	}
	m.sec = sec
	h.Provide(kernel.SvcSecurity, sec)
	h.Provide(kernel.SvcAuth, kernel.Authenticator(sec))
	h.Handle(nexusv1connect.NewDeviceServiceHandler(&deviceService{s: sec}))
	return nil
}

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error {
	if m.sec != nil {
		return m.sec.Close()
	}
	return nil
}

// New builds a Security from options (used by Init and by tests/CLIs).
func New(ctx context.Context, o Options) (*Security, error) {
	if o.Store == nil {
		return nil, errors.New("security: store required")
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cfg := o.Config
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = time.Hour
	}
	if err := o.Store.Migrate("security", migrations); err != nil {
		return nil, fmt.Errorf("security: migrate: %w", err)
	}
	ca, err := pki.LoadOrCreateCA(filepath.Join(o.DataDir, "pki"), cfg.CAName)
	if err != nil {
		return nil, fmt.Errorf("security: CA: %w", err)
	}
	hubKey, err := keys.LoadOrCreatePrivateKey(filepath.Join(o.DataDir, "hub_identity.key"))
	if err != nil {
		return nil, fmt.Errorf("security: hub identity key: %w", err)
	}
	auditPath := filepath.Join(o.DataDir, "audit.log")
	if o.MemoryAudit {
		auditPath = "" // CLI sharing the data dir with a running hub
	}
	al, err := audit.Open(auditPath)
	if err != nil {
		return nil, fmt.Errorf("security: audit log: %w", err)
	}
	s := &Security{
		log:    log,
		cfg:    cfg,
		host:   o.Host,
		db:     o.Store.DB(),
		ca:     ca,
		hubKey: hubKey,
		audit:  al,
		guard:  replay.New(replay.DefaultWindow),
		chal:   map[string]challenge{},
		attest: devAppAttest{},
	}
	s.pairing = &pairing.Manager{Store: &sqlPairingStore{db: s.db}, Now: s.now}
	s.reqsig = &requestsig.Verifier{Keys: s.AuthKey, Guard: s.guard}
	if cfg.DevToken != "" {
		log.Warn("!!!!!!!! SECURITY: DEV MODE IS ON — security.dev_token grants FULL API ACCESS. " +
			"Never use dev_token on a reachable hub; pair a device and remove it. !!!!!!!!")
		if len(cfg.DevToken) < 16 {
			log.Warn("security: dev_token is shorter than 16 characters")
		}
	}
	if len(cfg.NodeTokens) > 0 {
		log.Warn("security: static node_tokens configured; prefer node pairing (RedeemNodePairing) + mTLS")
	}
	_, _ = s.audit.Record("hub.start", map[string]any{"ca": ca.Fingerprint(), "dev_mode": cfg.DevToken != ""})
	return s, nil
}

// Options for New.
type Options struct {
	Config  Config
	Store   kernel.Store
	DataDir string
	Logger  *slog.Logger
	// Host is optional; used to resolve the notifier lazily for alerts.
	Host kernel.Host
	// MemoryAudit keeps the audit log in memory instead of appending to
	// <DataDir>/audit.log. CLIs that share the data dir with a running hub
	// (`nexusd pair`) set it so they don't fork the hub's hash chain.
	MemoryAudit bool
}

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS security_devices (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		kind INTEGER NOT NULL DEFAULT 0,
		public_key BLOB NOT NULL,
		approval_public_key BLOB,
		encryption_public_key BLOB,
		push_token TEXT NOT NULL DEFAULT '',
		push_environment TEXT NOT NULL DEFAULT '',
		is_primary INTEGER NOT NULL DEFAULT 0,
		app_attest_key_id TEXT NOT NULL DEFAULT '',
		paired_at INTEGER NOT NULL,
		last_seen INTEGER NOT NULL DEFAULT 0,
		revoked_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS security_sessions (
		token_hash BLOB PRIMARY KEY,
		device_id TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS security_sessions_device ON security_sessions(device_id)`,
	`CREATE TABLE IF NOT EXISTS security_live_activity_tokens (
		device_id TEXT NOT NULL,
		kind INTEGER NOT NULL,
		activity_type TEXT NOT NULL DEFAULT '',
		activity_id TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		token BLOB NOT NULL,
		environment TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (device_id, kind, activity_type, activity_id)
	)`,
	`CREATE TABLE IF NOT EXISTS security_pairings (
		secret_hash BLOB PRIMARY KEY,
		kind TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS security_nodes (
		id TEXT PRIMARY KEY,
		public_key BLOB NOT NULL,
		token_hash BLOB NOT NULL UNIQUE,
		paired_at INTEGER NOT NULL,
		revoked_at INTEGER NOT NULL DEFAULT 0
	)`,
}
