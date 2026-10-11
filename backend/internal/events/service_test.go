package events

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func TestModuleAndService(t *testing.T) {
	ctx := context.Background()
	h := kerneltest.New(t)
	sm := &store.Module{}
	if err := sm.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	defer sm.Stop(ctx)
	m := &Module{}
	if info := m.Info(); info.Name != "events" || len(info.Requires) != 1 || info.Requires[0] != "store" {
		t.Fatalf("bad info %+v", info)
	}
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	bus, err := kernel.Resolve[kernel.EventBus](h, kernel.SvcEventBus)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(h.Mux)
	defer srv.Close()
	client := nexusv1connect.NewEventServiceClient(srv.Client(), srv.URL)

	if _, err := bus.Publish(ctx, output("a", "before")); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Publish(ctx, output("b", "other")); err != nil {
		t.Fatal(err)
	}

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := client.Subscribe(sctx, connect.NewRequest(&nexusv1.SubscribeRequest{TaskIds: []string{"a"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.Publish(ctx, output("a", "after"))
	}()
	var got []string
	for len(got) < 2 && stream.Receive() {
		got = append(got, stream.Msg().GetEvent().GetRunOutput().GetText())
	}
	if len(got) != 2 || got[0] != "before" || got[1] != "after" {
		t.Fatalf("got %v (err %v)", got, stream.Err())
	}

	// Module Stop ends open streams with a retryable error.
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if stream.Receive() {
		t.Fatal("unexpected message after Stop")
	}
	if code := connect.CodeOf(stream.Err()); code != connect.CodeUnavailable {
		t.Fatalf("stream end code = %v (%v)", code, stream.Err())
	}
}
