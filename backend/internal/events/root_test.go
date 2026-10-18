package events

import (
	"context"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func TestRootTaskFilter(t *testing.T) {
	b := newBus(t, Options{})
	child := output("child", "c1")
	child.RootTaskId = "root"
	publish(t, b, output("root", "r1"), child, output("other", "o1"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := b.Subscribe(ctx, 0, kernel.EventFilter{RootTaskIDs: []string{"root"}})
	if err != nil {
		t.Fatal(err)
	}
	live := output("grandchild", "g1")
	live.RootTaskId = "root"
	publish(t, b, live, output("other", "o2"))
	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 3 {
		select {
		case ev := <-ch:
			got = append(got, ev.GetRunOutput().GetText())
		case <-timeout:
			t.Fatalf("got %v", got)
		}
	}
	if got[0] != "r1" || got[1] != "c1" || got[2] != "g1" {
		t.Fatalf("got %v", got)
	}
	if !Match(kernel.EventFilter{RootTaskIDs: []string{"root"}}, live) || Match(kernel.EventFilter{RootTaskIDs: []string{"root"}}, &nexusv1.Event{TaskId: "x"}) {
		t.Fatal("Match")
	}
}
