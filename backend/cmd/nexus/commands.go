package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) }

func cmdStatus(ctx context.Context, c *client, args []string) error {
	version, err := c.health(ctx)
	if err != nil {
		return fmt.Errorf("hub %s unreachable: %w", c.baseURL, err)
	}
	fmt.Printf("hub      %s (version %s)\n", c.baseURL, or(version, "?"))

	if resp, err := c.nodes.ListNodes(ctx, connect.NewRequest(&nexusv1.ListNodesRequest{})); err != nil {
		printUnavailable("nodes", err)
	} else {
		online := 0
		for _, n := range resp.Msg.GetNodes() {
			if n.GetStatus().GetOnline() {
				online++
			}
		}
		fmt.Printf("nodes    %d online / %d\n", online, len(resp.Msg.GetNodes()))
		for _, n := range resp.Msg.GetNodes() {
			state := "offline"
			if n.GetStatus().GetOnline() {
				state = "online"
			}
			fmt.Printf("         - %s (%s) %s, %d active runs\n", or(n.GetName(), n.GetId()), n.GetId(), state, n.GetStatus().GetActiveRuns())
		}
	}

	if resp, err := c.tasks.ListTasks(ctx, connect.NewRequest(&nexusv1.ListTasksRequest{Page: &nexusv1.PageRequest{PageSize: 200}})); err != nil {
		printUnavailable("tasks", err)
	} else {
		counts := map[string]int{}
		active := 0
		for _, t := range resp.Msg.GetTasks() {
			counts[stateName(t.GetState())]++
			if !terminal(t.GetState()) {
				active++
			}
		}
		var parts []string
		for _, s := range []nexusv1.TaskState{
			nexusv1.TaskState_TASK_STATE_RUNNING, nexusv1.TaskState_TASK_STATE_QUEUED,
			nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL, nexusv1.TaskState_TASK_STATE_WAITING_INPUT,
			nexusv1.TaskState_TASK_STATE_SUCCEEDED, nexusv1.TaskState_TASK_STATE_FAILED,
		} {
			if n := counts[stateName(s)]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, stateName(s)))
			}
		}
		fmt.Printf("tasks    %d active (recent: %s)\n", active, or(strings.Join(parts, ", "), "none"))
	}

	if resp, err := c.approvals.ListApprovals(ctx, connect.NewRequest(&nexusv1.ListApprovalsRequest{
		States: []nexusv1.ApprovalState{nexusv1.ApprovalState_APPROVAL_STATE_PENDING},
	})); err != nil {
		printUnavailable("inbox", err)
	} else {
		fmt.Printf("inbox    %d pending approvals\n", len(resp.Msg.GetApprovals()))
		for _, a := range resp.Msg.GetApprovals() {
			fmt.Printf("         - %s  %s\n", a.GetId(), a.GetTitle())
		}
	}
	return nil
}

// printUnavailable prints a status line for a failing service; services that
// are simply not enabled are reported as such, other errors verbatim.
func printUnavailable(label string, err error) {
	if notAvailable(err) {
		fmt.Printf("%-8s n/a (service not enabled)\n", label)
		return
	}
	fmt.Printf("%-8s error: %s\n", label, describe(err))
}

func cmdAgents(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	all := fs.Bool("all", false, "include disabled agents")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	resp, err := c.agents.ListAgents(ctx, connect.NewRequest(&nexusv1.ListAgentsRequest{IncludeDisabled: *all}))
	if err != nil {
		return err
	}
	w := table()
	fmt.Fprintln(w, "ID\tNAME\tRUNTIME\tMODEL\tTIER\tENABLED")
	for _, a := range resp.Msg.GetAgents() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", a.GetId(), a.GetName(), or(a.GetRuntime(), "-"),
			or(a.GetModel(), "-"), tierName(a.GetTier()), yesNo(a.GetEnabled()))
	}
	w.Flush()
	for _, a := range resp.Msg.GetAgents() {
		for _, e := range a.GetErrors() {
			fmt.Fprintf(os.Stderr, "warning: agent %s: %s\n", a.GetId(), e)
		}
	}
	return nil
}

