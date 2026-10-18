package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Runtime selection (docs/AGENTS.md "Runtime auto").
//
// An agent names its runtime with `runtime:` (one runtime), `runtime: auto`
// (the tier's list from policies/models.md `runtimes:`), or `runtimes: [a,
// b, …]` (its own ordered list). For each run the engine dispatches to the
// first runtime of the list that a connected node matching the agent's node
// tags reports as available, and puts it into the assignment's agent copy.
// When a run fails with result.error_kind rate_limited (usage limit) or auth
// (CLI not logged in), the task is retried on the next runtimes of the list.

// PolicyModelsPath is the vault note holding runtime preferences.
const PolicyModelsPath = "policies/models.md"

// DefaultRuntimePrefs are used when the policy note cannot be read or has
// no `runtimes:` entry for a tier: subscription CLIs first for real work,
// local Ollama first for cheap work.
var DefaultRuntimePrefs = map[string][]string{
	"simple": {"ollama", "codex", "claude-code"},
	"medium": {"claude-code", "codex", "ollama"},
	"heavy":  {"claude-code", "codex"},
}

// fallbackKinds are the result.error_kind values that move a task to the
// next runtime of its list.
var fallbackKinds = map[string]bool{kernel.ErrorKindRateLimited: true, kernel.ErrorKindAuth: true}

// runtimePolicy caches policies/models.md `runtimes:` by file mtime.
type runtimePolicy struct {
	mu    sync.Mutex
	path  string
	mod   time.Time
	prefs map[string][]string
}

// prefs returns the per-tier runtime lists: the policy note's entries over
// DefaultRuntimePrefs.
func (e *Engine) runtimePrefs() map[string][]string {
	out := map[string][]string{}
	for k, v := range DefaultRuntimePrefs {
		out[k] = v
	}
	v, ok := lookup[kernel.Vault](e.h, kernel.SvcVault)
	if !ok || v.Root() == "" {
		return out
	}
	p := filepath.Join(v.Root(), filepath.FromSlash(PolicyModelsPath))
	fi, err := os.Stat(p)
	if err != nil {
		return out
	}
	c := &e.rtPolicy
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != p || !c.mod.Equal(fi.ModTime()) {
		c.path, c.mod, c.prefs = p, fi.ModTime(), nil
		if b, err := os.ReadFile(p); err == nil {
			c.prefs = ParseRuntimePrefs(b)
		} else {
			e.log.Warn("runtime policy unreadable; using defaults", "path", p, "err", err)
		}
	}
	for k, v := range c.prefs {
		out[k] = v
	}
	return out
}

// ParseRuntimePrefs reads `runtimes: {tier: [runtime, …]}` from a policy
// note's frontmatter. Malformed entries are skipped.
func ParseRuntimePrefs(src []byte) map[string][]string {
	n := note.Parse(PolicyModelsPath, src)
	m, ok := n.Frontmatter["runtimes"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for tier, v := range m {
		if list := stringList(v); len(list) > 0 {
			out[strings.ToLower(strings.TrimSpace(tier))] = list
		}
	}
	return out
}

// stringList accepts a YAML list of strings or a comma-separated string.
func stringList(v any) []string {
	var raw []string
	switch t := v.(type) {
	case string:
		raw = strings.Split(t, ",")
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" && s != kernel.RuntimeAuto {
			out = append(out, s)
		}
	}
	return dedupe(out)
}

// RuntimeCandidates is the ordered runtime list for an agent at a tier:
// the note's `runtimes:` list when present, the tier's policy list for
// `runtime: auto` (or no runtime), else the single configured runtime.
func RuntimeCandidates(agent *nexusv1.Agent, markdown string, tier nexusv1.Tier, prefs map[string][]string) []string {
	if markdown != "" {
		n := note.Parse("", []byte(markdown))
		if list := stringList(n.Frontmatter["runtimes"]); len(list) > 0 {
			return list
		}
	}
	rt := strings.TrimSpace(agent.GetRuntime())
	if rt != "" && rt != kernel.RuntimeAuto {
		return []string{rt}
	}
	name := tierName(tier)
	if tier == nexusv1.Tier_TIER_UNSPECIFIED {
		name = "medium"
	}
	if list := prefs[name]; len(list) > 0 {
		return append([]string(nil), list...)
	}
	return append([]string(nil), DefaultRuntimePrefs["medium"]...)
}

// runtimesAfter returns the candidates after current (nil when current is
// not in the list or is the last one).
func runtimesAfter(cands []string, current string) []string {
	for i, c := range cands {
		if c == current {
			return append([]string(nil), cands[i+1:]...)
		}
	}
	return nil
}

// runtimeFallback decides whether a FAILED run should be retried on another
// runtime: its result.error_kind is rate_limited or auth and the agent's
// runtime list has runtimes after the run's. It returns them (nil = no).
func (e *Engine) runtimeFallback(ctx context.Context, run *runRec, nt *nexusv1.Task) []string {
	kind := nt.GetResult().GetFields()["error_kind"].GetStringValue()
	if !fallbackKinds[kind] || run.runtime == "" {
		return nil
	}
	cat, ok := e.agents()
	if !ok {
		return nil
	}
	agent, _, markdown, err := cat.GetAgent(ctx, run.agentID)
	if err != nil {
		return nil
	}
	cands := RuntimeCandidates(agent, markdown, run.tier, e.runtimePrefs())
	// A router-preferred runtime was moved to the front at assignment; keep
	// that order so its failure still falls back through the whole list.
	if r, err := e.loadTask(ctx, run.taskID); err == nil {
		cands = preferRuntime(cands, r.req.GetParams().GetFields()[ParamRuntimeHint].GetStringValue())
	}
	return runtimesAfter(cands, run.runtime)
}

// ParamRuntimeHint is the task/route param carrying the learning router's
// preferred runtime. It only reorders the candidates; it never adds one.
const ParamRuntimeHint = "runtime_hint"

// preferRuntime moves rt to the front of cands when it is one of them.
func preferRuntime(cands []string, rt string) []string {
	for i, c := range cands {
		if c == rt && i > 0 {
			out := make([]string, 0, len(cands))
			out = append(out, rt)
			out = append(out, cands[:i]...)
			return append(out, cands[i+1:]...)
		}
	}
	return cands
}

// runtimeLabel is the human name of a runtime.
func runtimeLabel(name string) string {
	switch name {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "ollama":
		return "Ollama"
	}
	return name
}
