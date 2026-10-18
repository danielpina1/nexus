package learn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Candidate kinds.
const (
	KindAgent    = "agent"
	KindTeam     = "team"
	KindWorkflow = "workflow"

	// TeamPrefix / WorkflowPrefix mark candidate ids of teams and workflows
	// ("team:<id>" is also what the task engine accepts as an agent id).
	TeamPrefix     = "team:"
	WorkflowPrefix = "workflow:"
)

// Candidate is something a request can be routed to.
type Candidate struct {
	ID          string // agent id, "team:<id>" or "workflow:<id>"
	Kind        string
	Name        string
	Description string
	// Examples are sample requests from the note's `examples:` list (agents)
	// or trigger phrases (teams, workflow intents).
	Examples []string
	Tier     nexusv1.Tier
	// Runtimes are the bandit's arms (empty when the runtime is fixed).
	Runtimes []string
	// HasInputSchema: the agent validates params, so no runtime hint is
	// added to the route (it would not match the schema).
	HasInputSchema bool
}

// Texts returns what is embedded for the description stage.
func (c Candidate) Texts() []string {
	out := []string{strings.TrimSpace(c.Name + ": " + c.Description)}
	for _, e := range c.Examples {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// DefaultRuntimePrefs mirror the task engine's built-in `runtime: auto`
// lists (docs/AGENTS.md §2.1), used when policies/models.md is missing.
var DefaultRuntimePrefs = map[string][]string{
	"simple": {"ollama", "codex", "claude-code"},
	"medium": {"claude-code", "codex", "ollama"},
	"heavy":  {"claude-code", "codex"},
}

// PolicyModelsPath is the vault note with the per-tier runtime lists.
const PolicyModelsPath = "policies/models.md"

type prefsCache struct {
	mu    sync.Mutex
	path  string
	mod   time.Time
	prefs map[string][]string
}

func (p *prefsCache) get(h kernel.Host) map[string][]string {
	out := map[string][]string{}
	for k, v := range DefaultRuntimePrefs {
		out[k] = v
	}
	v, err := kernel.Resolve[kernel.Vault](h, kernel.SvcVault)
	if err != nil || v.Root() == "" {
		return out
	}
	path := filepath.Join(v.Root(), filepath.FromSlash(PolicyModelsPath))
	fi, err := os.Stat(path)
	if err != nil {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path != path || !p.mod.Equal(fi.ModTime()) {
		p.path, p.mod, p.prefs = path, fi.ModTime(), nil
		if b, err := os.ReadFile(path); err == nil {
			n := note.Parse(PolicyModelsPath, b)
			if m, ok := n.Frontmatter["runtimes"].(map[string]any); ok {
				p.prefs = map[string][]string{}
				for tier, list := range m {
					if l := runtimeList(list); len(l) > 0 {
						p.prefs[strings.ToLower(tier)] = l
					}
				}
			}
		}
	}
	for k, v := range p.prefs {
		out[k] = v
	}
	return out
}

func runtimeList(v any) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range note.StringList(v) {
		s = strings.TrimSpace(s)
		if s != "" && s != kernel.RuntimeAuto && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func tierName(t nexusv1.Tier) string {
	switch t {
	case nexusv1.Tier_TIER_SIMPLE:
		return "simple"
	case nexusv1.Tier_TIER_HEAVY:
		return "heavy"
	}
	return "medium"
}

// agentCandidate builds the candidate of an agent; markdown may be empty.
func agentCandidate(a *nexusv1.Agent, markdown string, prefs map[string][]string) Candidate {
	c := Candidate{ID: a.Id, Kind: KindAgent, Name: a.Name, Description: a.Description, Tier: a.Tier,
		HasInputSchema: len(a.GetInputSchema().GetFields()) > 0}
	var fm map[string]any
	if markdown != "" {
		fm = note.Parse("", []byte(markdown)).Frontmatter
		c.Examples = note.StringList(fm["examples"])
	}
	if list := runtimeList(fm["runtimes"]); len(list) > 0 {
		c.Runtimes = list
	} else if rt := strings.TrimSpace(a.Runtime); rt == "" || rt == kernel.RuntimeAuto {
		c.Runtimes = append([]string(nil), prefs[tierName(a.Tier)]...)
	}
	return c
}

// workflowLister is the part of the workflow service the router needs.
type workflowLister interface {
	List(ctx context.Context) ([]*workflow.Workflow, error)
}

// candidates collects agents (given, or from the catalog), teams and
// workflows. Missing services are skipped.
func (l *Learner) candidates(ctx context.Context, agents []*nexusv1.Agent) []Candidate {
	prefs := l.prefs.get(l.h)
	cat, catErr := kernel.Resolve[kernel.AgentCatalog](l.h, kernel.SvcAgents)
	if agents == nil && catErr == nil {
		if list, err := cat.ListAgents(ctx, false); err == nil {
			agents = list
		}
	}
	var out []Candidate
	for _, a := range agents {
		// The router lists teams as pseudo-agents "team:<id>"; they are
		// added below from the team catalog.
		if a == nil || !a.Enabled || a.Id == "" || strings.HasPrefix(a.Id, TeamPrefix) {
			continue
		}
		md := ""
		if catErr == nil {
			if _, _, m, err := cat.GetAgent(ctx, a.Id); err == nil {
				md = m
			}
		}
		out = append(out, agentCandidate(a, md, prefs))
	}
	if tc, err := kernel.Resolve[kernel.TeamCatalog](l.h, kernel.SvcTeams); err == nil {
		if teams, err := tc.ListTeams(ctx, false); err == nil {
			for _, t := range teams {
				if t == nil || !t.Enabled || t.Id == "" {
					continue
				}
				out = append(out, Candidate{ID: TeamPrefix + t.Id, Kind: KindTeam, Name: t.Name,
					Description: t.Description, Examples: t.Triggers, Tier: nexusv1.Tier_TIER_HEAVY})
			}
		}
	}
	if v, ok := l.h.Lookup(workflow.SvcWorkflows); ok {
		if wl, ok := v.(workflowLister); ok {
			if wfs, err := wl.List(ctx); err == nil {
				for _, w := range wfs {
					if w == nil || !w.Enabled || !w.Valid() || w.ID == "" {
						continue
					}
					c := Candidate{ID: WorkflowPrefix + w.ID, Kind: KindWorkflow, Name: w.Name, Description: w.Description}
					for _, t := range w.Triggers {
						if t.Kind == workflow.TriggerIntent {
							c.Examples = append(c.Examples, t.Phrases...)
						}
					}
					out = append(out, c)
				}
			}
		}
	}
	return out
}
