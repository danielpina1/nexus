package agentdef

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func templateAgent(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "vault-template", "agents", name))
	if err != nil {
		t.Skipf("vault-template not available: %v", err)
	}
	return b
}

func TestTemplateAgents(t *testing.T) {
	for _, name := range []string{"assistant.md", "code-fixer.md", "grocery-continente.md", "librarian.md", "ops.md"} {
		t.Run(name, func(t *testing.T) {
			a, instr := Parse("agents/"+name, templateAgent(t, name))
			if len(a.Errors) > 0 || !a.Enabled {
				t.Fatalf("errors: %v", a.Errors)
			}
			if a.Id != strings.TrimSuffix(name, ".md") || a.Description == "" || a.Runtime == "" || instr == "" {
				t.Fatalf("bad agent: %+v", a)
			}
			if strings.HasPrefix(instr, "---") {
				t.Fatal("instructions include frontmatter")
			}
		})
	}
}

func TestGrocery(t *testing.T) {
	a, instr := Parse("agents/grocery-continente.md", templateAgent(t, "grocery-continente.md"))
	want := &nexusv1.Agent{
		Id:          "grocery-continente",
		Name:        "Demeter · Groceries (Continente)",
		Description: `Buys groceries on continente.pt from a list and guidelines. Use for shopping lists, supermarket orders, "buy the usual".`,
		Icon:        "cart",
		Runtime:     "auto",
		Model:       "smart",
		Tier:        nexusv1.Tier_TIER_MEDIUM,
		NodeTags:    []string{"browser"},
		Tools:       []string{"browser", "memory.search", "vault.read", "vault.write", "ask_user", "request_approval", "notify"},
		Limits: &nexusv1.AgentLimits{
			MaxRuntimeSeconds: 1800,
			MaxCost:           &nexusv1.Money{Currency: "EUR", MinorUnits: 50},
			MaxSpend:          &nexusv1.Money{Currency: "EUR", MinorUnits: 15000},
		},
		Policy:  map[string]nexusv1.TrustLevel{"spend": nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC},
		Note:    &nexusv1.NoteRef{Id: "grocery-continente", Path: "agents/grocery-continente.md", Title: "Demeter · Groceries (Continente)"},
		Version: 2,
		Enabled: true,
		Memory:  &nexusv1.AgentMemory{Reads: []string{"[[Shopping preferences]]", "products/"}, Writes: "episodes/shopping/"},
	}
	if !proto.Equal(a, want) {
		t.Fatalf("got  %v\nwant %v", a, want)
	}
	if !strings.HasPrefix(instr, "# Grocery (Continente)") {
		t.Fatalf("instructions = %q", instr[:40])
	}
}

func TestOpsPolicyAndHeavyTier(t *testing.T) {
	a, _ := Parse("agents/ops.md", templateAgent(t, "ops.md"))
	want := map[string]nexusv1.TrustLevel{
		"compute":        nexusv1.TrustLevel_TRUST_LEVEL_AUTO,
		"destructive":    nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC,
		"device_control": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE,
	}
	if !reflect.DeepEqual(a.Policy, want) {
		t.Fatalf("policy = %v", a.Policy)
	}
	c, _ := Parse("agents/code-fixer.md", templateAgent(t, "code-fixer.md"))
	if c.Tier != nexusv1.Tier_TIER_HEAVY || c.Limits.MaxCost.MinorUnits != 500 || c.Limits.MaxRuntimeSeconds != 3600 {
		t.Fatalf("code-fixer = %v", c)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		errs    []string // substrings, in order
		enabled bool
	}{
		{"minimal ok", "---\nid: x\ndescription: d\nruntime: shell\n---\nbody", nil, true},
		{"disabled", "---\nid: x\ndescription: d\nruntime: shell\nenabled: false\n---\n", nil, false},
		{"id from filename", "---\ndescription: d\nruntime: shell\n---\n", nil, true},
		{"no frontmatter", "# just text", []string{"missing frontmatter", "missing description", "missing runtime"}, false},
		{"bad yaml", "---\nid: [\n---\n", []string{"frontmatter", "missing description", "missing runtime"}, false},
		{"wrong type", "---\ntype: workflow\nid: x\ndescription: d\nruntime: shell\n---\n", []string{"type is"}, false},
		{"bad id", "---\nid: Bad Id\ndescription: d\nruntime: shell\n---\n", []string{"invalid id"}, false},
		{"bad tier", "---\nid: x\ndescription: d\nruntime: shell\ntier: huge\n---\n", []string{"invalid tier"}, false},
		{"bad limits", "---\nid: x\ndescription: d\nruntime: shell\nlimits: {max_runtime: soon, max_cost_eur: lots, max_subtasks: -1}\n---\n",
			[]string{"max_cost_eur", "max_runtime", "max_subtasks"}, false},
		{"bad policy", "---\nid: x\ndescription: d\nruntime: shell\npolicy: {spend: maybe}\n---\n", []string{"policy.spend"}, false},
		{"policy not map", "---\nid: x\ndescription: d\nruntime: shell\npolicy: approve\n---\n", []string{"policy must be"}, false},
		{"bad tools", "---\nid: x\ndescription: d\nruntime: shell\ntools: [a, {b: c}]\n---\n", []string{"tools must be"}, false},
		{"bad version", "---\nid: x\ndescription: d\nruntime: shell\nversion: v2\n---\n", []string{"version"}, false},
		{"bad enabled", "---\nid: x\ndescription: d\nruntime: shell\nenabled: yes please\n---\n", []string{"enabled"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := Parse("agents/file-id.md", []byte(tt.src))
			if a.Enabled != tt.enabled {
				t.Errorf("enabled = %v, errors %v", a.Enabled, a.Errors)
			}
			if len(a.Errors) != len(tt.errs) {
				t.Fatalf("errors = %q, want %d", a.Errors, len(tt.errs))
			}
			for i, s := range tt.errs {
				if !strings.Contains(a.Errors[i], s) {
					t.Errorf("error[%d] = %q, want ~%q", i, a.Errors[i], s)
				}
			}
		})
	}
	a, _ := Parse("agents/file-id.md", []byte("---\ndescription: d\nruntime: shell\n---\n"))
	if a.Id != "file-id" {
		t.Fatalf("id = %q", a.Id)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[any]time.Duration{
		"30m": 30 * time.Minute, "1h30m": 90 * time.Minute, "2d": 48 * time.Hour, "90": 90 * time.Second,
		90: 90 * time.Second, 1.5: 1500 * time.Millisecond, "": 0,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%v) = %v, %v", in, got, err)
		}
	}
	for _, in := range []any{"soon", "-5m", true} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%v) should fail", in)
		}
	}
}

