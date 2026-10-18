package router

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/router/learn"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// downLLM models a provider that is cooling down after failures: every call
// hangs until its context ends, and Available reports false.
type downLLM struct {
	down   atomic.Bool
	chats  atomic.Int32
	embeds atomic.Int32
}

func (l *downLLM) Available(string) bool { return !l.down.Load() }

func (l *downLLM) Chat(ctx context.Context, _ kernel.ChatRequest) (*kernel.ChatResponse, error) {
	l.chats.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (l *downLLM) ChatStream(ctx context.Context, req kernel.ChatRequest, _ func(string)) (*kernel.ChatResponse, error) {
	return l.Chat(ctx, req)
}

func (l *downLLM) Embed(ctx context.Context, _ string, _ []string) ([][]float32, error) {
	l.embeds.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func timedRoute(t *testing.T, r *Router, input string) (*nexusv1.Route, time.Duration) {
	t.Helper()
	start := time.Now()
	route, err := r.Route(context.Background(), input, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return route, time.Since(start)
}

// With the provider down the LLM stage is skipped at once: the command bar
// gets the rules result instead of waiting llm_timeout (30s) per model.
func TestRouterSkipsDegradedLLM(t *testing.T) {
	f := newFixture(t, false)
	llm := &downLLM{}
	llm.down.Store(true)
	f.env.Host.Provide(kernel.SvcLLM, llm)

	route, took := timedRoute(t, f.r, "a weekend somewhere warm") // unsure: would ask the LLM
	if took > 2*time.Second {
		t.Fatalf("Route waited %v for a degraded provider", took)
	}
	if n := llm.chats.Load(); n != 0 {
		t.Fatalf("LLM called %d times while degraded", n)
	}
	if route.Kind == nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
		t.Fatalf("no route: %+v", route)
	}
	// A rule still wins as before.
	if r, _ := timedRoute(t, f.r, "do the groceries"); r.AgentId != "grocery" {
		t.Fatalf("rule route = %q", r.AgentId)
	}
	var stage string
	if err := f.env.Store.DB().QueryRow(`SELECT stage FROM router_decisions ORDER BY rowid LIMIT 1`).Scan(&stage); err != nil || stage == "llm" {
		t.Fatalf("logged stage %q (%v)", stage, err)
	}
}

// Only the degraded model is skipped: the fallback model is still asked.
func TestRouterAsksFallbackWhenPrimaryDegraded(t *testing.T) {
	f := newFixture(t, true)
	f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
		return taskstest.Reply(`{"kind":"task","agent":"travel","confidence":0.9,"reason":"trip"}`), nil
	}
	f.env.Host.Provide(kernel.SvcLLM, &perModelLLM{LLM: f.llm, down: map[string]bool{"fast-local": true}})
	route, _ := timedRoute(t, f.r, "a weekend somewhere warm")
	if route.AgentId != "travel" || len(f.llm.Calls) != 1 || f.llm.Calls[0].Model != "smart" {
		t.Fatalf("route %q, calls %+v", route.AgentId, f.llm.Calls)
	}
}

type perModelLLM struct {
	*taskstest.LLM
	down map[string]bool
}

func (l *perModelLLM) Available(model string) bool { return !l.down[model] }

// The learning router does the same for its embedding stage (it switches
// to the built-in hashing embedder without calling the provider) and for its
// LLM stage.
func TestLearningRouterSkipsDegradedProvider(t *testing.T) {
	env := taskstest.New(t)
	env.Host.ConfigYAML = testConfig // embed_model defaults to the "embed" alias
	env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(
		taskstest.Agent("grocery", "Grocery (Continente)", "Buys groceries from a shopping list", nexusv1.Tier_TIER_MEDIUM),
		taskstest.Agent("coder", "Code fixer", "Fixes failing tests and bugs in repositories", nexusv1.Tier_TIER_HEAVY),
		taskstest.Agent("travel", "Travel planner", "Plans trips, flights and hotels", nexusv1.Tier_TIER_MEDIUM),
	))
	llm := &downLLM{}
	llm.down.Store(true)
	env.Host.Provide(kernel.SvcLLM, llm)
	rm := &Module{}
	if err := rm.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	lm := &learn.Module{}
	if err := lm.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	route, took := timedRoute(t, rm.Router, "a weekend somewhere warm")
	if took > 2*time.Second {
		t.Fatalf("Route waited %v for a degraded provider", took)
	}
	if llm.chats.Load() != 0 || llm.embeds.Load() != 0 {
		t.Fatalf("provider called while degraded: %d chats, %d embeds", llm.chats.Load(), llm.embeds.Load())
	}
	if route.Kind == nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
		t.Fatalf("no route: %+v", route)
	}
}
