package events

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Service implements nexusv1connect.EventServiceHandler on top of any
// kernel.EventBus.
type Service struct {
	nexusv1connect.UnimplementedEventServiceHandler
	Bus kernel.EventBus
}

// Subscribe streams events after since_seq (replay, then live) until the
// client disconnects. If the bus ends the subscription (slow consumer or hub
// shutdown) the stream ends with CodeUnavailable: clients should resubscribe
// from the last seq they processed.
func (s *Service) Subscribe(ctx context.Context, req *connect.Request[nexusv1.SubscribeRequest], stream *connect.ServerStream[nexusv1.SubscribeResponse]) error {
	ch, err := s.Bus.Subscribe(ctx, req.Msg.GetSinceSeq(), kernel.EventFilter{
		TaskIDs:   req.Msg.GetTaskIds(),
		InboxOnly: req.Msg.GetInboxOnly(),
	})
	if errors.Is(err, ErrClosed) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for ev := range ch {
		if err := stream.Send(&nexusv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil // client went away
	}
	return connect.NewError(connect.CodeUnavailable,
		errors.New("subscription closed by hub (slow consumer or shutdown); resubscribe from last seq"))
}
