package llm

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The shipped MacBook hub profile must load: subscriptions only, no Ollama.
func TestMacBookProfileLoads(t *testing.T) {
	b, err := os.ReadFile("../../configs/macbook/nexus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		LLM Config `yaml:"llm"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	c, err := New(doc.LLM)
	if err != nil {
		t.Fatalf("profile llm section: %v", err)
	}
	for name, p := range doc.LLM.Providers {
		if !isCLIAPI(p.api()) {
			t.Errorf("provider %q (%s) is not a subscription CLI", name, p.api())
		}
	}
	for _, alias := range []string{"fast-local", "smart", "best"} {
		chain, err := c.ResolveChain(t.Context(), alias)
		if err != nil || len(chain) < 2 || chain[0].Provider != "claude" || chain[1].Provider != "codex" {
			t.Errorf("%s = %v, %v (want claude then codex)", alias, chain, err)
		}
	}
}
