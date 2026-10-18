package events

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
)

func serveEvents(t *testing.T, svc *Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(nexusv1connect.NewEventServiceHandler(svc))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// The response headers arrive as soon as the subscription is open, before
// any event or keepalive.
func TestSubscribeFlushesHeadersImmediately(t *testing.T) {
	bus := newBus(t, Options{})
	srv := serveEvents(t, &Service{Bus: bus, Heartbeat: time.Hour})

	msg, err := proto.Marshal(&nexusv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	body.WriteByte(0)
	binary.Write(&body, binary.BigEndian, uint32(len(msg)))
	body.Write(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+nexusv1connect.EventServiceSubscribeProcedure, &body)
	req.Header.Set("Content-Type", "application/connect+proto")
	start := time.Now()
	resp, err := srv.Client().Do(req) // returns once the headers are in
	if err != nil {
		t.Fatalf("no response headers on an idle stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d after %v", resp.StatusCode, time.Since(start))
	}
}

// An idle stream carries keepalives (no event); events still flow and the
// keepalive restarts after each event.
func TestSubscribeHeartbeats(t *testing.T) {
	ctx := context.Background()
	bus := newBus(t, Options{})
	srv := serveEvents(t, &Service{Bus: bus, Heartbeat: 30 * time.Millisecond})
	client := nexusv1connect.NewEventServiceClient(srv.Client(), srv.URL)

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := client.Subscribe(sctx, connect.NewRequest(&nexusv1.SubscribeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	beats := 0
	for beats < 2 && stream.Receive() {
		m := stream.Msg()
		if !m.GetHeartbeat() || m.GetEvent() != nil {
			t.Fatalf("idle stream sent %v", m)
		}
		beats++
	}
	if beats < 2 {
		t.Fatalf("got %d heartbeats (err %v)", beats, stream.Err())
	}
	if _, err := bus.Publish(ctx, output("a", "hello")); err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		m := stream.Msg()
		if m.GetHeartbeat() {
			continue
		}
		if m.GetEvent().GetRunOutput().GetText() != "hello" {
			t.Fatalf("unexpected message %v", m)
		}
		return
	}
	t.Fatalf("stream ended: %v", stream.Err())
}

// Keepalives can be disabled (Heartbeat < 0).
func TestSubscribeHeartbeatDisabled(t *testing.T) {
	s := &Service{Heartbeat: -1}
	if s.heartbeat() >= 0 {
		t.Fatal("negative heartbeat must disable keepalives")
	}
	if (&Service{}).heartbeat() != DefaultHeartbeat {
		t.Fatal("default heartbeat")
	}
}
