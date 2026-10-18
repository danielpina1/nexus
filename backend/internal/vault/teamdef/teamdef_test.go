package teamdef

import (
	"strings"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestParseTeam(t *testing.T) {
	src := `---
type: team
id: squad
name: Squad
description: Does research.
orchestrator: lead
strategy: hierarchical
members:
  - {agent: researcher, count: 3, when: "one sub-question each"}
  - fact-checker
shared: {blackboard: false}
limits: {max_parallel: 2, max_tasks: 9, max_depth: 2, max_cost_eur: 1.5, timeout: 30m, max_runs: 20}
approvals: {bundle: true}
output: {type: object, properties: {report: {type: string}}}
triggers: [{phrase: ["deep dive", "squad"]}]
---
Mission body`
	tm, mission := Parse("teams/squad.md", []byte(src))
	if len(tm.Errors) > 0 || !tm.Enabled {
		t.Fatalf("errors: %v", tm.Errors)
	}
	if mission != "Mission body" {
		t.Fatalf("mission = %q", mission)
	}
	if tm.OrchestratorAgentId != "lead" || tm.Strategy != nexusv1.TeamStrategy_TEAM_STRATEGY_HIERARCHICAL {
		t.Fatalf("team = %v", tm)
	}
	if len(tm.Members) != 2 || tm.Members[0].Role != "researcher" || tm.Members[0].Count != 3 || tm.Members[1].Role != "fact-checker" || tm.Members[1].Count != 1 {
		t.Fatalf("members = %v", tm.Members)
	}
	l := tm.Limits
	if l.MaxParallel != 2 || l.MaxTasks != 9 || l.MaxDepth != 2 || l.MaxCost.GetMinorUnits() != 150 || l.TimeoutSeconds != 1800 || l.MaxRuns != 20 {
		t.Fatalf("limits = %v", l)
	}
	if tm.Blackboard || !tm.BundleApprovals || tm.OutputSchema == nil {
		t.Fatalf("flags = %v", tm)
	}
	if len(tm.Triggers) != 2 {
		t.Fatalf("triggers = %v", tm.Triggers)
	}
}

func TestParseInlineOrchestratorAndDebate(t *testing.T) {
	src := `---
id: panel
description: d
orchestrator: {instructions: "Lead well.", runtime: claude-code, tools: [memory.search], tier: simple}
strategy: debate
debate: {rounds: 3}
members: [{agent: analyst, role: pro}, {agent: analyst, role: con}, {agent: judge, role: judge}]
---`
	tm, _ := Parse("teams/panel.md", []byte(src))
	if len(tm.Errors) > 0 {
		t.Fatalf("errors: %v", tm.Errors)
	}
	io := tm.InlineOrchestrator
	if io == nil || io.Instructions != "Lead well." || io.Runtime != "claude-code" || io.Tier != nexusv1.Tier_TIER_SIMPLE || len(io.Tools) != 1 {
		t.Fatalf("inline orchestrator = %v", io)
	}
	if tm.DebateRounds != 3 || !tm.Blackboard {
		t.Fatalf("team = %v", tm)
	}
}

func TestParseTeamErrors(t *testing.T) {
	cases := map[string]string{
		"no orchestrator":   "strategy: hierarchical\nmembers: [a]",
		"bad strategy":      "orchestrator: x\nstrategy: chaos\nmembers: [a]",
		"duplicate role":    "orchestrator: x\nmembers: [a, a]",
		"reserved role":     "orchestrator: x\nmembers: [{agent: a, role: orchestrator}]",
		"bad count":         "orchestrator: x\nmembers: [{agent: a, count: 99}]",
		"pipeline no mem":   "strategy: pipeline",
		"debate one":        "strategy: debate\nmembers: [a]",
		"bad limits":        "orchestrator: x\nmembers: [a]\nlimits: {max_parallel: -2}",
		"bad output":        "orchestrator: x\nmembers: [a]\noutput: {type: 3}",
		"orchestrator list": "orchestrator: [a]\nmembers: [a]",
	}
	for name, extra := range cases {
		src := "---\nid: t\ndescription: d\n" + extra + "\n---\n"
		tm, _ := Parse("teams/t.md", []byte(src))
		if len(tm.Errors) == 0 || tm.Enabled {
			t.Errorf("%s: expected an error, got %v", name, tm)
		}
	}
	// Parallel teams may have no orchestrator.
	tm, _ := Parse("teams/p.md", []byte("---\nid: p\ndescription: d\nstrategy: parallel\nmembers: [a, b]\n---\n"))
	if len(tm.Errors) > 0 {
		t.Fatalf("parallel without orchestrator: %v", tm.Errors)
	}
}

func TestParseSkill(t *testing.T) {
	s := ParseSkill("skills/web.md", []byte("---\nid: web\nname: Web\ndescription: Search.\ntools: [web.search]\n---\nDo it well.\n"))
	if len(s.Errors) > 0 || !s.Enabled || s.Instructions != "Do it well." || len(s.Tools) != 1 {
		t.Fatalf("skill = %v", s)
	}
	bad := ParseSkill("skills/x.md", []byte("---\nid: x\n---\n"))
	if len(bad.Errors) < 2 || bad.Enabled {
		t.Fatalf("expected errors, got %v", bad)
	}
	if !strings.Contains(strings.Join(bad.Errors, ";"), "description") {
		t.Fatalf("errors = %v", bad.Errors)
	}
}
