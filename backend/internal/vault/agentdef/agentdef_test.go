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
		Name:        "Grocery (Continente)",
		Description: `Buys groceries on continente.pt from a list and guidelines. Use for shopping lists, supermarket orders, "buy the usual".`,
		Icon:        "cart",
		Runtime:     "browser",
		Model:       "smart",
		Tier:        nexusv1.Tier_TIER_MEDIUM,
		NodeTags:    []string{"browser", "always-on"},
		Tools:       []string{"browser", "memory.search", "vault.write", "ask_user", "request_approval"},
		Limits: &nexusv1.AgentLimits{
			MaxRuntimeSeconds: 1800,
			MaxCost:           &nexusv1.Money{Currency: "EUR", MinorUnits: 50},
			MaxSpend:          &nexusv1.Money{Currency: "EUR", MinorUnits: 15000},
		},
		Policy:  map[string]nexusv1.TrustLevel{"spend": nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC},
		Note:    &nexusv1.NoteRef{Id: "grocery-continente", Path: "agents/grocery-continente.md", Title: "Grocery (Continente)"},
		Version: 1,
		Enabled: true,
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
