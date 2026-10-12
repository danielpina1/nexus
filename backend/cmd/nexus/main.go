// Command nexus is the Nexus command-line client. It talks to the hub's
// Connect API over HTTP.
//
//	nexus [-url URL] [-token TOKEN] <command> [args]
//
// NEXUS_URL (default http://localhost:7700) and NEXUS_TOKEN set the defaults.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `nexus — Nexus command-line client

Usage:
  nexus [-url URL] [-token TOKEN] [-ca FILE] <command> [args]

Commands:
  status                          hub health, nodes, tasks and pending approvals
  agents [-all]                   list agents
  tasks [-n 20] [-state running]  list tasks
  task <id>                       show a task and its children
  run "<prompt>" [-agent id] [-follow]
                                  create a task (and stream its events)
  approve <approval-id> [-comment text]
  deny <approval-id> [-comment text]
  events [-since N] [-task id] [-inbox]
                                  stream the event log (Ctrl-C to stop)
  stop-all [-pause-triggers]      cancel every running task

Environment:
  NEXUS_URL    hub URL (default http://localhost:7700)
  NEXUS_TOKEN  bearer token (security.dev_token in nexus.yaml)
  NEXUS_CA_FILE  hub CA (PEM) for https:// hubs using the private CA
                 (<data_dir>/security/pki/ca.crt on the hub; -ca flag)
`

type command func(ctx context.Context, c *client, args []string) error

var commands = map[string]command{
	"status":   cmdStatus,
	"agents":   cmdAgents,
	"tasks":    cmdTasks,
	"task":     cmdTask,
	"run":      cmdRun,
	"approve":  func(ctx context.Context, c *client, a []string) error { return cmdDecide(ctx, c, a, true) },
	"deny":     func(ctx context.Context, c *client, a []string) error { return cmdDecide(ctx, c, a, false) },
	"events":   cmdEvents,
	"stop-all": cmdStopAll,
}

func main() {
	global := flag.NewFlagSet("nexus", flag.ExitOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	url := global.String("url", envOr("NEXUS_URL", "http://localhost:7700"), "hub URL")
	token := global.String("token", os.Getenv("NEXUS_TOKEN"), "bearer token")
	caFile := global.String("ca", os.Getenv("NEXUS_CA_FILE"), "hub CA certificate (PEM) to trust for https:// (e.g. data/security/pki/ca.crt)")
	_ = global.Parse(os.Args[1:])

	if global.NArg() == 0 {
		global.Usage()
		os.Exit(2)
	}
	name, args := global.Arg(0), global.Args()[1:]
	if name == "help" {
		fmt.Print(usage)
		return
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "nexus: unknown command %q\n\n%s", name, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := transport(*caFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexus:", err)
		os.Exit(1)
	}
	if err := cmd(ctx, newClientRT(*url, *token, rt), args); err != nil {
		if ctx.Err() != nil {
			return // interrupted
		}
		fmt.Fprintln(os.Stderr, "nexus:", describe(err))
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
