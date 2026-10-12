package security

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

func TestCreatePairingAuth(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()
	req := func() *nexusv1.CreatePairingRequest {
		return &nexusv1.CreatePairingRequest{Kind: nexusv1.PairingKind_PAIRING_KIND_DEVICE, Label: "test"}
	}

	if _, err := hs.client.CreatePairing(ctx, connect.NewRequest(req())); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
	// Nodes are denied (static node token).
	nr := authed(req(), nodeToken)
	nr.Header().Set(HeaderNodeID, "node_static")
	if _, err := hs.client.CreatePairing(ctx, nr); code(err) != connect.CodePermissionDenied {
		t.Fatalf("node: %v", err)
	}

	// Dev token: device pairing URL that a phone can redeem.
	resp, err := hs.client.CreatePairing(ctx, authed(req(), devToken))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if m.Kind != nexusv1.PairingKind_PAIRING_KIND_DEVICE || !slices.Equal(m.HubUrls, []string{"https://hub.test:7700"}) ||
		m.CaFingerprint != hs.sec.CA().Fingerprint() {
		t.Fatalf("response: %+v", m)
	}
	if d := m.ExpiresAt.AsTime().Sub(hs.now); d <= 0 || d > pairing.DefaultTTL+time.Second {
		t.Fatalf("expires in %v", d)
	}
	q, err := pairing.ParseQR(m.Url)
	if err != nil || q.Kind != pairing.KindDevice {
		t.Fatalf("url %q: %v", m.Url, err)
	}
	p := newPhone(t)
	pr, err := hs.client.Pair(ctx, connect.NewRequest(p.pairReq(t, q)))
	if err != nil {
		t.Fatal(err)
	}
	p.id = pr.Msg.Device.Id
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}

	// A device session may create a node pairing.
	nreq := &nexusv1.CreatePairingRequest{Kind: nexusv1.PairingKind_PAIRING_KIND_NODE}
	resp, err = hs.client.CreatePairing(ctx, authed(nreq, p.token))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Kind != nexusv1.PairingKind_PAIRING_KIND_NODE {
		t.Fatal("kind")
	}
	if _, err := hs.client.CreatePairing(ctx, authed(&nexusv1.CreatePairingRequest{Kind: 99}, devToken)); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad kind: %v", err)
	}

	// PairNode is public and proof-authenticated.
	q, _ = pairing.ParseQR(resp.Msg.Url)
	k, _ := keys.Generate()
	pub := keys.MarshalPublicKey(&k.PublicKey)
	secret, _ := pairing.DecodeToken(q.Token)
	proof, _ := pairing.Proof(secret, pub, nil, nil)
	nresp, err := hs.client.PairNode(ctx, connect.NewRequest(&nexusv1.PairNodeRequest{
		PairingToken: q.Token, NodeId: "mac-studio", PublicKey: pub, PairingProof: proof}))
	if err != nil {
		t.Fatal(err)
	}
	n := nresp.Msg
	if n.NodeId != "mac-studio" || n.Token == "" || n.SafetyCode != pairing.SafetyCode(hs.sec.HubPublicKey(), pub) {
		t.Fatalf("pair node: %+v", n)
	}
	cert, err := pki.ParseCertPEM(n.ClientCertificate)
	if err != nil {
		t.Fatal(err)
	}
	if kind, id, ok := pki.CertIdentity(cert); !ok || kind != pki.KindNode || id != "mac-studio" {
		t.Fatal("cert identity")
	}
	if err := hs.sec.AuthenticateNode(ctx, "mac-studio", n.Token); err != nil {
		t.Fatal(err)
	}
	// Single use.
	if _, err := hs.client.PairNode(ctx, connect.NewRequest(&nexusv1.PairNodeRequest{
		PairingToken: q.Token, NodeId: "mac-studio", PublicKey: pub, PairingProof: proof})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := hs.client.PairNode(ctx, connect.NewRequest(&nexusv1.PairNodeRequest{
		PairingToken: q.Token, NodeId: "bad id/..", PublicKey: pub, PairingProof: proof})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
	// Device pairing codes cannot pair nodes.
	resp, _ = hs.client.CreatePairing(ctx, authed(req(), devToken))
	q, _ = pairing.ParseQR(resp.Msg.Url)
	secret, _ = pairing.DecodeToken(q.Token)
	proof, _ = pairing.Proof(secret, pub, nil, nil)
	if _, err := hs.client.PairNode(ctx, connect.NewRequest(&nexusv1.PairNodeRequest{
		PairingToken: q.Token, PublicKey: pub, PairingProof: proof})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("device code for node: %v", err)
	}
}

func TestCreatePairingNeedsHubURL(t *testing.T) {
	hs := newHarness(t, "")
	hs.sec.cfg.HubURLs = nil
	_, err := hs.client.CreatePairing(context.Background(), authed(&nexusv1.CreatePairingRequest{}, devToken))
	if code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v", err)
	}
}

func TestHubTLSConfigServerNames(t *testing.T) {
	hs := newHarness(t, "")
	tc := hs.sec.HubTLSConfig([]string{"10.20.0.10"})
	if tc.MinVersion != 0x0304 {
		t.Fatal("not TLS 1.3")
	}
	c, err := tc.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.Leaf.DNSNames, "localhost") || len(c.Leaf.IPAddresses) != 3 || c.Leaf.IPAddresses[2].String() != "10.20.0.10" {
		t.Fatalf("SANs: %v %v", c.Leaf.DNSNames, c.Leaf.IPAddresses)
	}
	if !slices.Equal(hs.sec.ServerNames(), []string{"localhost", "127.0.0.1", "::1", "10.20.0.10"}) {
		t.Fatal(hs.sec.ServerNames())
	}
}
