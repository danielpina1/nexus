package configs_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/configs"
	"github.com/danielpina1/nexus/backend/internal/events"
	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/hubserver"
	"github.com/danielpina1/nexus/backend/internal/llm"
	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/internal/nodelink"
	"github.com/danielpina1/nexus/backend/internal/notify"
	"github.com/danielpina1/nexus/backend/internal/router"
	"github.com/danielpina1/nexus/backend/internal/runtime/browser"
	"github.com/danielpina1/nexus/backend/internal/runtime/claudecode"
	"github.com/danielpina1/nexus/backend/internal/runtime/codex"
	"github.com/danielpina1/nexus/backend/internal/runtime/echo"
	"github.com/danielpina1/nexus/backend/internal/runtime/shell"
	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/internal/vault"
	"github.com/danielpina1/nexus/backend/internal/webtools"
)

// Sections each example may contain, with the module's config type. Decoding
// is strict (unknown keys fail), so a renamed option breaks this test instead
// of being silently ignored at runtime.
var (
	commonSections = map[string]func() any{
		"data_dir": nil, // top level, decoded by host.Config
		"modules":  nil,
	}
	hubSections = map[string]func() any{
		"server":        func() any { return new(hubserver.Config) },
		"store":         func() any { return new(store.Config) },
		"events":        func() any { return new(events.Config) },
		"security":      func() any { return new(security.Config) },
		"vault":         func() any { return new(vault.Config) },
		"llm":           func() any { return new(llm.Config) },
		"router":        func() any { return new(router.Config) },
		"notify":        func() any { return new(notify.Config) },
		"nodelink":      func() any { return new(nodelink.Config) },
		"tools.web.hub": func() any { return new(webtools.Config) },
	}
	nodeSections = map[string]func() any{
		"node":               func() any { return new(node.Config) },
		"node.llm":           func() any { return new(llm.Config) },
		"runtime.echo":       func() any { return new(echo.Config) },
		"runtime.shell":      func() any { return new(shell.Config) },
		"runtime.claudecode": func() any { return new(claudecode.Config) },
		"runtime.codex":      func() any { return new(codex.Config) },
		"runtime.browser":    func() any { return new(browser.Config) },
		"tools.web":          func() any { return new(webtools.Config) },
	}
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkStrict parses b with the loader nexusd/nexus-node use, then decodes
// every section strictly into its module's config type.
func checkStrict(t *testing.T, b []byte, sections map[string]func() any, llmSection string) *host.Config {
	t.Helper()
	cfg, err := host.ParseConfig(b)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	for name, n := range top {
		mk, known := sections[name]
		if !known {
			_, known = commonSections[name]
		}
		if !known {
			t.Errorf("unknown top-level section %q", name)
			continue
		}
		if mk == nil {
			continue
		}
		out, err := yaml.Marshal(&n)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(out))
		dec.KnownFields(true)
		if err := dec.Decode(mk()); err != nil && err.Error() != "EOF" {
			t.Errorf("section %s: %v", name, err)
		}
	}
	if llmSection != "" {
		var lc llm.Config
		if err := cfg.Section(llmSection, &lc); err != nil {
			t.Fatalf("section %s: %v", llmSection, err)
		}
		if _, err := llm.New(lc); err != nil {
			t.Errorf("section %s: llm.New: %v", llmSection, err)
		}
	}
	return cfg
}

func TestHubConfigs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"nexus.example.yaml (embedded)", configs.NexusExample},
		{"macbook/nexus.yaml", readFile(t, filepath.Join("macbook", "nexus.yaml"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := checkStrict(t, tc.body, hubSections, "llm")
			srv, err := hubserver.LoadConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if srv.Listen == "" || !srv.TLS.Auto {
				t.Errorf("server: listen=%q tls.auto=%v, want a listen address and TLS", srv.Listen, srv.TLS.Auto)
			}
			var sec security.Config
			if err := cfg.Section("security", &sec); err != nil {
				t.Fatal(err)
			}
			if len(sec.HubURLs) == 0 || len(sec.ServerNames) == 0 {
				t.Errorf("security: hub_urls=%v server_names=%v", sec.HubURLs, sec.ServerNames)
			}
			// `nexusd init` and scripts/mac/setup.sh replace this exact line.
			if !bytes.Contains(tc.body, []byte("\n  dev_token: CHANGE_ME\n")) {
				t.Error(`security.dev_token must be the placeholder "CHANGE_ME" (never a real token)`)
			}
		})
	}
}

func TestMacbookHubProfile(t *testing.T) {
	b := readFile(t, filepath.Join("macbook", "nexus.yaml"))
	cfg := checkStrict(t, b, hubSections, "llm")
	srv, _ := hubserver.LoadConfig(cfg)
	if srv.Listen != "0.0.0.0:7700" {
		t.Errorf("listen = %q", srv.Listen)
	}
	// setup.sh substitutes these placeholders.
	for _, s := range []string{"my-macbook.local", `web_dir: ""`} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("placeholder %q missing (scripts/mac/setup.sh relies on it)", s)
		}
	}
	var r router.Config
	if err := cfg.Section("router", &r); err != nil || r.Model != "fast-local" {
		t.Errorf("router.model = %q (%v), want fast-local", r.Model, err)
	}
	var lc llm.Config
	_ = cfg.Section("llm", &lc)
	for _, a := range []string{"fast-local", "embed"} {
		if lc.Aliases[a].Model == "" || lc.Aliases[a].Provider != "ollama" {
			t.Errorf("alias %s = %+v, want a pinned ollama model (setup.sh pulls it)", a, lc.Aliases[a])
		}
	}
}

func TestNodeConfigs(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"node.example.yaml", filepath.Join("..", "cmd", "nexus-node", "node.example.yaml")},
		{"macbook/node.yaml", filepath.Join("macbook", "node.yaml")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := checkStrict(t, readFile(t, tc.path), nodeSections, "node.llm")
			var nc node.Config
			if err := cfg.Section("node", &nc); err != nil {
				t.Fatal(err)
			}
			if nc.HubURL == "" || nc.NodeID == "" {
				t.Errorf("node: hub_url=%q node_id=%q", nc.HubURL, nc.NodeID)
			}
		})
	}
}

func TestMacbookNodeProfile(t *testing.T) {
	cfg := checkStrict(t, readFile(t, filepath.Join("macbook", "node.yaml")), nodeSections, "node.llm")
	var nc node.Config
	_ = cfg.Section("node", &nc)
	if nc.Token != "" {
		t.Error("node.token must stay empty in the profile: the MacBook node is paired")
	}
	want := map[string]bool{"mac": true, "dev": true, "browser": true}
	for _, tag := range nc.Tags {
		delete(want, tag)
	}
	if len(want) != 0 {
		t.Errorf("tags %v missing %v", nc.Tags, want)
	}
	var bc browser.Config
	if err := cfg.Section("runtime.browser", &bc); err != nil || bc.Headless == nil || !*bc.Headless {
		t.Errorf("runtime.browser.headless should be explicitly true (err %v)", err)
	}
}
