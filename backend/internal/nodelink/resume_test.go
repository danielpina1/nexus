package nodelink

import (
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// Offline queuing: reliable messages are kept, unreliable ones fail, and the
// outbox is bounded.
func TestOfflineOutbox(t *testing.T) {
	h := kerneltest.New(t)
	hb := NewHub(h, Config{OutboxLimit: 2})
	ns := &nodeState{id: "n"}
	hb.nodes["n"] = ns
	cancel := &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Cancel{Cancel: &nexusv1.RunCancel{RunId: "r"}}}
	if err := hb.send(ns, cancel); err != nil {
		t.Fatal(err)
	}
	if err := hb.send(ns, &nexusv1.ConnectResponse{Msg: &nexusv1.ConnectResponse_Ack{Ack: &nexusv1.Ack{}}}); err == nil {
		t.Fatal("unreliable send to offline node should fail")
	}
	_ = hb.send(ns, cancel)
	if err := hb.send(ns, cancel); err == nil {
		t.Fatal("outbox limit not enforced")
	}
	if len(ns.outbox) != 2 || ns.outbox[0].seq != 0 {
		t.Fatalf("outbox %+v", ns.outbox)
	}
	// Assign to an offline-only registry fails.
	if _, err := hb.Assign(t.Context(), kernel.NodeSelector{}, &nexusv1.RunAssignment{RunId: "r"}); err == nil {
		t.Fatal("assign without online nodes should fail")
	}
}
