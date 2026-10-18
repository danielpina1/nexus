package router

import (
	"strings"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

func TestRoutesToTeams(t *testing.T) {
	f := newFixture(t, true)
	tm := taskstest.Team("research-squad", "lead", nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL)
	tm.Name, tm.Description = "Research squad", "Researchers and a fact checker for a deep dive report"
	f.env.Host.Provide(kernel.SvcTeams, taskstest.NewTeams(tm))

	// The LLM sees the team in the catalog and may pick it.
	f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
		if !strings.Contains(req.Messages[0].Content, "team:research-squad") {
			t.Errorf("catalog lacks the team: %s", req.Messages[0].Content)
		}
		return taskstest.Reply(`{"kind":"task","agent":"team:research-squad","confidence":0.9}`), nil
	}
	r := f.route(t, "I need a full report on heat pumps", nil)
	if r.TeamId != "research-squad" || r.AgentId != "" || r.Kind != nexusv1.CommandKind_COMMAND_KIND_TASK {
		t.Fatalf("route = %v", r)
	}
}
