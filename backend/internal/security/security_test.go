package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/internal/security/audit"
	"github.com/danielpina1/nexus/backend/internal/security/envelope"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
	"github.com/danielpina1/nexus/backend/internal/security/requestsig"
	"github.com/danielpina1/nexus/backend/internal/sqlitex"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

type memStore struct{ db *sql.DB }

func (m memStore) DB() *sql.DB { return m.db }
func (m memStore) Migrate(_ string, stmts []string) error {
	for _, s := range stmts {
		if _, err := m.db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

type harness struct {
	t      *testing.T
	h      *kerneltest.Host
	sec    *Security
	srv    *httptest.Server
	client nexusv1connect.DeviceServiceClient
	now    time.Time
}

const devToken = "dev-token-0123456789abcdef"
const nodeToken = "static-node-token-0123456789"

func newHarness(t *testing.T, extraYAML string) *harness {
	t.Helper()
	db, err := sqlitex.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := kerneltest.New(t)
	h.ConfigYAML = "dev_token: " + devToken + "\nnode_tokens: [" + nodeToken + "]\nhub_urls: [\"https://hub.test:7700\"]\n" + extraYAML
	h.Provide(kernel.SvcStore, kernel.Store(memStore{db}))
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	sec, err := kernel.Resolve[*Security](h, kernel.SvcSecurity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Resolve[kernel.Authenticator](h, kernel.SvcAuth); err != nil {
		t.Fatal(err)
	}
	hs := &harness{t: t, h: h, sec: sec, now: time.Now()}
	sec.SetClock(func() time.Time { return hs.now })
	hs.srv = httptest.NewServer(sec.Middleware(h.Mux))
	t.Cleanup(hs.srv.Close)
	hs.client = nexusv1connect.NewDeviceServiceClient(hs.srv.Client(), hs.srv.URL)
	return hs
}

type phone struct {
	auth, appr *ecdsa.PrivateKey
	encPub     []byte
	encPriv    []byte
	id         string
	token      string
	cert       []byte
}

func newPhone(t *testing.T) *phone {
	a, _ := keys.Generate()
	b, _ := keys.Generate()
	pub, priv, err := envelope.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	return &phone{auth: a, appr: b, encPub: pub, encPriv: priv}
}

func (p *phone) pairReq(t *testing.T, qr *pairing.QR) *nexusv1.PairRequest {
	secret, err := pairing.DecodeToken(qr.Token)
	if err != nil {
		t.Fatal(err)
	}
	ap := keys.MarshalPublicKey(&p.auth.PublicKey)
	bp := keys.MarshalPublicKey(&p.appr.PublicKey)
	proof, _ := pairing.Proof(secret, ap, bp, p.encPub)
	return &nexusv1.PairRequest{PairingToken: qr.Token, Name: "iPhone", Kind: nexusv1.DeviceKind_DEVICE_KIND_IOS,
		PublicKey: ap, ApprovalPublicKey: bp, EncryptionPublicKey: p.encPub, PairingProof: proof}
}

func authed[T any](msg *T, token string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	return r
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func (hs *harness) pair(p *phone) {
	t := hs.t
	qr, err := hs.sec.NewPairing(context.Background(), PairingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pairing.ParseQR(qr.URL())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.CAFingerprint != hs.sec.CA().Fingerprint() {
		t.Fatal("QR CA fingerprint")
	}
	resp, err := hs.client.Pair(context.Background(), connect.NewRequest(p.pairReq(t, parsed)))
	if err != nil {
		t.Fatal(err)
	}
	p.id = resp.Msg.Device.Id
	p.cert = resp.Msg.ClientCertificate
	want := pairing.SafetyCode(hs.sec.HubPublicKey(), keys.MarshalPublicKey(&p.auth.PublicKey),
		keys.MarshalPublicKey(&p.appr.PublicKey), p.encPub)
	if resp.Msg.SafetyCode != want {
		t.Fatalf("safety code %q want %q", resp.Msg.SafetyCode, want)
	}
}

func (hs *harness) login(p *phone) error {
	ctx := context.Background()
	ch, err := hs.client.GetChallenge(ctx, connect.NewRequest(&nexusv1.GetChallengeRequest{DeviceId: p.id}))
	if err != nil {
		return err
	}
	sig, _ := keys.Sign(p.auth, AuthenticateMessage(p.id, ch.Msg.Challenge))
	resp, err := hs.client.Authenticate(ctx, connect.NewRequest(&nexusv1.AuthenticateRequest{DeviceId: p.id, Challenge: ch.Msg.Challenge, Signature: sig}))
	if err != nil {
		return err
	}
	p.token = resp.Msg.AccessToken
	return nil
}

func TestPairingAndSessions(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()

	// Unauthenticated access is rejected.
	if _, err := hs.client.ListDevices(ctx, connect.NewRequest(&nexusv1.ListDevicesRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anon ListDevices: %v", err)
	}
	if _, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, "nxs_bogus")); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("bogus token: %v", err)
	}

	p := newPhone(t)
	hs.pair(p)
	cert, err := pki.ParseCertPEM(p.cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: hs.sec.CA().Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if k, id, ok := pki.CertIdentity(cert); !ok || k != "device" || id != p.id {
		t.Fatal("cert identity")
	}

	// Login.
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}
	list, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, p.token))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Devices) != 1 || !list.Msg.Devices[0].Primary {
		t.Fatalf("devices %+v", list.Msg.Devices)
	}
	// Dev token works; static node token is not allowed to manage devices.
	if _, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, devToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, nodeToken)); code(err) != connect.CodePermissionDenied {
		t.Fatalf("node ListDevices: %v", err)
	}

	// Second device is not primary.
	p2 := newPhone(t)
	hs.pair(p2)
	if err := hs.login(p2); err != nil {
		t.Fatal(err)
	}
	list, _ = hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, p.token))
	if len(list.Msg.Devices) != 2 || list.Msg.Devices[1].Primary {
		t.Fatal("second device primary")
	}

	// Push + live activity tokens.
	if _, err := hs.client.RegisterPushToken(ctx, authed(&nexusv1.RegisterPushTokenRequest{PushToken: "apns", Environment: "sandbox"}, p.token)); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.client.RegisterPushToken(ctx, authed(&nexusv1.RegisterPushTokenRequest{PushToken: "apns"}, devToken)); code(err) != connect.CodePermissionDenied {
		t.Fatalf("dev push token: %v", err)
	}
	if _, err := hs.client.RegisterLiveActivityToken(ctx, authed(&nexusv1.RegisterLiveActivityTokenRequest{
		Kind: nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE, ActivityType: "run", ActivityId: "a1", TaskId: "tsk_1", Token: []byte{1, 2}}, p.token)); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.client.RegisterLiveActivityToken(ctx, authed(&nexusv1.RegisterLiveActivityTokenRequest{
		Kind: nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE, Token: []byte{1}}, p.token)); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing activity id: %v", err)
	}
	lat, err := hs.sec.LiveActivityTokens(ctx, 0, "run")
	if err != nil || len(lat) != 1 || lat[0].TaskID != "tsk_1" || lat[0].DeviceID != p.id {
		t.Fatalf("live activity tokens %v %+v", err, lat)
	}
	devs, _ := hs.sec.Devices(ctx)
	if devs[0].PushToken != "apns" {
		t.Fatal("push token not stored")
	}

	// Session expiry.
	hs.now = hs.now.Add(2 * time.Hour)
	if _, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, p.token)); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired session: %v", err)
	}
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}

	// Revoke p2 from p: its session dies, it can't log in again.
	if _, err := hs.client.RevokeDevice(ctx, authed(&nexusv1.RevokeDeviceRequest{Id: p2.id}, p.token)); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.client.ListDevices(ctx, authed(&nexusv1.ListDevicesRequest{}, p2.token)); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked session: %v", err)
	}
	if err := hs.login(p2); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked login: %v", err)
	}
	if _, err := hs.client.RevokeDevice(ctx, authed(&nexusv1.RevokeDeviceRequest{Id: p2.id}, p.token)); code(err) != connect.CodeNotFound {
		t.Fatalf("double revoke: %v", err)
	}
	if lat, _ := hs.sec.LiveActivityTokens(ctx, 0, ""); len(lat) != 1 {
		t.Fatal("tokens of other device affected")
	}

	// Audit log is intact and checkpoint verifies with the hub key.
	if err := hs.sec.Audit().Verify(); err != nil {
		t.Fatal(err)
	}
	cp, err := hs.sec.AuditCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	hubPub, _ := keys.ParsePublicKey(hs.sec.HubPublicKey())
	if err := audit.VerifyAgainstCheckpoint(hs.sec.Audit().Entries(0), cp, hubPub); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range hs.sec.Audit().Entries(0) {
		kinds = append(kinds, string(e.Payload))
	}
	all := strings.Join(kinds, "\n")
	for _, k := range []string{"device.paired", "device.revoked", "pairing.enabled", "auth.failed"} {
		if !strings.Contains(all, `"kind":"`+k+`"`) {
			t.Errorf("audit missing %s", k)
		}
	}
}

