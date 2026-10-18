package router

import (
	"context"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/router/learn"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// The learning stage (router.learn) is consulted for every input, logs its
// stage scores beside the decision and adds a runtime hint.
func TestLearningStageWiring(t *testing.T) {
	env := taskstest.New(t)
	env.Host.ConfigYAML = testConfig + "\nembed_model: hash\n"
	auto := func(a *nexusv1.Agent) *nexusv1.Agent { a.Runtime = "auto"; return a }
	env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(
		auto(taskstest.Agent("grocery", "Grocery (Continente)", "Buys groceries from a shopping list", nexusv1.Tier_TIER_MEDIUM)),
		auto(taskstest.Agent("coder", "Code fixer", "Fixes failing tests and bugs in repositories", nexusv1.Tier_TIER_HEAVY)),
		taskstest.Agent("travel", "Travel planner", "Plans trips, flights and hotels", nexusv1.Tier_TIER_MEDIUM),
	))
	llm := &taskstest.LLM{Fn: func(kernel.ChatRequest) (*kernel.ChatResponse, error) {
		return taskstest.Reply(`{"kind":"task","agent":"travel","confidence":0.9,"reason":"trip"}`), nil
	}}
	env.Host.Provide(kernel.SvcLLM, llm)
	rm := &Module{}
	if err := rm.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	lm := &learn.Module{}
	if err := lm.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Unsure → the LLM stage is blended in.
	r, err := rm.Router.Route(ctx, "a weekend somewhere warm", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.AgentId != "travel" || len(llm.Calls) != 1 {
		t.Fatalf("route %q (%s), LLM calls %d", r.AgentId, r.Reason, len(llm.Calls))
	}
	id := r.GetParams().GetFields()["decision_id"].GetStringValue()
	var stage, chosen string
	if err := env.Store.DB().QueryRow(`SELECT d.stage, s.chosen FROM router_decisions d JOIN router_learn_scores s ON s.decision_id = d.id WHERE d.id = ?`, id).
		Scan(&stage, &chosen); err != nil {
		t.Fatal(err)
	}
	if stage != "learned+llm" || chosen != "travel" {
		t.Fatalf("logged stage %q chosen %q", stage, chosen)
	}

	// A strong rule keeps its route and gets a runtime hint for runtime: auto.
	r, err = rm.Router.Route(ctx, "do the groceries", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.AgentId != "grocery" || r.GetParams().GetFields()[learn.ParamRuntimeHint].GetStringValue() == "" {
		t.Fatalf("rule route %q params %v", r.AgentId, r.GetParams())
	}
	if len(llm.Calls) != 1 {
		t.Fatalf("strong rule must not call the LLM")
	}
}
