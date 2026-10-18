package events

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

// DefaultHeartbeat is how often Subscribe sends a keepalive while no events
// flow.
const DefaultHeartbeat = 20 * time.Second

// Service implements nexusv1connect.EventServiceHandler on top of any
// kernel.EventBus.
type Service struct {
	nexusv1connect.UnimplementedEventServiceHandler
	Bus kernel.EventBus
	// Heartbeat overrides DefaultHeartbeat (tests); < 0 disables keepalives.
	Heartbeat time.Duration
}

func (s *Service) heartbeat() time.Duration {
	if s.Heartbeat == 0 {
		return DefaultHeartbeat
	}
	return s.Heartbeat
}

// Subscribe streams events after since_seq (replay, then live) until the
// client disconnects. Response headers are flushed as soon as the
// subscription is open, and a keepalive (SubscribeResponse.heartbeat, no
// event) is sent whenever no event was sent for the heartbeat interval, so
// clients can tell an idle stream from a hung one. If the bus ends the
// subscription (slow consumer or hub shutdown) the stream ends with
// CodeUnavailable: clients should resubscribe from the last seq they
// processed.
func (s *Service) Subscribe(ctx context.Context, req *connect.Request[nexusv1.SubscribeRequest], stream *connect.ServerStream[nexusv1.SubscribeResponse]) error {
	ch, err := s.Bus.Subscribe(ctx, req.Msg.GetSinceSeq(), kernel.EventFilter{
		TaskIDs:     req.Msg.GetTaskIds(),
		RootTaskIDs: req.Msg.GetRootTaskIds(),
		InboxOnly:   req.Msg.GetInboxOnly(),
	})
	if errors.Is(err, ErrClosed) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	// Flush the response headers now: the client knows the stream is open
	// even when there is nothing to replay.
	if err := stream.Send(nil); err != nil {
		return err
	}
	var tick <-chan time.Time
	var ticker *time.Ticker
	if hb := s.heartbeat(); hb > 0 {
		ticker = time.NewTicker(hb)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return nil // client went away
				}
				return connect.NewError(connect.CodeUnavailable,
					errors.New("subscription closed by hub (slow consumer or shutdown); resubscribe from last seq"))
			}
			if err := stream.Send(&nexusv1.SubscribeResponse{Event: ev}); err != nil {
				return err
			}
			if ticker != nil {
				ticker.Reset(s.heartbeat())
			}
		case <-tick:
			if err := stream.Send(&nexusv1.SubscribeResponse{Heartbeat: true}); err != nil {
				return err
			}
		}
	}
}
