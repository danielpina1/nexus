package node

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config is the "node" section of node.yaml.
//
//	node:
//	  hub_url: https://hub.example:7700
//	  node_id: mac
//	  token: s3cret            # or env NEXUS_NODE_TOKEN
//	  tags: [mac, browser]
//	  workspaces_dir: ./data/workspaces
//	  token_file: ""           # file holding the token (written by `nexus-node pair`)
//	  ca_file: ""              # hub CA (PEM); when set it is the ONLY trusted root
//	  cert_file: ""            # mTLS client certificate (from pairing)
//	  key_file: ""             # node identity key (PKCS#8 PEM, 0600)
//	  status_interval: 5s
//	  strict_approvals: false  # true: reject approvals when no verifier is installed
//	  max_concurrent:          # per-runtime parallel runs (overrides the runtime's own
//	    claude-code: 2         # default, e.g. runtime.claudecode.max_concurrent); 0 = unlimited.
//	    codex: 2               # Runs beyond the limit wait in a FIFO queue.
type Config struct {
	HubURL          string         `yaml:"hub_url"`
	NodeID          string         `yaml:"node_id"`
	Token           string         `yaml:"token"`
	Tags            []string       `yaml:"tags"`
	WorkspacesDir   string         `yaml:"workspaces_dir"`
	TokenFile       string         `yaml:"token_file"`
	CAFile          string         `yaml:"ca_file"`
	CertFile        string         `yaml:"cert_file"`
	KeyFile         string         `yaml:"key_file"`
	StatusInterval  time.Duration  `yaml:"status_interval"`
	BackoffMin      time.Duration  `yaml:"backoff_min"`
	BackoffMax      time.Duration  `yaml:"backoff_max"`
	StrictApprovals bool           `yaml:"strict_approvals"`
	MaxConcurrent   map[string]int `yaml:"max_concurrent"`
	// Version is reported in NodeHello (set by the binary).
	Version string `yaml:"-"`
}

// Defaults fills unset fields; dataDir is the module data dir.
func (c *Config) Defaults(dataDir string) {
	if c.NodeID == "" {
		c.NodeID, _ = os.Hostname()
	}
	if c.Token == "" {
		c.Token = os.Getenv("NEXUS_NODE_TOKEN")
	}
	if c.HubURL == "" {
		c.HubURL = "http://127.0.0.1:7700"
	}
	if c.WorkspacesDir == "" {
		c.WorkspacesDir = filepath.Join(dataDir, "workspaces")
	}
	// Runtimes start processes inside the workspace (cmd.Dir) and pass paths
	// under it as arguments, so a relative path would be resolved twice.
	if abs, err := filepath.Abs(c.WorkspacesDir); err == nil {
		c.WorkspacesDir = abs
	}
	if c.StatusInterval <= 0 {
		c.StatusInterval = 5 * time.Second
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 500 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 60 * time.Second
	}
	if c.Version == "" {
		c.Version = "dev"
	}
}

// helloTags are the configured tags plus os:/arch: tags.
func (c *Config) helloTags() []string {
	tags := append([]string(nil), c.Tags...)
	return append(tags, "os:"+runtime.GOOS, "arch:"+runtime.GOARCH)
}