func TestPairRejections(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()
	p := newPhone(t)

	qr, _ := hs.sec.NewPairing(ctx, PairingOptions{})
	req := p.pairReq(t, qr)
	req.PairingProof[0] ^= 1
	if _, err := hs.client.Pair(ctx, connect.NewRequest(req)); code(err) != connect.CodePermissionDenied {
		t.Fatalf("bad proof: %v", err)
	}
	// Token was consumed by the failed attempt.
	if _, err := hs.client.Pair(ctx, connect.NewRequest(p.pairReq(t, qr))); code(err) != connect.CodePermissionDenied {
		t.Fatalf("reuse after failure: %v", err)
	}

	// Single use.
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	if _, err := hs.client.Pair(ctx, connect.NewRequest(p.pairReq(t, qr))); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.client.Pair(ctx, connect.NewRequest(newPhone(t).pairReq(t, qr))); code(err) != connect.CodePermissionDenied {
		t.Fatalf("reuse: %v", err)
	}

	// Expired.
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	hs.now = hs.now.Add(6 * time.Minute)
	if _, err := hs.client.Pair(ctx, connect.NewRequest(newPhone(t).pairReq(t, qr))); code(err) != connect.CodePermissionDenied {
		t.Fatalf("expired: %v", err)
	}

	// Keys swapped by a MITM after the proof was computed.
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	req = newPhone(t).pairReq(t, qr)
	evil, _ := keys.Generate()
	req.PublicKey = keys.MarshalPublicKey(&evil.PublicKey)
	if _, err := hs.client.Pair(ctx, connect.NewRequest(req)); code(err) != connect.CodePermissionDenied {
		t.Fatalf("swapped key: %v", err)
	}

	// Node pairing token cannot pair a device.
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{Kind: pairing.KindNode})
	if _, err := hs.client.Pair(ctx, connect.NewRequest(newPhone(t).pairReq(t, qr))); code(err) != connect.CodePermissionDenied {
		t.Fatalf("node token for device: %v", err)
	}

	// Garbage key.
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	req = newPhone(t).pairReq(t, qr)
	req.PublicKey = []byte{1, 2, 3}
	if _, err := hs.client.Pair(ctx, connect.NewRequest(req)); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad key: %v", err)
	}
}

