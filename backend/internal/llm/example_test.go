package llm

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/configs"
)

// The shipped example configs must produce a valid client.
func TestExampleConfigs(t *testing.T) {
	node, err := os.ReadFile(filepath.Join("..", "..", "cmd", "nexus-node", "node.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, section string
		body          []byte
	}{
		{"nexus.example.yaml", "llm", configs.NexusExample},
		{"node.example.yaml", "node.llm", node},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var doc map[string]yaml.Node
			if err := yaml.Unmarshal(tt.body, &doc); err != nil {
				t.Fatal(err)
			}
			n, ok := doc[tt.section]
			if !ok {
				t.Fatalf("no %s section", tt.section)
			}
			var cfg Config
			if err := n.Decode(&cfg); err != nil {
				t.Fatal(err)
			}
			c, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.AliasesAuto || c.DefaultProvider() != "ollama" || c.baseURL("ollama") != "http://localhost:11434/v1" {
				t.Fatalf("unexpected example config %+v", cfg)
			}
			for _, a := range []string{"fast-local", "smart", "best", "embed"} {
				if !isPlaceholder(cfg.Aliases[a].Model) {
					t.Errorf("%s should be CHANGE_ME", a)
				}
			}
		})
	}
}
