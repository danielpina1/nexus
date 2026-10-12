package tasks

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// mdCatalog returns raw agent markdown alongside the parsed agent.
type mdCatalog struct {
	*taskstest.Catalog
	md map[string]string
}

func (c *mdCatalog) GetAgent(ctx context.Context, id string) (*nexusv1.Agent, string, string, error) {
	a, instr, _, err := c.Catalog.GetAgent(ctx, id)
	return a, instr, c.md[id], err
}

const shopNote = `---
type: agent
id: shopper
runtime: browser
browser:
  allowed_domains: [kuantokusta.pt]
  profile: shopper
  restrict_domains: true
shops: [worten, fnac]
shopping: { max_stores: 3, research_only: false }
limits: { max_runtime: 30m }
secret_stuff: nope
---
# Shopper
`

func TestAgentConfigForwarded(t *testing.T) {
	cat := &mdCatalog{Catalog: taskstest.NewCatalog(), md: map[string]string{"shopper": shopNote}}
	cat.Add(&nexusv1.Agent{Id: "shopper", Runtime: "browser", Enabled: true, Tier: nexusv1.Tier_TIER_MEDIUM}, "# Shopper")
	cat.Add(&nexusv1.Agent{Id: "plain", Runtime: "echo", Enabled: true, Tier: nexusv1.Tier_TIER_SIMPLE}, "# Plain")
	env := taskstest.New(t)
	disp := &taskstest.Dispatcher{}
	env.Host.Provide(kernel.SvcDispatcher, disp)
	env.Host.Provide(kernel.SvcAgents, cat)
	m := &Module{}
	if err := m.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}

	// A task param named like a forwarded key is replaced by the agent's value;
	// other params are kept.
	params, _ := structpb.NewStruct(map[string]any{
		"item":    "auscultadores",
		"browser": map[string]any{"allowed_domains": []any{"evil.example"}},
	})
	task, err := m.Engine.CreateTask(context.Background(), &nexusv1.CreateTaskRequest{Prompt: "compra auscultadores", AgentId: "shopper", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != stAssigned {
		t.Fatalf("state %v: %s", task.State, task.Summary)
	}
	c := disp.LastAssignment().Context.AsMap()
	b, _ := c["browser"].(map[string]any)
	doms, _ := b["allowed_domains"].([]any)
	if len(doms) != 1 || doms[0] != "kuantokusta.pt" || b["profile"] != "shopper" || b["restrict_domains"] != true {
		t.Fatalf("browser = %v", c["browser"])
	}
	if s, _ := c["shops"].([]any); len(s) != 2 || s[0] != "worten" {
		t.Fatalf("shops = %v", c["shops"])
	}
	if s, _ := c["shopping"].(map[string]any); s["max_stores"] != float64(3) || s["research_only"] != false {
		t.Fatalf("shopping = %v", c["shopping"])
	}
	if c["item"] != "auscultadores" || c["task_id"] != task.Id {
		t.Fatalf("params/metadata lost: %v", c)
	}
	for _, k := range []string{"limits", "secret_stuff", "type", "runtime"} {
		if _, ok := c[k]; ok {
			t.Errorf("%s forwarded", k)
		}
	}

	// An agent without markdown (or without the keys) is unchanged.
	task2, err := m.Engine.CreateTask(context.Background(), &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "plain", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	c2 := disp.LastAssignment().Context.AsMap()
	if b2, _ := c2["browser"].(map[string]any); b2["allowed_domains"].([]any)[0] != "evil.example" || c2["task_id"] != task2.Id {
		t.Fatalf("plain context = %v", c2)
	}
}

func TestMergeAgentConfig(t *testing.T) {
	ctx, _ := structpb.NewStruct(map[string]any{"a": 1})
	if err := mergeAgentConfig(ctx, "no frontmatter"); err != nil || len(ctx.Fields) != 1 {
		t.Fatalf("plain markdown: %v %v", err, ctx)
	}
	if err := mergeAgentConfig(ctx, "---\nbrowser: [unclosed\n---\n"); err == nil {
		t.Fatal("invalid yaml accepted")
	}
	if err := mergeAgentConfig(ctx, "---\nshops:\n  - id: x\n    domains: [x.pt]\n    added: 2024-01-02\n---\n"); err != nil {
		t.Fatal(err)
	}
	s := ctx.AsMap()["shops"].([]any)[0].(map[string]any)
	if s["id"] != "x" || s["added"] != "2024-01-02" {
		t.Fatalf("shops = %v", s)
	}
}
