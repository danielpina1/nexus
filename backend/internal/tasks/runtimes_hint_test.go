package tasks

import (
	"slices"
	"testing"
)

func TestPreferRuntime(t *testing.T) {
	base := []string{"claude-code", "codex", "ollama"}
	cases := []struct {
		hint string
		want []string
	}{
		{"codex", []string{"codex", "claude-code", "ollama"}},
		{"ollama", []string{"ollama", "claude-code", "codex"}},
		{"claude-code", base},
		{"shell", base}, // not a candidate: the hint never adds a runtime
		{"", base},
	}
	for _, c := range cases {
		if got := preferRuntime(slices.Clone(base), c.hint); !slices.Equal(got, c.want) {
			t.Errorf("preferRuntime(%q) = %v, want %v", c.hint, got, c.want)
		}
	}
	// Fallback after the hinted runtime fails covers every other runtime.
	if got := runtimesAfter(preferRuntime(base, "codex"), "codex"); !slices.Equal(got, []string{"claude-code", "ollama"}) {
		t.Errorf("fallback after hinted codex = %v", got)
	}
}
