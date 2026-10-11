package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func cmdEvents(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	since := fs.Int64("since", 0, "replay events after this seq (0 = from the beginning)")
	task := fs.String("task", "", "only events of this task")
	inbox := fs.Bool("inbox", false, "only inbox events (approvals, questions, notices)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	req := &nexusv1.SubscribeRequest{SinceSeq: *since, InboxOnly: *inbox}
	if *task != "" {
		req.TaskIds = []string{*task}
	}
	fmt.Fprintf(os.Stderr, "streaming events from %s after seq %d (Ctrl-C to stop)\n", c.baseURL, *since)
	return stream(ctx, c, req, func(ev *nexusv1.Event) error {
		fmt.Println(eventLine(ev))
		return nil
	})
}
