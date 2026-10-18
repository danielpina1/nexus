package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func cmdTeams(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("teams", flag.ExitOnError)
	all := fs.Bool("all", false, "include disabled teams")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	resp, err := c.teams.ListTeams(ctx, connect.NewRequest(&nexusv1.ListTeamsRequest{IncludeDisabled: *all}))
	if err != nil {
		return err
	}
	for _, t := range resp.Msg.GetTeams() {
		var members []string
		for _, m := range t.GetMembers() {
			s := m.GetRole()
			if m.GetCount() > 1 {
				s += fmt.Sprintf("×%d", m.GetCount())
			}
			members = append(members, s)
		}
		orch := or(t.GetOrchestratorAgentId(), "inline")
		if t.GetOrchestratorAgentId() == "" && t.GetInlineOrchestrator() == nil {
			orch = "none"
		}
		fmt.Printf("%-18s %-13s orchestrator %-14s members %s\n", t.GetId(),
			enumName(t.GetStrategy().String(), "TEAM_STRATEGY_"), orch, strings.Join(members, ", "))
		for _, e := range t.GetErrors() {
			fmt.Fprintf(os.Stderr, "  error: %s\n", e)
		}
		for _, w := range t.GetWarnings() {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
	}
	return nil
}

func cmdTeamRun(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("team-run", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw TeamRun as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: nexus team-run <root-task-id> [-json]")
	}
	resp, err := c.teams.GetTeamRun(ctx, connect.NewRequest(&nexusv1.GetTeamRunRequest{RootTaskId: pos[0]}))
	if err != nil {
		return err
	}
	run := resp.Msg.GetRun()
	if *asJSON {
		b, _ := json.MarshalIndent(run, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	root := run.GetRoot()
	fmt.Printf("%s  %s  team %s  tasks %d  runs %d  active %d  queued %d",
		root.GetId(), stateName(root.GetState()), or(root.GetTeamId(), "-"), run.GetTotalTasks(), run.GetTotalRuns(), run.GetActiveTasks(), run.GetQueuedTasks())
	if s := costString(run.GetTotalCost()); s != "" {
		fmt.Printf("  cost %s", s)
	}
	fmt.Println()
	if r := run.GetStoppedReason(); r != "" {
		fmt.Printf("stopped: %s\n", r)
	}
	for _, rt := range run.GetTasks() {
		t := rt.GetTask()
		state := stateName(t.GetState())
		if t.GetPhase() != "" {
			state += "/" + t.GetPhase()
		}
		fmt.Printf("%s%-14s %-18s %-22s %s\n", strings.Repeat("  ", int(t.GetDepth())), or(t.GetRole(), "-"), state, t.GetAgentId(),
			truncate(or(t.GetSummary(), t.GetTitle()), 70))
	}
	if bb := run.GetBlackboard(); len(bb) > 0 {
		fmt.Println("\nblackboard:")
		for _, e := range bb {
			b, _ := json.Marshal(e.GetValue().AsInterface())
			fmt.Printf("  %s (v%d, %s): %s\n", e.GetKey(), e.GetVersion(), e.GetAuthorRole(), truncate(string(b), 100))
		}
	}
	if ms := run.GetMessages(); len(ms) > 0 {
		fmt.Println("\nmessages:")
		for _, m := range ms {
			fmt.Printf("  %s → %s: %s\n", m.GetFromRole(), m.GetTo(), truncate(m.GetText(), 100))
		}
	}
	return nil
}
