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
//	  ca_file: ""              # extra CA bundle for the hub certificate
//	  status_interval: 15s
//	  strict_approvals: false  # true: reject approvals when no verifier is installed
type Config struct {
	HubURL          string        `yaml:"hub_url"`
	NodeID          string        `yaml:"node_id"`
	Token           string        `yaml:"token"`
	Tags            []string      `yaml:"tags"`
	WorkspacesDir   string        `yaml:"workspaces_dir"`
	CAFile          string        `yaml:"ca_file"`
	StatusInterval  time.Duration `yaml:"status_interval"`
	BackoffMin      time.Duration `yaml:"backoff_min"`
	BackoffMax      time.Duration `yaml:"backoff_max"`
	StrictApprovals bool          `yaml:"strict_approvals"`
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
	if c.StatusInterval <= 0 {
		c.StatusInterval = 15 * time.Second
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
