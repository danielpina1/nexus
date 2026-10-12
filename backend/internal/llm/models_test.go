package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
)

func TestParseModelList(t *testing.T) {
	tests := []struct {
		name, body string
		want       []string
	}{
		{"openai", `{"object":"list","data":[{"id":"a"},{"id":"b"}]}`, []string{"a", "b"}},
		{"ollama native", `{"models":[{"name":"qwen2.5:7b","model":"qwen2.5:7b"}]}`, []string{"qwen2.5:7b"}},
		{"bare array", `[{"id":"x"}]`, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, _, _, err := parseModelList([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, m := range ms {
				ids = append(ids, m.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v", ids)
			}
		})
	}
	if _, _, _, err := parseModelList([]byte(`{"hello":1}`)); err == nil {
		t.Fatal("expected error for unrecognised list")
	}
}

func TestAutoAliases(t *testing.T) {
	local := newFakeGateway(t, "llama3.2:3b", "nomic-embed-text:latest", "qwen2.5:14b", "qwen2.5:7b")
	cloud := newFakeGateway(t, "claude-3-5-sonnet", "claude-opus-4-1", "claude-sonnet-4-5", "text-embedding-3-small")
	ls, cs := httptest.NewServer(local), httptest.NewServer(cloud)
	defer ls.Close()
	defer cs.Close()
	cfg := Config{
		DefaultProvider: "ollama",
		AliasesAuto:     true,
		Providers: map[string]ProviderConfig{
			"ollama": {BaseURL: ls.URL},
			"cloud":  {BaseURL: cs.URL},
		},
		Aliases: map[string]Alias{
			"fast-local": {Provider: "ollama", Model: "CHANGE_ME"}, // auto, restricted to ollama
			"smart":      {Model: "CHANGE_ME"},                     // auto across providers
			"best":       {Provider: "cloud", Model: "gpt-pinned"}, // explicit wins
			"embed":      {Provider: "ollama", Model: ""},          // auto, ollama
			"coder":      {Model: "auto"},                          // custom preference
			"nothing":    {Provider: "ollama", Model: "CHANGE_ME"}, // no preferences
		},
		AliasPreferences: map[string][]string{"coder": {"qwen2.5:14"}},
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// No network until a request needs it.
	if _, _, err := c.Resolve("smart"); err == nil {
		t.Fatal("Resolve should not discover")
	}
	auto, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Target{
		"fast-local": {"ollama", "llama3.2:3b"},
		"smart":      {"cloud", "claude-sonnet-4-5"}, // "sonnet" preference beats local qwen; greatest id wins
		"embed":      {"ollama", "nomic-embed-text:latest"},
		"coder":      {"ollama", "qwen2.5:14b"},
	}
	for a, w := range want {
		if auto[a] != w {
			t.Errorf("%s = %v want %v", a, auto[a], w)
		}
	}
	if _, ok := auto["best"]; ok {
		t.Error("explicit alias must not be auto-filled")
	}
	p, m, _ := c.Resolve("best")
	if p != "cloud" || m != "gpt-pinned" {
		t.Errorf("best %s/%s", p, m)
	}
	if _, err := c.ResolveChain(context.Background(), "nothing"); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("nothing: %v", err)
	}
	table := c.AliasTable(context.Background())
	for _, a := range table {
		if a.Name == "smart" && !a.Auto {
			t.Errorf("smart should be marked auto: %+v", a)
		}
		if a.Name == "best" && a.Auto {
			t.Errorf("best is configured: %+v", a)
		}
	}
}

func TestAutoAliasLazyDiscoveryOnChat(t *testing.T) {
	g := newFakeGateway(t, "qwen2.5:7b")
	srv := httptest.NewServer(g)
	defer srv.Close()
	c, err := New(Config{
		AliasesAuto: true,
		Providers:   map[string]ProviderConfig{"ollama": {BaseURL: srv.URL}},
		Aliases:     map[string]Alias{"fast-local": {Provider: "ollama", Model: "CHANGE_ME"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		resp, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "fast-local", Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Message.Content != "OK" {
			t.Fatalf("content %q", resp.Message.Content)
		}
	}
	if g.lastReq["model"] != "qwen2.5:7b" {
		t.Errorf("model %v", g.lastReq["model"])
	}
	if n := g.count("/v1/models"); n != 1 {
		t.Errorf("models listed %d times, want 1", n)
	}
	// Standard alias that is not configured at all is auto-filled too.
	p, m, err := c.Resolve("smart")
	if err != nil || p != "ollama" || m != "qwen2.5:7b" {
		t.Errorf("smart → %s/%s %v", p, m, err)
	}
}

func TestListModelsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<!DOCTYPE html><html>web ui</html>")
	}))
	defer srv.Close()
	c, _ := New(Config{Providers: map[string]ProviderConfig{"p": {BaseURL: srv.URL}}})
	if _, err := c.ListModels(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err %v", err)
	}
}
