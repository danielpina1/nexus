package vault

import (
	"context"
	"strings"
	"testing"
)

// The starter vault's teams, skills and new agents are valid and cross-checked.
func TestTemplateTeamsAndSkills(t *testing.T) {
	ctx := context.Background()
	root := copyTemplate(t)
	v := newVault(t, Options{Root: root})
	skills, err := NewSkillCatalog(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := NewCatalog(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	cat.Skills = skills
	if err := cat.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	teams, err := NewTeamCatalog(ctx, v, cat)
	if err != nil {
		t.Fatal(err)
	}

	sl, _ := skills.ListSkills(ctx, true)
	if len(sl) < 5 {
		t.Fatalf("skills = %d", len(sl))
	}
	for _, s := range sl {
		if len(s.Errors) > 0 {
			t.Errorf("skill %s: %v", s.Id, s.Errors)
		}
	}
	for _, id := range []string{"critic", "analyst", "judge", "architect", "reviewer", "tester", "report-writer"} {
		a, _, _, err := cat.GetAgent(ctx, id)
		if err != nil {
			t.Fatalf("agent %s: %v", id, err)
		}
		if len(a.Errors) > 0 || len(a.Warnings) > 0 {
			t.Errorf("agent %s: errors %v warnings %v", id, a.Errors, a.Warnings)
		}
	}
	tl, _ := teams.ListTeams(ctx, true)
	want := map[string]bool{"research-squad": true, "product-buyer": true, "dev-crew": true, "debate-panel": true}
	for _, tm := range tl {
		if len(tm.Errors) > 0 || !tm.Enabled {
			t.Errorf("team %s: %v", tm.Id, tm.Errors)
		}
		delete(want, tm.Id)
	}
	if len(want) > 0 {
		t.Fatalf("missing teams: %v", want)
	}
	// anthrex.* tools are for coding agents only: non-coding teams never list them.
	for _, id := range []string{"research-squad", "product-buyer", "debate-panel"} {
		tm, _, _, _ := teams.GetTeam(ctx, id)
		for _, m := range tm.Members {
			a, _, _, _ := cat.GetAgent(ctx, m.AgentId)
			for _, tool := range a.Tools {
				if strings.HasPrefix(tool, "anthrex") {
					t.Errorf("team %s member %s lists %s", id, m.AgentId, tool)
				}
			}
		}
		for _, tool := range tm.GetInlineOrchestrator().GetTools() {
			if strings.HasPrefix(tool, "anthrex") {
				t.Errorf("team %s orchestrator lists %s", id, tool)
			}
		}
	}

	// Cross references: an unknown member disables the team; validate_only reports it.
	bad := "---\ntype: team\nid: ghost-team\ndescription: d\norchestrator: research-lead\nmembers: [ghost]\n---\n"
	tm, err := teams.UpsertTeam(ctx, bad, "", true)
	if err != nil || tm.Enabled || !strings.Contains(strings.Join(tm.Errors, ";"), "unknown agent \"ghost\"") {
		t.Fatalf("validate_only = %v, %v", tm, err)
	}
	if _, err := teams.UpsertTeam(ctx, bad, "", false); err == nil {
		t.Fatal("invalid team written")
	}
	good := "---\ntype: team\nid: duo\ndescription: Two researchers.\nstrategy: parallel\nmembers: [researcher, fact-checker]\n---\nMission.\n"
	tm, err = teams.UpsertTeam(ctx, good, "", false)
	if err != nil || !tm.Enabled {
		t.Fatalf("upsert = %v, %v", tm, err)
	}
	if _, mission, _, err := teams.GetTeam(ctx, "duo"); err != nil || mission != "Mission." {
		t.Fatalf("get = %q, %v", mission, err)
	}

	// Unknown skills are warnings on the agent, not errors.
	writeFile(t, root, "agents/skilled.md", "---\nid: skilled\ndescription: d\nruntime: shell\nskills: [nope]\n---\n")
	if err := cat.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	a, _, _, _ := cat.GetAgent(ctx, "skilled")
	if !a.Enabled || len(a.Warnings) != 1 {
		t.Fatalf("skilled = %v", a)
	}
}

// Nested teams: a member "team:<id>" must name a known team, and a chain of
// nested teams must not lead back to the team (validate_only and reload).
func TestNestedTeamValidation(t *testing.T) {
	ctx := context.Background()
	root := copyTemplate(t)
	v := newVault(t, Options{Root: root})
	cat, err := NewCatalog(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := NewTeamCatalog(ctx, v, cat)
	if err != nil {
		t.Fatal(err)
	}
	outer := "---\ntype: team\nid: outer\ndescription: d\norchestrator: research-lead\nmembers:\n  - researcher\n  - team:debate-panel\n  - {team: research-squad, role: squad, when: big questions}\n---\n"
	tm, err := teams.UpsertTeam(ctx, outer, "", false)
	if err != nil || !tm.Enabled {
		t.Fatalf("outer = %v, %v", tm, err)
	}
	if tm.Members[1].AgentId != "team:debate-panel" || tm.Members[1].Role != "debate-panel" || tm.Members[2].AgentId != "team:research-squad" || tm.Members[2].Role != "squad" {
		t.Fatalf("members = %v", tm.Members)
	}

	unknown := "---\ntype: team\nid: lost\ndescription: d\norchestrator: research-lead\nmembers: [team:nowhere]\n---\n"
	tm, _ = teams.UpsertTeam(ctx, unknown, "", true)
	if tm.Enabled || !strings.Contains(strings.Join(tm.Errors, ";"), `unknown team "nowhere"`) {
		t.Fatalf("unknown team = %v", tm.Errors)
	}

	// research-squad → loop → outer → research-squad.
	loop := "---\ntype: team\nid: loop\ndescription: d\norchestrator: research-lead\nmembers: [team:outer]\n---\n"
	if tm, err := teams.UpsertTeam(ctx, loop, "", false); err != nil || !tm.Enabled {
		t.Fatalf("loop = %v, %v", tm, err)
	}
	squad, _, md, _ := teams.GetTeam(ctx, "research-squad")
	if squad == nil {
		t.Fatal("no research-squad")
	}
	cyc := strings.Replace(md, "members:\n", "members:\n  - team:loop\n", 1)
	if cyc == md {
		t.Fatalf("could not edit research-squad members:\n%s", md)
	}
	tm, _ = teams.UpsertTeam(ctx, cyc, "", true)
	if tm.Enabled || !strings.Contains(strings.Join(tm.Errors, ";"), "research-squad → loop → outer → research-squad") {
		t.Fatalf("cycle via validate_only = %v", tm.Errors)
	}
	if _, err := teams.UpsertTeam(ctx, cyc, "", false); err == nil {
		t.Fatal("cyclic team written")
	}
	// Written by hand (e.g. from Obsidian): the reload disables every team of the cycle.
	writeFile(t, root, "teams/research-squad.md", cyc)
	if err := teams.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"research-squad", "loop", "outer"} {
		tm, _, _, _ := teams.GetTeam(ctx, id)
		if tm.Enabled || !strings.Contains(strings.Join(tm.Errors, ";"), "cycle") {
			t.Errorf("%s after reload: enabled %v errors %v", id, tm.Enabled, tm.Errors)
		}
	}
	if tm, _, _, _ := teams.GetTeam(ctx, "debate-panel"); !tm.Enabled {
		t.Errorf("debate-panel disabled: %v", tm.Errors)
	}
}
