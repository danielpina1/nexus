package notify

import (
	"context"
	"crypto/ecdsa"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/events"
	"github.com/danielpina1/nexus/backend/internal/security/envelope"
	"github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func TestModuleEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus, err := events.New(st, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)

	h := kerneltest.New(t)
	h.ConfigYAML = "apns: {bundle_id: com.example.nexus}\nlive_activity: {pulse: false}\n"
	h.Provide(kernel.SvcEventBus, kernel.EventBus(bus))
	pub, priv, _ := envelope.GenerateX25519()
	dir := &fakeDir{devices: []*nexusv1.Device{{Id: "dev_1", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS, PushToken: "aa11", EncryptionPublicKey: pub}}}
	h.Provide(kernel.SvcSecurity, dir)

	m := &Module{}
	ctx := context.Background()
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Resolve[kernel.Notifier](h, kernel.SvcNotifier); err != nil {
		t.Fatal(err)
	}
	p := &recPusher{}
	m.svc.pusher = p
	// Events published before Start are not replayed (live from LastSeq).
	_, _ = bus.Publish(ctx, evApproval(&nexusv1.Approval{Id: "apr_old", TaskId: "tsk_0", State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING}))
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)

	_, _ = bus.Publish(ctx, follow("tsk_1"))
	_, _ = bus.Publish(ctx, evApproval(&nexusv1.Approval{Id: "apr_1", TaskId: "tsk_1", Title: "Deploy",
		State: nexusv1.ApprovalState_APPROVAL_STATE_PENDING}))

	deadline := time.Now().Add(5 * time.Second)
	var reqs []apnsReq
	for time.Now().Before(deadline) && len(reqs) == 0 {
		for _, r := range p.take() {
			reqs = append(reqs, apnsReq{r.CollapseID, r.Payload})
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	for _, r := range p.take() {
		reqs = append(reqs, apnsReq{r.CollapseID, r.Payload})
	}
	if len(reqs) != 1 || reqs[0].collapse != "apr_1" {
		t.Fatalf("pushes = %+v", reqs)
	}
	// The envelope was signed with the notify-owned key (no HubSigner in the fake).
	sg, err := m.signer()
	if err != nil {
		t.Fatal(err)
	}
	_, pt := openPush(t, reqs[0].payload, priv, sg.Public().(*ecdsa.PublicKey), nil)
	if !strings.Contains(string(pt), `"title":"Deploy"`) {
		t.Errorf("plaintext %s", pt)
	}
}

type apnsReq struct {
	collapse string
	payload  []byte
}
