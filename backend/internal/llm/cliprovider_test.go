package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
)

// fakeCLI writes an executable shell script and returns its path.
func fakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeCLIChat(t *testing.T) {
	// Echoes its arguments and stdin back inside the result so the test can
	// check the flags and prompt the hub sends.
	bin := fakeCLI(t, "claude", `in=$(cat)
args="$*"
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","total_cost_usd":0.01,"usage":{"input_tokens":12,"output_tokens":3}}\n' "$(printf '%s|%s' "$args" "$in" | tr '\n"' '  ')"
`)
	c, err := New(Config{Providers: map[string]ProviderConfig{"claude": {API: APIClaudeCLI, Binary: bin}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "haiku", JSON: true, Messages: []kernel.ChatMessage{
		{Role: "system", Content: "You route requests."},
		{Role: "user", Content: "buy milk"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Message.Content
	for _, want := range []string{"-p", "--output-format json", "--max-turns 1", "--model haiku", "You route requests.", "JSON object only", "|buy milk"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if res.Cost.GetInputTokens() != 12 || res.Cost.GetOutputTokens() != 3 {
		t.Errorf("cost = %+v", res.Cost)
	}
}

func TestCodexCLIChatAndFenceStripping(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events.jsonl")
	events := `{"type":"thread.started"}
{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"type":"agent_message","text":"` + "```json\\n{\\\"agent_id\\\":\\\"shop\\\"}\\n```" + `"}}
{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":5}}
`
	if err := os.WriteFile(out, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := fakeCLI(t, "codex", "cat >/dev/null; cat '"+out+"'\n")
	c, err := New(Config{Providers: map[string]ProviderConfig{"codex": {API: APICodexCLI, Binary: bin}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "gpt-5", JSON: true, Messages: []kernel.ChatMessage{{Role: "user", Content: "route"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != `{"agent_id":"shop"}` {
		t.Fatalf("content = %q", res.Message.Content)
	}
	if res.Cost.GetOutputTokens() != 5 {
		t.Errorf("cost = %+v", res.Cost)
	}
}

// A missing or logged-out Claude CLI falls back to Codex through the alias chain.
func TestCLIFallbackAndErrors(t *testing.T) {
	codex := fakeCLI(t, "codex", `cat >/dev/null; echo '{"type":"item.completed","item":{"type":"agent_message","text":"from codex"}}'`)
	loggedOut := fakeCLI(t, "claude", `cat >/dev/null; echo "Invalid API key · Please run /login" >&2; exit 1`)
	for name, claudeBin := range map[string]string{"missing": filepath.Join(t.TempDir(), "no-such-claude"), "logged out": loggedOut} {
		c, err := New(Config{
			Providers: map[string]ProviderConfig{
				"claude": {API: APIClaudeCLI, Binary: claudeBin},
				"codex":  {API: APICodexCLI, Binary: codex},
			},
			Aliases: map[string]Alias{"smart": {Provider: "claude", Model: "sonnet", Fallback: []Target{{Provider: "codex", Model: "gpt-5"}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "smart", Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Message.Content != "from codex" {
			t.Fatalf("%s: content = %q", name, res.Message.Content)
		}
	}
	c, _ := New(Config{Providers: map[string]ProviderConfig{"claude": {API: APIClaudeCLI, Binary: loggedOut}}})
	if _, err := c.Embed(context.Background(), "haiku", []string{"x"}); err == nil {
		t.Fatal("embeddings should be refused")
	}
	if models, err := c.ListModels(context.Background(), "claude"); err != nil || len(models) == 0 {
		t.Fatalf("models = %v, %v", models, err)
	}
}
