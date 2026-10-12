package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/llm"
)

// fakeModels is a minimal OpenAI-compatible server.
func fakeModels() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"qwen2.5:7b"}]}`)
		case "/v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
}

func writeLLMConfig(t *testing.T, base string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	cfg := "llm:\n  providers:\n    ollama: { base_url: \"" + base + "\", auth: none }\n  aliases:\n    smart: { provider: ollama, model: \"qwen2.5:7b\" }\n"
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCmdLLM(t *testing.T) {
	srv := fakeModels()
	defer srv.Close()
	if err := cmdLLM([]string{"check", "-quick", "-config", writeLLMConfig(t, srv.URL)}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := cmdLLM([]string{"models", "-config", writeLLMConfig(t, srv.URL)}); err != nil {
		t.Fatalf("models: %v", err)
	}
	srv.Close()
	if err := cmdLLM([]string{"check", "-quick", "-config", writeLLMConfig(t, srv.URL)}); !errors.Is(err, llm.ErrCheckFailed) {
		t.Fatalf("check against a stopped server: %v", err)
	}
	if err := cmdLLM([]string{"check", "-config", filepath.Join(t.TempDir(), "missing.yaml")}); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("missing config: %v", err)
	}
}
