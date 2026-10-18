package node

import (
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// codeOnlyPrefixes are tool families reserved for coding agents. Anthrex
// drives repositories (worktrees, merges, terminals), so other agents never
// receive these tools, whatever their allow-list, a team orchestrator or the
// Agent Architect says.
var codeOnlyPrefixes = []string{"anthrex."}

// IsCodeAgent reports whether the run's agent is a coding agent: it runs on
// the anthrex runtime, or its forwarded claude/codex settings say coding: true.
func IsCodeAgent(a *nexusv1.RunAssignment) bool {
	if a.GetAgent().GetRuntime() == "anthrex" {
		return true
	}
	ctx := a.GetContext().AsMap()
	for _, key := range []string{"claude", "codex"} {
		if m, ok := ctx[key].(map[string]any); ok {
			if v, ok := m["coding"].(bool); ok && v {
				return true
			}
		}
	}
	return false
}

// codeOnlyTools drops code-only tools from non-coding agents' runs.
func codeOnlyTools(a *nexusv1.RunAssignment, tools []kernel.Tool) []kernel.Tool {
	if IsCodeAgent(a) {
		return tools
	}
	out := tools[:0:0]
	for _, t := range tools {
		if isCodeOnlyTool(t.Spec().Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isCodeOnlyTool(name string) bool {
	for _, p := range codeOnlyPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
