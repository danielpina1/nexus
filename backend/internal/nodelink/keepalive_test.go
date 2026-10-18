package nodelink_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/internal/nodelink"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// The hub advertises its keepalive interval in HubWelcome and sends Acks to
// an idle node, so the node can tell an idle link from a hung one.
func TestHubSendsKeepalives(t *testing.T) {
	ctx := context.Background()
	h := kerneltest.New(t)
	h.ConfigYAML = "tokens:\n  n1: secret\nkeepalive: 40ms\n"
	h.Provide(kernel.SvcEventBus, &fakeBus{})
	m := &nodelink.Module{}
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	srv := httptest.NewUnstartedServer(h.Mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := nexusv1connect.NewNodeLinkServiceClient(srv.Client(), srv.URL)
	s := c.Connect(sctx)
	s.RequestHeader().Set("Authorization", "Bearer secret")
	s.RequestHeader().Set("X-Nexus-Node-Id", "n1")
	if err := s.Send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_Hello{Hello: &nexusv1.NodeHello{NodeId: "n1"}}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.CloseRequest(); _ = s.CloseResponse() }()
	first, err := s.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if w := first.GetWelcome(); w == nil || w.KeepaliveMs != 40 {
		t.Fatalf("welcome = %v", first)
	}
	start := time.Now()
	for i := 0; i < 3; i++ {
		msg, err := s.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetAck() == nil {
			t.Fatalf("keepalive %d = %v", i, msg)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("keepalives too slow: %v", d)
	}
}

// silentHub accepts the link, welcomes the node and then never writes again.
type silentHub struct {
	nexusv1connect.UnimplementedNodeLinkServiceHandler
	keepaliveMs int32
	connects    atomic.Int32
}

func (h *silentHub) Connect(ctx context.Context, s *connect.BidiStream[nexusv1.ConnectRequest, nexusv1.ConnectResponse]) error {
	h.connects.Add(1)
	if _, err := s.Receive(); err != nil {
		return err
	}
	if err := s.Send(&nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Welcome{Welcome: &nexusv1.HubWelcome{KeepaliveMs: h.keepaliveMs}}}); err != nil {
		return err
	}
	go func() { // drain node messages (status reports) without answering
		for {
			if _, err := s.Receive(); err != nil {
				return
			}
		}
	}()
	<-ctx.Done()
	return nil
}

func runNodeAgainst(t *testing.T, hub *silentHub) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(nexusv1connect.NewNodeLinkServiceHandler(hub))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	nh := kerneltest.New(t)
	nh.SideValue = kernel.SideNode
	d, err := node.New(nh, node.Config{
		HubURL: srv.URL, NodeID: "n1", Token: "secret",
		WorkspacesDir: filepath.Join(nh.Dir, "ws"), BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	}, node.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// A hub that promised keepalives and then goes silent is treated as a hung
// link: the node drops the session and reconnects.
func TestNodeReconnectsWhenHubGoesSilent(t *testing.T) {
	hub := &silentHub{keepaliveMs: 30}
	runNodeAgainst(t, hub)
	deadline := time.Now().Add(5 * time.Second)
	for hub.connects.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("node reconnected %d times; want a reconnect after ~%v of silence", hub.connects.Load()-1, node.HubSilenceLimit(30*time.Millisecond))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Old hubs (keepalive_ms = 0) are never timed out by the node.
func TestNodeKeepsQuietOldHub(t *testing.T) {
	hub := &silentHub{}
	runNodeAgainst(t, hub)
	time.Sleep(400 * time.Millisecond)
	if n := hub.connects.Load(); n != 1 {
		t.Fatalf("connects = %d, want 1", n)
	}
	if node.HubSilenceLimit(0) != 0 || node.HubSilenceLimit(time.Second) != 3*time.Second {
		t.Fatal("HubSilenceLimit")
	}
}
