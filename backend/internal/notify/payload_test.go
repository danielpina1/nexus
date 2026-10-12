package notify

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/notify/apns"
	"github.com/danielpina1/nexus/backend/internal/security/envelope"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
	"github.com/danielpina1/nexus/backend/kernel"
)

func kernelNotification() kernel.Notification {
	return kernel.Notification{
		Category: CatApproval, Title: "Checkout", Body: "22 items · €84.30", ThreadID: "tsk_1", CollapseID: "apr_1",
		TimeSensitive: true,
		Data: map[string]string{
			DataApprovalID: "apr_1", DataChoices: `["Yes","No"]`, DataSubtitle: "sub",
			DataItems: `[{"name":"Milk","amount":"€1.20"}]`,
		},
	}
}

// openPush decodes nexus_envelope from an APNs payload and opens it the way
// the iOS extension does (purpose NOTIFICATION, recipient key).
func openPush(t *testing.T, payload []byte, recipientPriv []byte, sender *ecdsa.PublicKey, guard *replay.Guard) (*nexusv1.EnvelopeHeader, []byte) {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(payload, &top); err != nil {
		t.Fatal(err)
	}
	s, _ := top[EnvelopeKey].(string)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || s == "" {
		t.Fatalf("nexus_envelope: %v", err)
	}
	env := &nexusv1.SealedEnvelope{}
	if err := proto.Unmarshal(raw, env); err != nil {
		t.Fatal(err)
	}
	hdr, pt, err := envelope.Open(env, envelope.OpenOptions{
		RecipientPrivateKey: recipientPriv,
		Purpose:             nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_NOTIFICATION,
		Sender: func(id string) (*ecdsa.PublicKey, error) {
			if id != keys.KeyID(sender) {
				return nil, errors.New("unknown sender")
			}
			return sender, nil
		},
		Replay: guard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return hdr, pt
}

func TestEnvelopeRoundTrip(t *testing.T) {
	signer, _ := keys.Generate()
	sl, err := newSealer(signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := envelope.GenerateX25519()
	m := FromNotification(kernelNotification())
	m.TaskID, m.AgentID, m.AgentName, m.AttachmentURL = "tsk_1", "shopper", "Shopper", "https://hub/blob/x.png"

	guard := replay.New(replay.DefaultWindow)
	var lastCounter uint64
	for i := range 2 {
		payload, err := buildAlert(m, LevelTimeSensitive, true, sl, pub)
		if err != nil {
			t.Fatal(err)
		}
		// Visible alert is generic; content only inside the envelope.
		if strings.Contains(string(payload), "Checkout") || strings.Contains(string(payload), "84.30") {
			t.Fatalf("plaintext leaked: %s", payload)
		}
		aps := decodeAPS(t, payload)
		alert := aps["alert"].(map[string]any)
		if alert["title"] != GenericTitle || alert["body"] != GenericBody || aps["mutable-content"] != float64(1) ||
			aps["category"] != CatApproval || aps["thread-id"] != "tsk_1" || aps["interruption-level"] != "time-sensitive" {
			t.Errorf("aps = %v", aps)
		}
		hdr, pt := openPush(t, payload, priv, &signer.PublicKey, guard)
		if hdr.RecipientKeyId != keys.KeyIDBytes(pub) || hdr.CorrelationId != "apr_1" || len(hdr.Nonce) < 16 {
			t.Errorf("header %+v", hdr)
		}
		if hdr.Counter <= lastCounter {
			t.Errorf("counter not increasing: %d after %d", hdr.Counter, lastCounter)
		}
		lastCounter = hdr.Counter
		var got map[string]any
		if err := json.Unmarshal(pt, &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"title": "Checkout", "subtitle": "sub", "body": "22 items · €84.30", "category": "APPROVAL",
			"thread_id": "tsk_1", "task_id": "tsk_1", "approval_id": "apr_1", "agent_id": "shopper",
			"agent_name": "Shopper", "attachment_url": "https://hub/blob/x.png",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("iter %d: plaintext[%s] = %v, want %v", i, k, got[k], v)
			}
		}
		if ch, _ := got["choices"].([]any); len(ch) != 2 {
			t.Errorf("choices = %v", got["choices"])
		}
		if it, _ := got["items"].([]any); len(it) != 1 || it[0].(map[string]any)["amount"] != "€1.20" {
			t.Errorf("items = %v", got["items"])
		}
	}
}

func TestPlaintextModeAndSizeGuard(t *testing.T) {
	m := Message{Category: CatResult, Title: "Done", Body: strings.Repeat("é", 5000), TaskID: "tsk_1", ThreadID: "tsk_1"}
	payload, err := buildAlert(m, LevelPassive, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > MaxPayload {
		t.Fatalf("payload %d bytes", len(payload))
	}
	aps := decodeAPS(t, payload)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "Done" || !strings.HasSuffix(alert["body"].(string), "…") {
		t.Errorf("alert = %v", alert)
	}
	if _, ok := aps["mutable-content"]; ok {
		t.Error("plaintext mode should not set mutable-content")
	}
	if _, ok := aps["sound"]; ok {
		t.Error("passive must be silent")
	}
	// Encrypted with a huge body still fits.
	signer, _ := keys.Generate()
	sl, _ := newSealer(signer, time.Now)
	pub, _, _ := envelope.GenerateX25519()
	payload, err = buildAlert(m, LevelActive, true, sl, pub)
	if err != nil || len(payload) > MaxPayload {
		t.Fatalf("encrypted payload %d bytes, err %v", len(payload), err)
	}
}

func TestServiceSendEncryptedAnd410(t *testing.T) {
	signer, _ := keys.Generate()
	pub, priv, _ := envelope.GenerateX25519()
	dir := &droppingDir{fakeDir{devices: []*nexusv1.Device{
		{Id: "dev_1", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS, PushToken: "aa11", EncryptionPublicKey: pub},
		{Id: "dev_2", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS, PushToken: "bb22"}, // no encryption key
		{Id: "dev_web", Kind: nexusv1.DeviceKind_DEVICE_KIND_WEB, PushToken: "cc33"},
		{Id: "dev_3", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS}, // no push token
	}}}
	p := &recPusher{status: func(r apns.Request) int {
		if r.DeviceToken == "bb22" {
			return 410
		}
		return 200
	}}
	cfg := Config{APNS: APNSConfig{BundleID: "com.example.nexus", Environment: "sandbox"}}
	_ = cfg.defaults()
	svc := newService(cfg, discard, p, func() Directory { return dir }, func() (crypto.Signer, error) { return signer, nil }, "")
	if err := svc.Notify(context.Background(), kernelNotification()); err != nil {
		t.Fatal(err)
	}
	reqs := p.take()
	if len(reqs) != 2 {
		t.Fatalf("pushes = %d, want 2", len(reqs))
	}
	r := reqs[0]
	if r.DeviceToken != "aa11" || r.Topic != "com.example.nexus" || r.PushType != "alert" || r.Priority != 10 ||
		r.CollapseID != "apr_1" || r.Environment != "sandbox" || r.Expiration.IsZero() {
		t.Errorf("request %+v", r)
	}
	_, pt := openPush(t, r.Payload, priv, &signer.PublicKey, nil)
	if !strings.Contains(string(pt), `"title":"Checkout"`) {
		t.Errorf("plaintext %s", pt)
	}
	// dev_2: generic alert, no envelope, then 410 → dropped via security.
	if strings.Contains(string(reqs[1].Payload), EnvelopeKey) || strings.Contains(string(reqs[1].Payload), "Checkout") {
		t.Errorf("device without key got %s", reqs[1].Payload)
	}
	if len(dir.dropped) != 1 || dir.dropped[0] != "push:dev_2:bb22" {
		t.Errorf("dropped = %v", dir.dropped)
	}
	// DeviceIDs filter.
	n := kernelNotification()
	n.DeviceIDs = []string{"dev_1"}
	_ = svc.Notify(context.Background(), n)
	if got := p.take(); len(got) != 1 || got[0].DeviceToken != "aa11" {
		t.Errorf("filtered pushes = %v", got)
	}
}

func TestService410LocalDenylist(t *testing.T) {
	dir := &fakeDir{devices: []*nexusv1.Device{{Id: "dev_1", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS, PushToken: "aa11"}}}
	p := &recPusher{status: func(apns.Request) int { return 410 }}
	cfg := Config{Encrypt: new(bool)}
	_ = cfg.defaults()
	path := t.TempDir() + "/deny.json"
	svc := newService(cfg, discard, p, func() Directory { return dir }, nil, path)
	ctx := context.Background()
	_ = svc.Send(ctx, Message{Title: "a", Body: "b"})
	_ = svc.Send(ctx, Message{Title: "a", Body: "b"})
	if n := len(p.take()); n != 1 {
		t.Errorf("pushes = %d, want 1 (token denylisted after 410)", n)
	}
	// Persisted.
	if !loadDenylist(path).denied("aa11", time.Time{}) {
		t.Error("denylist not persisted")
	}
	// A Live Activity token re-registered after the 410 is allowed again.
	d := loadDenylist("")
	d.add("tok", time.Unix(100, 0))
	if !d.denied("tok", time.Unix(50, 0)) || d.denied("tok", time.Unix(200, 0)) {
		t.Error("re-registration handling")
	}
}

func TestQuietHours(t *testing.T) {
	q := &QuietHours{Start: "22:00", End: "07:00", Timezone: "UTC"}
	if err := q.parse(); err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	if !q.Active(at(23, 0)) || !q.Active(at(6, 59)) || q.Active(at(7, 0)) || q.Active(at(12, 0)) {
		t.Error("wrapping window")
	}
	var nilQ *QuietHours
	if nilQ.Active(at(23, 0)) {
		t.Error("nil quiet hours")
	}
	// Active notifications become passive (priority 5, no sound) in quiet hours.
	dir := &fakeDir{devices: []*nexusv1.Device{{Id: "d", PushToken: "aa"}}}
	p := &recPusher{}
	cfg := Config{Encrypt: new(bool), QuietHours: q}
	_ = cfg.defaults()
	svc := newService(cfg, discard, p, func() Directory { return dir }, nil, "")
	svc.now = func() time.Time { return at(23, 30) }
	_ = svc.Send(context.Background(), Message{Category: CatResult, Title: "x", Body: "y"})
	_ = svc.Send(context.Background(), Message{Category: CatAlert, Title: "x", Body: "y"})
	reqs := p.take()
	if reqs[0].Priority != 5 || decodeAPS(t, reqs[0].Payload)["interruption-level"] != "passive" {
		t.Errorf("result in quiet hours: %s", reqs[0].Payload)
	}
	if reqs[1].Priority != 10 || decodeAPS(t, reqs[1].Payload)["interruption-level"] != "time-sensitive" {
		t.Errorf("alert in quiet hours: %s", reqs[1].Payload)
	}
}
