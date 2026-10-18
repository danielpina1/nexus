package llm

import (
	"errors"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Available is a cheap, offline health check: false only while every target
// of the alias is cooling down.
func TestAvailable(t *testing.T) {
	c := chainClient(t, "http://127.0.0.1:1", "http://127.0.0.1:2")
	now := time.Unix(1_000_000, 0)
	c.st.health.now = func() time.Time { return now }
	var _ kernel.LLMAvailability = c

	if !c.Available("smart") || !kernel.LLMAvailable(c, "smart") {
		t.Fatal("fresh client must be available")
	}
	down := errors.New("connection refused")
	trip := func(p string) {
		for i := 0; i < 2; i++ {
			c.st.health.failure(p, down)
		}
	}
	trip("cloud")
	if !c.Available("smart") {
		t.Fatal("fallback provider is healthy: alias must stay available")
	}
	trip("ollama")
	if c.Available("smart") || kernel.LLMAvailable(c, "smart") {
		t.Fatal("every target is cooling down: alias must be unavailable")
	}
	// Raw model names use the default provider.
	if c.Available("big") != c.st.health.available(c.DefaultProvider()) {
		t.Fatal("raw model availability must follow the default provider")
	}
	// The cool-down ends: available again (half-open probe allowed).
	now = now.Add(2 * time.Minute)
	if !c.Available("smart") {
		t.Fatal("cool-down over: alias must be available again")
	}
	// Copies share health state.
	trip("cloud")
	trip("ollama")
	if cp, ok := c.WithDefaultProvider("ollama").(kernel.LLMAvailability); !ok || cp.Available("smart") {
		t.Fatal("WithDefaultProvider copy must share health")
	}
}

type plainLLM struct{ kernel.LLM }

func TestLLMAvailableDefaultsToTrue(t *testing.T) {
	if !kernel.LLMAvailable(plainLLM{}, "x") {
		t.Fatal("LLMs without a health check are assumed available")
	}
}
