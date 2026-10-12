package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/node"
)

func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	cfgPath := fs.String("config", "node.yaml", "node config file (created if missing, updated in place)")
	nodeID := fs.String("id", "", "node id to request (default: node.node_id or the hostname)")
	_ = fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		return errors.New("usage: nexus-node pair [-config node.yaml] [-id NAME] '<nexus://pair URL>'")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return pairNode(ctx, os.Stdout, *cfgPath, fs.Arg(0), *nodeID, node.PairOptions{})
}

// reorder moves flags before the positional URL so both
// `pair URL -config x` and `pair -config x URL` work.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if !strings.ContainsRune(a, '=') && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

// pairNode pairs with the hub and records the credentials in the node
// config. o carries test overrides (Transport, Now).
func pairNode(ctx context.Context, w io.Writer, cfgPath, pairURL, nodeID string, o node.PairOptions) error {
	cfg, err := host.LoadConfigFile(cfgPath)
	if err != nil {
		return err
	}
	var nc node.Config
	if err := cfg.Section("node", &nc); err != nil {
		return fmt.Errorf("config node: %w", err)
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	if nodeID == "" {
		nodeID = nc.NodeID
	}
	if nodeID == "" {
		nodeID, _ = os.Hostname()
	}
	o.URL, o.NodeID, o.DataDir = pairURL, nodeID, filepath.Join(dataDir, "node")
	fmt.Fprintf(w, "Pairing node %q with the hub…\n", nodeID)
	res, err := node.Pair(ctx, o)
	if err != nil {
		return err
	}
	if err := updateNodeYAML(cfgPath, res); err != nil {
		return fmt.Errorf("paired, but updating %s failed: %w (set node.hub_url/node_id/token_file/ca_file/cert_file/key_file by hand)", cfgPath, err)
	}
	fmt.Fprintf(w, `
✓ paired as %s with %s
  token:       %s
  hub CA:      %s   (only trusted root for the hub)
  certificate: %s
  key:         %s
  config:      %s updated

Safety code:  %s

Compare it with the hub: the "node paired" log line of nexusd (safety_code=…)
must show the same 6 emojis. If they differ, revoke the node on the hub.

Next: nexus-node run -config %s
`, res.NodeID, res.HubURL, res.TokenFile, res.CAFile, res.CertFile, res.KeyFile, cfgPath, res.SafetyCode, cfgPath)
	if os.Getenv("NEXUS_NODE_TOKEN") != "" || nc.Token != "" {
		fmt.Fprintln(w, "\nNote: node.token / NEXUS_NODE_TOKEN take precedence over the paired token; remove them.")
	}
	return nil
}

// updateNodeYAML sets the node section's hub_url, node_id and credential
// files, keeping the rest of the file (and its comments). A missing file is
// created.
func updateNodeYAML(path string, r *node.PairResult) error {
	var doc yaml.Node
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		b = []byte("# nexus-node configuration (written by nexus-node pair)\ndata_dir: ./data\n")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return errors.New("config root is not a mapping")
	}
	sec := mapGet(root, "node")
	if sec == nil || sec.Kind != yaml.MappingNode {
		sec = &yaml.Node{Kind: yaml.MappingNode}
		mapSet(root, "node", sec)
	}
	for _, kv := range [][2]string{
		{"hub_url", r.HubURL},
		{"node_id", r.NodeID},
		{"token_file", r.TokenFile},
		{"ca_file", r.CAFile},
		{"cert_file", r.CertFile},
		{"key_file", r.KeyFile},
	} {
		mapSet(sec, kv[0], &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[1]})
	}
	mapDelete(sec, "token") // a static token would override the paired one
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	_ = enc.Close()
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(path, out.Bytes(), mode)
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func mapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