func TestRequireAppAttest(t *testing.T) {
	hs := newHarness(t, "require_app_attest: true\n")
	ctx := context.Background()
	qr, _ := hs.sec.NewPairing(ctx, PairingOptions{})
	if _, err := hs.client.Pair(ctx, connect.NewRequest(newPhone(t).pairReq(t, qr))); code(err) != connect.CodePermissionDenied {
		t.Fatalf("missing attestation: %v", err)
	}
	hs.sec.SetAppAttestVerifier(attestFunc(func(_ context.Context, kid string, att, cdh []byte) error {
		if string(att) != "ok" || len(cdh) != 32 {
			return errors.New("bad attestation")
		}
		return nil
	}))
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	req := newPhone(t).pairReq(t, qr)
	req.AppAttestation, req.AppAttestKeyId = []byte("ok"), "kid"
	if _, err := hs.client.Pair(ctx, connect.NewRequest(req)); err != nil {
		t.Fatal(err)
	}
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{})
	req = newPhone(t).pairReq(t, qr)
	req.AppAttestation = []byte("forged")
	if _, err := hs.client.Pair(ctx, connect.NewRequest(req)); code(err) != connect.CodePermissionDenied {
		t.Fatalf("forged attestation: %v", err)
	}
}

type attestFunc func(context.Context, string, []byte, []byte) error