func cmdTasks(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	n := fs.Int("n", 20, "number of tasks")
	state := fs.String("state", "", "filter by state (running, queued, succeeded…)")
	agent := fs.String("agent", "", "filter by agent id")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	req := &nexusv1.ListTasksRequest{Page: &nexusv1.PageRequest{PageSize: int32(*n)}, AgentId: *agent}
	if *state != "" {
		v, ok := nexusv1.TaskState_value["TASK_STATE_"+strings.ToUpper(strings.ReplaceAll(*state, "-", "_"))]
		if !ok {
			return fmt.Errorf("unknown state %q", *state)
		}
		req.States = []nexusv1.TaskState{nexusv1.TaskState(v)}
	}
	resp, err := c.tasks.ListTasks(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	if len(resp.Msg.GetTasks()) == 0 {
		fmt.Println("no tasks")
		return nil
	}
	w := table()
	fmt.Fprintln(w, "ID\tSTATE\tAGENT\tUPDATED\tTITLE")
	for _, t := range resp.Msg.GetTasks() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.GetId(), stateName(t.GetState()), or(t.GetAgentId(), "-"),
			ago(t.GetUpdatedAt()), truncate(or(t.GetTitle(), t.GetPrompt()), 60))
	}
	return w.Flush()
}

func cmdTask(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: nexus task <id>")
	}
	resp, err := c.tasks.GetTask(ctx, connect.NewRequest(&nexusv1.GetTaskRequest{Id: pos[0], IncludeChildren: true}))
	if err != nil {
		return err
	}
	t := resp.Msg.GetTask()
	w := table()
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%s\t%s\n", k, v)
		}
	}
	row("id", t.GetId())
	row("title", t.GetTitle())
	row("state", stateName(t.GetState()))
	row("agent", t.GetAgentId())
	row("workflow", t.GetWorkflowId())
	row("tier", tierName(t.GetTier()))
	row("node", t.GetNodeId())
	row("run", t.GetCurrentRunId())
	row("parent", t.GetParentId())
	row("source", t.GetSource())
	row("created", ago(t.GetCreatedAt()))
	row("updated", ago(t.GetUpdatedAt()))
	row("cost", costString(t.GetCost()))
	if t.GetEpisode() != nil {
		row("episode", t.GetEpisode().GetPath())
	}
	w.Flush()
	fmt.Printf("\nprompt:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(t.GetPrompt()), "\n", "\n  "))
	if s := strings.TrimSpace(t.GetSummary()); s != "" {
		fmt.Printf("\nsummary:\n  %s\n", strings.ReplaceAll(s, "\n", "\n  "))
	}
	if kids := resp.Msg.GetChildren(); len(kids) > 0 {
		fmt.Println("\nsubtasks:")
		w = table()
		for _, k := range kids {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", k.GetId(), stateName(k.GetState()), or(k.GetAgentId(), "-"), truncate(or(k.GetTitle(), k.GetPrompt()), 60))
		}
		w.Flush()
	}
	return nil
}

func cmdDecide(ctx context.Context, c *client, args []string, approve bool) error {
	verb := map[bool]string{true: "approve", false: "deny"}[approve]
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	comment := fs.String("comment", "", "comment for the agent")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: nexus %s <approval-id> [-comment text]", verb)
	}
	// The CLI has no device key: decisions are unsigned and only accepted for
	// trust levels that do not require a biometric signature.
	resp, err := c.approvals.Decide(ctx, connect.NewRequest(&nexusv1.DecideRequest{
		Decision: &nexusv1.SignedApproval{ApprovalId: pos[0], Approve: approve},
		Comment:  *comment,
	}))
	if err != nil {
		return err
	}
	a := resp.Msg.GetApproval()
	fmt.Printf("%s: %s (%s)\n", a.GetId(), enumName(a.GetState().String(), "APPROVAL_STATE_"), a.GetTitle())
	return nil
}

func cmdStopAll(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("stop-all", flag.ExitOnError)
	pause := fs.Bool("pause-triggers", false, "also pause schedules and other triggers")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	resp, err := c.tasks.StopAll(ctx, connect.NewRequest(&nexusv1.StopAllRequest{PauseTriggers: *pause}))
	if err != nil {
		return err
	}
	fmt.Printf("cancelled %d task(s)\n", resp.Msg.GetCancelled())
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