func TestParseEUR(t *testing.T) {
	for in, want := range map[any]int64{0.5: 50, 150: 15000, "0.50": 50, "€1,20": 120, "2 EUR": 200, 0.105: 11, 5: 500} {
		m, err := ParseEUR(in)
		if err != nil || m.MinorUnits != want || m.Currency != "EUR" {
			t.Errorf("ParseEUR(%v) = %v, %v", in, m, err)
		}
	}
	for _, in := range []any{"lots", -1, true} {
		if _, err := ParseEUR(in); err == nil {
			t.Errorf("ParseEUR(%v) should fail", in)
		}
	}
}

func TestNodeForms(t *testing.T) {
	for src, want := range map[string][]string{
		"node: any":                 {"any"},
		"node: [gpu, dev]":          {"gpu", "dev"},
		"node: { tags: [browser] }": {"browser"},
	} {
		a, _ := Parse("agents/x.md", []byte("---\nid: x\ndescription: d\nruntime: shell\n"+src+"\n---\n"))
		if !reflect.DeepEqual(a.NodeTags, want) || len(a.Errors) > 0 {
			t.Errorf("%s → %v %v", src, a.NodeTags, a.Errors)
		}
	}
}

func TestRuntimeAutoAndList(t *testing.T) {
	for _, tc := range []struct {
		name, fm, runtime string
		ok                bool
	}{
		{"auto", "runtime: auto\n", "auto", true},
		{"list implies auto", "runtimes: [claude-code, codex]\n", "auto", true},
		{"list with runtime", "runtime: claude-code\nruntimes: [claude-code, codex]\n", "claude-code", true},
		{"bad list entry", "runtimes: [claude-code, \"Bad Name\"]\n", "auto", false},
		{"bad list type", "runtime: auto\nruntimes: {a: b}\n", "auto", false},
		{"missing", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "---\ntype: agent\nid: x\ndescription: d\n" + tc.fm + "---\n# X\n"
			a, _ := Parse("agents/x.md", []byte(src))
			if a.Runtime != tc.runtime || (len(a.Errors) == 0) != tc.ok || a.Enabled != tc.ok {
				t.Fatalf("runtime %q errors %v enabled %v", a.Runtime, a.Errors, a.Enabled)
			}
		})
	}
}

func TestComplexAgentFields(t *testing.T) {
	src := `---
type: agent
id: analyst
description: Analyses things.
runtime: auto
skills: [web-research, critique]
inputs:
  type: object
  required: [topic]
  properties: {topic: {type: string}}
output:
  type: object
  required: [verdict]
  properties: {verdict: {type: string, enum: [yes, no]}, reasons: {type: array, items: {type: string}}}
review: {critic: critic, rounds: 2, criteria: "Every claim has a source."}
limits: {max_depth: 3, max_parallel: 2, max_tasks: 10, max_runs: 20, max_tree_cost_eur: 2.5}
---
Body`
	a, _ := Parse("agents/analyst.md", []byte(src))
	if len(a.Errors) > 0 {
		t.Fatalf("errors: %v", a.Errors)
	}
	if got := a.Skills; len(got) != 2 || got[0] != "web-research" {
		t.Fatalf("skills = %v", got)
	}
	if a.InputSchema.GetFields()["type"].GetStringValue() != "object" {
		t.Fatalf("input schema = %v", a.InputSchema)
	}
	if req := a.OutputSchema.GetFields()["required"].GetListValue().GetValues(); len(req) != 1 {
		t.Fatalf("output schema = %v", a.OutputSchema)
	}
	if r := a.Review; r.GetCritic() != "critic" || r.GetRounds() != 2 || r.GetCriteria() == "" {
		t.Fatalf("review = %v", r)
	}
	l := a.Limits
	if l.MaxDepth != 3 || l.MaxParallel != 2 || l.MaxTasks != 10 || l.MaxRuns != 20 || l.MaxTreeCost.GetMinorUnits() != 250 {
		t.Fatalf("limits = %v", l)
	}
}

func TestComplexAgentFieldErrors(t *testing.T) {
	cases := map[string]string{
		"bad output schema": "output: {type: 7}",
		"output not a map":  "output: [a]",
		"review no critic":  "review: {rounds: 2}",
		"review self":       "review: {critic: x}",
		"review rounds":     "review: {critic: c, rounds: 99}",
		"bad skill":         "skills: [\"Bad Skill\"]",
		"bad limit":         "limits: {max_parallel: -1}",
	}
	for name, extra := range cases {
		src := "---\nid: x\ndescription: d\nruntime: shell\n" + extra + "\n---\n"
		a, _ := Parse("agents/x.md", []byte(src))
		if len(a.Errors) == 0 || a.Enabled {
			t.Errorf("%s: expected an error, got %v", name, a)
		}
	}
}