func (f attestFunc) VerifyAttestation(ctx context.Context, k string, a, c []byte) error {
	return f(ctx, k, a, c)
}

func TestAuthenticateRejections(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()
	p := newPhone(t)
	hs.pair(p)

	ch, _ := hs.client.GetChallenge(ctx, connect.NewRequest(&nexusv1.GetChallengeRequest{DeviceId: p.id}))
	// Signed with the wrong key (approval key instead of auth key).
	sig, _ := keys.Sign(p.appr, AuthenticateMessage(p.id, ch.Msg.Challenge))
	_, err := hs.client.Authenticate(ctx, connect.NewRequest(&nexusv1.AuthenticateRequest{DeviceId: p.id, Challenge: ch.Msg.Challenge, Signature: sig}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong key: %v", err)
	}
	// Challenge is single-use even after a failure.
	sig, _ = keys.Sign(p.auth, AuthenticateMessage(p.id, ch.Msg.Challenge))
	_, err = hs.client.Authenticate(ctx, connect.NewRequest(&nexusv1.AuthenticateRequest{DeviceId: p.id, Challenge: ch.Msg.Challenge, Signature: sig}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("reused challenge: %v", err)
	}
	// Challenge bound to another device id.
	ch, _ = hs.client.GetChallenge(ctx, connect.NewRequest(&nexusv1.GetChallengeRequest{DeviceId: "dev_other"}))
	sig, _ = keys.Sign(p.auth, AuthenticateMessage(p.id, ch.Msg.Challenge))
	_, err = hs.client.Authenticate(ctx, connect.NewRequest(&nexusv1.AuthenticateRequest{DeviceId: p.id, Challenge: ch.Msg.Challenge, Signature: sig}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("other device challenge: %v", err)
	}
	// Expired challenge.
	ch, _ = hs.client.GetChallenge(ctx, connect.NewRequest(&nexusv1.GetChallengeRequest{DeviceId: p.id}))
	hs.now = hs.now.Add(ChallengeTTL)
	sig, _ = keys.Sign(p.auth, AuthenticateMessage(p.id, ch.Msg.Challenge))
	_, err = hs.client.Authenticate(ctx, connect.NewRequest(&nexusv1.AuthenticateRequest{DeviceId: p.id, Challenge: ch.Msg.Challenge, Signature: sig}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired challenge: %v", err)
	}
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}
}

// signedCall makes a raw Connect unary JSON call so the body can be signed.
func (hs *harness) signedCall(path, body, token, deviceID string, signer *ecdsa.PrivateKey, counter uint64) int {
	r, _ := http.NewRequest(http.MethodPost, hs.srv.URL+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if signer != nil {
		if err := requestsig.Sign(r, deviceID, signer, counter, hs.now); err != nil {
			hs.t.Fatal(err)
		}
	}
	resp, err := hs.srv.Client().Do(r)
	if err != nil {
		hs.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestSignedRequests(t *testing.T) {
	hs := newHarness(t, "require_signed_requests: true\n")
	p := newPhone(t)
	hs.pair(p)
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}
	path := nexusv1connect.DeviceServiceListDevicesProcedure
	if c := hs.signedCall(path, `{}`, p.token, p.id, p.auth, 1); c != 200 {
		t.Fatalf("signed: %d", c)
	}
	if c := hs.signedCall(path, `{}`, p.token, "", nil, 0); c != 401 {
		t.Fatalf("unsigned with require: %d", c)
	}
	if c := hs.signedCall(path, `{}`, p.token, p.id, p.auth, 1); c != 401 {
		t.Fatalf("counter replay: %d", c)
	}
	if c := hs.signedCall(path, `{}`, p.token, p.id, p.appr, 2); c != 401 {
		t.Fatalf("wrong key: %d", c)
	}
	p2 := newPhone(t)
	hs.pair(p2)
	if c := hs.signedCall(path, `{}`, p.token, p2.id, p2.auth, 1); c != 401 {
		t.Fatalf("signed by other device than session: %d", c)
	}
	// Dev token is not forced to sign; a signature, when present, is verified.
	if c := hs.signedCall(path, `{}`, devToken, "", nil, 0); c != 200 {
		t.Fatalf("dev unsigned: %d", c)
	}
	if c := hs.signedCall(path, `{}`, devToken, p.id, p2.auth, 3); c != 401 {
		t.Fatalf("dev with bad signature: %d", c)
	}
	// Public endpoints need nothing.
	if c := hs.signedCall(nexusv1connect.DeviceServiceGetChallengeProcedure, `{"deviceId":"x"}`, "", "", nil, 0); c != 200 {
		t.Fatalf("public: %d", c)
	}
}

func TestVerifyApproval(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()
	p := newPhone(t)
	hs.pair(p)

	payload := map[string]any{"cart": []any{"milk"}, "total": 84.3}
	h, _ := approval.ActionHash(payload)
	a := &nexusv1.Approval{Id: "apr_1", Challenge: []byte("0123456789abcdef"), PayloadHash: h,
		ExpiresAt: timestamppb.New(hs.now.Add(time.Hour))}
	d := &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: true, Challenge: a.Challenge, ActionSha256: h,
		ExpiresAt: timestamppb.New(hs.now.Add(5 * time.Minute)), DeviceId: p.id}
	if err := approval.Sign(d, p.appr); err != nil {
		t.Fatal(err)
	}
	if err := hs.sec.VerifyApproval(ctx, d, a); err != nil {
		t.Fatal(err)
	}
	// Signed with the auth key instead of the approval key.
	bad := proto.Clone(d).(*nexusv1.SignedApproval)
	_ = approval.Sign(bad, p.auth)
	if err := hs.sec.VerifyApproval(ctx, bad, a); !errors.Is(err, approval.ErrBadSignature) {
		t.Fatal(err)
	}
	// Different action.
	h2 := sha256.Sum256([]byte("x"))
	a2 := proto.Clone(a).(*nexusv1.Approval)
	a2.PayloadHash = h2[:]
	if err := hs.sec.VerifyApproval(ctx, d, a2); !errors.Is(err, approval.ErrHashMismatch) {
		t.Fatal(err)
	}
	// Expired.
	hs.now = hs.now.Add(10 * time.Minute)
	if err := hs.sec.VerifyApproval(ctx, d, a); !errors.Is(err, approval.ErrExpired) {
		t.Fatal(err)
	}
	hs.now = hs.now.Add(-10 * time.Minute)
	// Revoked.
	if err := hs.sec.RevokeDevice(ctx, p.id, Principal{Kind: PrincipalDev}); err != nil {
		t.Fatal(err)
	}
	if err := hs.sec.VerifyApproval(ctx, d, a); !errors.Is(err, approval.ErrRevoked) {
		t.Fatal(err)
	}
}

func TestNodePairingAndTokens(t *testing.T) {
	hs := newHarness(t, "")
	ctx := context.Background()
	call := func(token, nodeHeader string) int {
		r, _ := http.NewRequest(http.MethodPost, hs.srv.URL+"/nexus.v1.NodeLinkService/Anything", strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+token)
		if nodeHeader != "" {
			r.Header.Set(HeaderNodeID, nodeHeader)
		}
		resp, err := hs.srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// Static node token passes auth (404 = reached the mux, no handler).
	if c := call(nodeToken, "node_static"); c != 404 {
		t.Fatalf("static node token: %d", c)
	}

	qr, err := hs.sec.NewPairing(ctx, PairingOptions{Kind: pairing.KindNode, HubURLs: []string{"https://x"}})
	if err != nil {
		t.Fatal(err)
	}
	nk, _ := keys.Generate()
	pub := keys.MarshalPublicKey(&nk.PublicKey)
	secret, _ := pairing.DecodeToken(qr.Token)
	proof, _ := pairing.Proof(secret, pub, nil, nil)
	if _, err := hs.sec.RedeemNodePairing(ctx, qr.Token, "node_1", pub, append([]byte{0}, proof[1:]...)); err == nil {
		t.Fatal("bad proof accepted")
	}
	qr, _ = hs.sec.NewPairing(ctx, PairingOptions{Kind: pairing.KindNode})
	secret, _ = pairing.DecodeToken(qr.Token)
	proof, _ = pairing.Proof(secret, pub, nil, nil)
	np, err := hs.sec.RedeemNodePairing(ctx, qr.Token, "node_1", pub, proof)
	if err != nil {
		t.Fatal(err)
	}
	if np.SafetyCode != pairing.SafetyCode(hs.sec.HubPublicKey(), pub) {
		t.Fatal("node safety code")
	}
	if c := call(np.Token, "node_1"); c != 404 {
		t.Fatalf("node token: %d", c)
	}
	if c := call(np.Token, "node_2"); c != 401 {
		t.Fatalf("node header mismatch: %d", c)
	}
	if err := hs.sec.AuthenticateNode(ctx, "node_1", np.Token); err != nil {
		t.Fatal(err)
	}
	if err := hs.sec.AuthenticateNode(ctx, "node_2", np.Token); err == nil {
		t.Fatal("token accepted for another node id")
	}
	if err := hs.sec.AuthenticateNode(ctx, "anything", nodeToken); err != nil {
		t.Fatal(err)
	}
	if err := hs.sec.RevokeNode(ctx, "node_1"); err != nil {
		t.Fatal(err)
	}
	if c := call(np.Token, "node_1"); c != 401 {
		t.Fatalf("revoked node: %d", c)
	}
	if err := hs.sec.AuthenticateNode(ctx, "node_1", np.Token); err == nil {
		t.Fatal("revoked node token accepted")
	}
}

func TestMutualTLSIntegration(t *testing.T) {
	hs := newHarness(t, "server_names: [\"127.0.0.1\"]\n")
	p := newPhone(t)
	hs.pair(p)
	if err := hs.login(p); err != nil {
		t.Fatal(err)
	}
	var got Principal
	srv := httptest.NewUnstartedServer(hs.sec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = PrincipalFromContext(r.Context())
	})))
	// Not StartTLS: it would install httptest's own certificate.
	srv.Listener = tls.NewListener(srv.Listener, hs.sec.ServerTLSConfig(pki.RequireClientCert))
	srv.Start()
	defer srv.Close()
	srvURL := strings.Replace(srv.URL, "http://", "https://", 1)

	der, _ := pki.ParseCertPEM(p.cert)
	clientCert := &tls.Certificate{Certificate: [][]byte{der.Raw}, PrivateKey: p.auth}
	do := func(token string) (int, error) {
		tr := &http.Transport{TLSClientConfig: pki.ClientTLSConfig(hs.sec.CA().Cert, clientCert, "127.0.0.1")}
		defer tr.CloseIdleConnections()
		r, _ := http.NewRequest(http.MethodPost, srvURL+"/x", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Transport: tr}).Do(r)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	c, err := do(p.token)
	if err != nil || c != 200 {
		t.Fatalf("%d %v", c, err)
	}
	if got.Kind != PrincipalDevice || got.ID != p.id || !got.CertVerified {
		t.Fatalf("principal %+v", got)
	}
	// Cert of p with session of another device → rejected.
	p2 := newPhone(t)
	hs.pair(p2)
	_ = hs.login(p2)
	if c, err := do(p2.token); err != nil || c != 401 {
		t.Fatalf("cert/token mismatch: %d %v", c, err)
	}
	// Revoked device: handshake refused.
	if err := hs.sec.RevokeDevice(context.Background(), p.id, Principal{Kind: PrincipalDev}); err != nil {
		t.Fatal(err)
	}
	if _, err := do(p.token); err == nil {
		t.Fatal("revoked cert accepted")
	}
}

func TestPublicPaths(t *testing.T) {
	hs := newHarness(t, "public_paths: [\"/healthz\"]\n")
	if !hs.sec.Public(nexusv1connect.DeviceServicePairProcedure) || !hs.sec.Public("/healthz") || hs.sec.Public(nexusv1connect.DeviceServiceListDevicesProcedure) {
		t.Fatal("Public()")
	}
	if _, err := hs.sec.NewPairing(context.Background(), PairingOptions{}); err != nil {
		t.Fatal(err)
	}
}
