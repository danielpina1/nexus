package shops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

const templateRoot = "../../../vault-template"

// fakeVault implements kernel.Vault's Root (the only method shops uses).
type fakeVault struct {
	kernel.Vault
	root string
}

func (v fakeVault) Root() string { return v.root }

func write(t *testing.T, root, p, s string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newModule(t *testing.T, root string) (*Module, *kerneltest.Host) {
	t.Helper()
	h := kerneltest.New(t)
	h.Provide(kernel.SvcVault, kernel.Vault(fakeVault{root: root}))
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return m, h
}

func callTool(t *testing.T, h *kerneltest.Host, name string, in any) (map[string]any, error) {
	t.Helper()
	tool, ok := h.Tools().Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	if tool.Spec().ActionClass != nexusv1.ActionClass_ACTION_CLASS_READ {
		t.Errorf("%s class %v", name, tool.Spec().ActionClass)
	}
	b, _ := json.Marshal(in)
	res, err := tool.Invoke(context.Background(), kernel.ToolCall{Input: b})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func ids(out map[string]any) string {
	var s []string
	for _, x := range out["shops"].([]any) {
		s = append(s, x.(map[string]any)["id"].(string))
	}
	return strings.Join(s, ",")
}

func TestListGet(t *testing.T) {
	root := t.TempDir()
	write(t, root, "shops/worten.md", "---\ntype: shop\nid: worten\nname: Worten\ndomains: [worten.pt]\ncategories: [electronics, appliances]\nsearch_url: \"https://www.worten.pt/search?query={q}\"\nlogin_required: true\ncheckout_labels: [\"Confirmar e pagar\"]\npriority: 10\n---\n# Worten\nPlaybook here.\n")
	write(t, root, "shops/fnac.md", "---\ntype: shop\nname: Fnac\ndomains: [\"https://www.fnac.pt/\"]\ncategories: [books, Electrónica]\npriority: 11\n---\n# Fnac\n")
	write(t, root, "shops/kk.md", "---\ntype: shop\nid: kuantokusta\ndomains: [kuantokusta.pt]\ncategories: [price-comparison, electronics]\nresearch_only: true\npriority: 5\n---\n# KuantoKusta\n")
	write(t, root, "shops/off.md", "---\ntype: shop\nid: off\ndomains: [off.pt]\nenabled: false\n---\n# Off\n")
	write(t, root, "shops/bad.md", "---\ntype: shop\nid: bad\nsearch_url: https://elsewhere.com/?q={q}\n---\n# Bad\n")
	write(t, root, "shops/readme.md", "---\ntype: topic\n---\n# Not a shop\n")
	_, h := newModule(t, root)

	out, err := callTool(t, h, "shops.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(out); got != "kuantokusta,worten,fnac" {
		t.Fatalf("list = %s", got)
	}
	out, _ = callTool(t, h, "shops.list", map[string]any{"category": "electronics", "buyable": true})
	if got := ids(out); got != "worten" {
		t.Fatalf("electronics buyable = %s", got)
	}
	out, _ = callTool(t, h, "shops.list", map[string]any{"category": "electronica"}) // accent-insensitive
	if got := ids(out); got != "fnac" {
		t.Fatalf("electronica = %s", got)
	}
	// Workflow-rendered strings: ids as CSV, limit/buyable as strings.
	out, _ = callTool(t, h, "shops.list", map[string]any{"ids": "fnac, worten", "limit": "1", "buyable": "true", "category": "", "query": "fone bluetooth"})
	if got := ids(out); got != "fnac" {
		t.Fatalf("ids = %s", got)
	}
	out, _ = callTool(t, h, "shops.list", map[string]any{"ids": []string{"worten"}, "query": "fone bluetooth"})
	if l := out["shops"].([]any)[0].(map[string]any)["search_link"]; l != "https://www.worten.pt/search?query=fone+bluetooth" {
		t.Fatalf("search_link = %v", l)
	}

	for _, key := range []string{"worten", "Worten", "www.worten.pt", "https://www.worten.pt/x"} {
		g, err := callTool(t, h, "shops.get", map[string]any{"id": key, "query": "tv"})
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if g["id"] != "worten" || g["playbook"] != "# Worten\nPlaybook here." || g["search_link"] != "https://www.worten.pt/search?query=tv" ||
			g["login_required"] != true || g["checkout_labels"].([]any)[0] != "Confirmar e pagar" || g["domains"].([]any)[0] != "worten.pt" {
			t.Fatalf("get %s = %v", key, g)
		}
	}
	if g, _ := callTool(t, h, "shops.get", map[string]any{"id": "fnac"}); g["domains"].([]any)[0] != "www.fnac.pt" {
		t.Fatalf("fnac = %v", g)
	}
	if _, err := callTool(t, h, "shops.get", map[string]any{"id": "nope"}); err == nil || !strings.Contains(err.Error(), "known: kuantokusta, worten, fnac") {
		t.Fatalf("unknown err = %v", err)
	}
	if _, err := callTool(t, h, "shops.get", map[string]any{"id": "off"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled err = %v", err)
	}
	if _, err := callTool(t, h, "shops.get", map[string]any{"id": "bad"}); err == nil || !strings.Contains(err.Error(), "missing domains") || !strings.Contains(err.Error(), "search_url") {
		t.Fatalf("bad err = %v", err)
	}
}

func TestNoVault(t *testing.T) {
	h := kerneltest.New(t)
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, h, "shops.list", map[string]any{}); err == nil {
		t.Fatal("expected an error without a vault")
	}
	// Missing folder = empty list.
	_, h2 := newModule(t, t.TempDir())
	out, err := callTool(t, h2, "shops.list", map[string]any{})
	if err != nil || out["count"] != float64(0) {
		t.Fatalf("empty = %v %v", out, err)
	}
	if info := m.Info(); info.Name != "shops" || info.Side != kernel.SideHub || !info.DefaultEnabled {
		t.Fatalf("info = %+v", info)
	}
}

// TestTemplateShops parses every vault-template/shops note.
func TestTemplateShops(t *testing.T) {
	all, err := Load(filepath.Join(templateRoot, "shops"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"continente", "pingo-doce", "auchan", "worten", "fnac", "radio-popular", "pcdiga",
		"amazon-es", "el-corte-ingles", "ikea", "decathlon", "kuantokusta"}
	byID := map[string]*Shop{}
	for _, s := range all {
		byID[s.ID] = s
		if !s.Valid() {
			t.Errorf("%s: %v", s.Path, s.Errors)
		}
		if s.Name == "" || len(s.Categories) == 0 || s.Playbook == "" || s.Currency != "EUR" {
			t.Errorf("%s incomplete: %+v", s.ID, s)
		}
		if s.SearchURL != "" && !strings.HasPrefix(s.SearchLink("x y"), "https://") {
			t.Errorf("%s search link %q", s.ID, s.SearchLink("x y"))
		}
	}
	for _, id := range want {
		if byID[id] == nil {
			t.Errorf("missing shop %s", id)
		}
	}
	if len(all) != len(want) {
		t.Errorf("shops = %d, want %d", len(all), len(want))
	}
	if kk := byID["kuantokusta"]; kk == nil || !kk.ResearchOnly {
		t.Error("kuantokusta must be research_only")
	}
	if got := Select(all, Filter{Category: "groceries", Buyable: true, Limit: 3}); len(got) != 3 || got[0].ID != "continente" {
		t.Errorf("groceries = %v", got)
	}
}

// TestTemplateAgents parses the shopping agent notes with agentdef.
func TestTemplateAgents(t *testing.T) {
	for _, name := range []string{"shop.md", "grocery-continente.md"} {
		b, err := os.ReadFile(filepath.Join(templateRoot, "agents", name))
		if err != nil {
			t.Fatal(err)
		}
		a, instr := agentdef.Parse("agents/"+name, b)
		if len(a.Errors) > 0 || !a.Enabled || a.Runtime != "auto" || instr == "" {
			t.Fatalf("%s: %v %+v", name, a.Errors, a)
		}
		if a.Policy["spend"] != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			t.Errorf("%s spend policy %v", name, a.Policy)
		}
		if name == "shop.md" {
			tools := strings.Join(a.Tools, ",")
			for _, want := range []string{"browser", "shops.list", "shops.get", "ask_user", "request_approval", "vault.write", "notify"} {
				if !strings.Contains(tools, want) {
					t.Errorf("shop tools missing %s: %s", want, tools)
				}
			}
			// The vault test searches "continente" among agents and expects
			// only grocery-continente.
			if strings.Contains(strings.ToLower(string(b)), "continente") {
				t.Error("shop.md must not mention the grocery store by name")
			}
		}
	}
}

// TestTemplateWorkflows parses the shopping workflows with the workflow parser.
func TestTemplateWorkflows(t *testing.T) {
	for _, name := range []string{"shop.md", "price-compare.md", "groceries.md"} {
		b, err := os.ReadFile(filepath.Join(templateRoot, "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		w := workflow.Parse("workflows/"+name, b)
		if !w.Valid() {
			t.Fatalf("%s: %v", name, w.Errors)
		}
		switch w.ID {
		case "shop":
			if w.Triggers[0].Kind != workflow.TriggerIntent || len(w.Inputs) != 4 || w.Steps[0].Agent != "shop" {
				t.Errorf("shop = %+v", w)
			}
		case "price-compare":
			if got := strings.Join(w.StepIDs(), ","); !strings.Contains(got, "stores") || !strings.Contains(got, "buy") {
				t.Errorf("price-compare steps %s", got)
			}
			if w.Steps[0].Tool != "shops.list" || w.Steps[1].Kind != workflow.KindParallel || w.Steps[1].Do[0].Kind != workflow.KindForeach ||
				w.Steps[1].Do[0].Concurrency != 4 || w.Steps[len(w.Steps)-1].If == "" {
				t.Errorf("price-compare = %+v", w.Steps)
			}
		}
	}
}
