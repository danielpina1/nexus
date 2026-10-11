package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func cmdRun(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent id (default: route automatically)")
	follow := fs.Bool("follow", false, "stream the task's events until it finishes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(pos, " "))
	if prompt == "" {
		return errors.New(`usage: nexus run "<prompt>" [-agent id] [-follow]`)
	}
	resp, err := c.tasks.CreateTask(ctx, connect.NewRequest(&nexusv1.CreateTaskRequest{
		Prompt: prompt, AgentId: *agent, Source: "cli",
	}))
	if err != nil {
		return err
	}
	t := resp.Msg.GetTask()
	fmt.Fprintf(os.Stderr, "task %s %s (agent %s)\n", t.GetId(), stateName(t.GetState()), or(t.GetAgentId(), "auto"))
	if !*follow {
		fmt.Println(t.GetId())
		return nil
	}
	return followTask(ctx, c, t.GetId())
}

// followTask replays and streams one task's events (since 0 so nothing
// between CreateTask and Subscribe is missed) until it reaches a final state.
func followTask(ctx context.Context, c *client, id string) error {
	p := &runPrinter{}
	var final *nexusv1.Task
	err := stream(ctx, c, &nexusv1.SubscribeRequest{TaskIds: []string{id}}, func(ev *nexusv1.Event) error {
		p.print(ev)
		if t := ev.GetTaskUpdated().GetTask(); t != nil && terminal(t.GetState()) {
			final = t
			return stopStream
		}
		return nil
	})
	p.endLine()
	if err != nil {
		return err
	}
	if final != nil {
		fmt.Fprintf(os.Stderr, "\n%s %s", stateName(final.GetState()), final.GetId())
		if c := costString(final.GetCost()); c != "" {
			fmt.Fprintf(os.Stderr, " · %s", c)
		}
		fmt.Fprintln(os.Stderr)
		if final.GetState() == nexusv1.TaskState_TASK_STATE_FAILED {
			return fmt.Errorf("task failed: %s", or(final.GetSummary(), "no details"))
		}
	}
	return nil
}

// runPrinter renders a task's events for humans: agent text streams inline,
// everything else gets its own dimmed line on stderr.
type runPrinter struct {
	midLine   bool // stdout has text without a trailing newline
	lastState nexusv1.TaskState
}

func (p *runPrinter) endLine() {
	if p.midLine {
		fmt.Println()
		p.midLine = false
	}
}

func (p *runPrinter) note(format string, a ...any) {
	p.endLine()
	fmt.Fprintf(os.Stderr, "  "+format+"\n", a...)
}

func (p *runPrinter) print(ev *nexusv1.Event) {
	switch b := ev.GetBody().(type) {
	case *nexusv1.Event_RunOutput:
		text := b.RunOutput.GetText()
		if text == "" {
			return
		}
		fmt.Print(text)
		p.midLine = !strings.HasSuffix(text, "\n")
	case *nexusv1.Event_TaskUpdated:
		t := b.TaskUpdated.GetTask()
		if t.GetState() != p.lastState {
			p.lastState = t.GetState()
			if !terminal(t.GetState()) {
				p.note("· %s", stateName(t.GetState()))
			}
		}
	case *nexusv1.Event_Question:
		_, text := summarize(ev)
		p.note("? %s", text)
		p.note("  (answer from the app or dashboard)")
	case *nexusv1.Event_Approval:
		a := b.Approval
		if a.GetState() == nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
			p.note("! approval needed: %s", a.GetTitle())
			p.note("  nexus approve %s   |   nexus deny %s", a.GetId(), a.GetId())
		} else {
			p.note("! approval %s: %s", enumName(a.GetState().String(), "APPROVAL_STATE_"), a.GetTitle())
		}
	case *nexusv1.Event_UserMessage:
		// echo of our own prompt or a later message; not interesting here
	default:
		kind, text := summarize(ev)
		p.note("%s %s", kind, text)
	}
}
