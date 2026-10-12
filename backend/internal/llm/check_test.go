package llm

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func yamlLoad(path, section string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	n, ok := doc[section]
	if !ok {
		return nil
	}
	return n.Decode(out)
}

func runCLI(t *testing.T, cfgYAML string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nexus.yaml")
	if err := os.WriteFile(path, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := RunCLI(context.Background(), append(args, "-config", path, "-timeout", "5s"), CLIOptions{
		Prog: "nexusd", Section: "llm", DefaultConfig: "nexus.yaml", Load: yamlLoad,
		Stdout: &out, Stderr: &errOut,
		Getenv: func(k string) string { return env[k] },
	})
	return out.String() + errOut.String(), err
}

func TestCheckHealthyGateway(t *testing.T) {
	g := newFakeGateway(t, "qwen2.5:7b", "nomic-embed-text", "llama3.2:3b")
	g.key = "k1"
	srv := httptest.NewServer(g)
	defer srv.Close()
	cfg := `
llm:
  aliases_auto: true
  providers:
    gw:
      base_url: ` + srv.URL + `
      api_key_env: GW_KEY
  aliases:
    smart: { provider: gw, model: CHANGE_ME }
    fast-local: { provider: gw, model: "llama3.2:3b" }
`
	out, err := runCLI(t, cfg, map[string]string{"GW_KEY": "k1"}, "check")
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	for _, want := range []string{
		"gw: models", "3 models",
		"alias smart", "gw/qwen2.5:7b", "auto",
		"alias embed", "gw/nomic-embed-text",
		"chat", `"OK"`, "tools", "nexus.probe", "json", "stream", "deltas", "embed", "dim 3",
		"bearer $GW_KEY", "0 failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, "✗") {
		t.Error("unexpected failure row")
	}
}

func TestCheckFailures(t *testing.T) {
	g := newFakeGateway(t, "qwen2.5:7b")
	g.key = "right"
	srv := httptest.NewServer(g)
	defer srv.Close()
	closed := httptest.NewServer(g)
	closed.Close()

	tests := []struct {
		name  string
		base  string
		env   map[string]string
		wants []string
	}{
		{"401 wrong key", srv.URL, map[string]string{"GW_KEY": "wrong"}, []string{"HTTP 401", "was rejected", "chat"}},
		{"401 empty env", srv.URL, nil, []string{"(EMPTY)", "$GW_KEY is empty"}},
		{"404 wrong base path", srv.URL + "/api/openai", map[string]string{"GW_KEY": "right"}, []string{"HTTP 404", "base_url is probably wrong", "/api/openai/models"}},
		{"unreachable", closed.URL, map[string]string{"GW_KEY": "right"}, []string{"connection refused", "skipped: provider unreachable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := `
llm:
  providers:
    gw: { base_url: "` + tt.base + `", api_key_env: GW_KEY }
  aliases:
    smart: { provider: gw, model: "qwen2.5:7b" }
`
			out, err := runCLI(t, cfg, tt.env, "check", "-quick")
			t.Logf("\n%s", out)
			if !errors.Is(err, ErrCheckFailed) {
				t.Fatalf("err %v", err)
			}
			for _, w := range tt.wants {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q", w)
				}
			}
		})
	}
}

func TestCheckModelWithoutTools(t *testing.T) {
	g := newFakeGateway(t, "tiny")
	g.noTools = true
	srv := httptest.NewServer(g)
	defer srv.Close()
	c, err := New(Config{
		Providers: map[string]ProviderConfig{"gw": {BaseURL: srv.URL}},
		Aliases:   map[string]Alias{"fast-local": {Provider: "gw", Model: "tiny"}, "smart": {Provider: "gw", Model: "missing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := Check(context.Background(), c, CheckOptions{Timeout: 5 * time.Second})
	var buf bytes.Buffer
	rep.Print(&buf)
	out := buf.String()
	t.Logf("\n%s", out)
	if !rep.Failed() || !contains(out, "answered without calling the tool", "does not list model \"missing\"", "is not served by gw") {
		t.Fatal("expected tool warning and missing-model failure")
	}
}

func TestCLIModelsAndStatus(t *testing.T) {
	srv := httptest.NewServer(newFakeGateway(t, "qwen2.5:7b", "nomic-embed-text"))
	defer srv.Close()
	cfg := `
llm:
  aliases_auto: true
  providers:
    ollama: { base_url: "` + srv.URL + `", auth: none }
  aliases:
    smart: { provider: ollama, model: "qwen2.5:7b" }
`
	out, err := runCLI(t, cfg, nil, "models")
	t.Logf("\n%s", out)
	if err != nil || !contains(out, "qwen2.5:7b", "smart", "fast-local (auto)", "nomic-embed-text", "embed (auto)") {
		t.Fatalf("models: %v", err)
	}
	out, err = runCLI(t, cfg, nil, "status")
	t.Logf("\n%s", out)
	if err != nil || !contains(out, "ollama", "/v1", "smart", "configured", "auto") {
		t.Fatalf("status: %v", err)
	}
	if _, err := runCLI(t, "llm: {}\n", nil, "check"); err == nil || !strings.Contains(err.Error(), "no providers") {
		t.Fatalf("empty config: %v", err)
	}
	if _, err := runCLI(t, cfg, nil, "bogus"); err == nil {
		t.Fatal("unknown subcommand accepted")
	}
}
