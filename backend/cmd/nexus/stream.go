package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// stopStream is returned by an event handler to end streaming cleanly.
var stopStream = errors.New("stop")

// stream subscribes to the event log from since and calls fn for each event.
// Dropped connections are resumed from the last seen seq with backoff;
// errors that retrying cannot fix (auth, missing service) are returned.
func stream(ctx context.Context, c *client, req *nexusv1.SubscribeRequest, fn func(*nexusv1.Event) error) error {
	since := req.GetSinceSeq()
	backoff := 500 * time.Millisecond
	for {
		r := &nexusv1.SubscribeRequest{SinceSeq: since, TaskIds: req.GetTaskIds(), InboxOnly: req.GetInboxOnly()}
		s, err := c.events.Subscribe(ctx, connect.NewRequest(r))
		if err == nil {
			for s.Receive() {
				ev := s.Msg().GetEvent()
				if ev == nil {
					continue
				}
				backoff = 500 * time.Millisecond
				since = ev.GetSeq()
				if err := fn(ev); err != nil {
					s.Close()
					if err == stopStream {
						return nil
					}
					return err
				}
			}
			err = s.Err()
			s.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch connect.CodeOf(err) {
		case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnimplemented, connect.CodeInvalidArgument:
			return err
		}
		if err == nil {
			err = fmt.Errorf("stream ended")
		}
		fmt.Fprintf(os.Stderr, "event stream interrupted (%s); reconnecting from seq %d…\n", describe(err), since)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}
