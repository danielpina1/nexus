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
