package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/internal/security/audit"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
	"github.com/danielpina1/nexus/backend/internal/security/requestsig"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Header carrying the node id alongside a node bearer token.
const HeaderNodeID = "X-Nexus-Node-Id"

// Principal kinds.
const (
	PrincipalDev    = "dev"    // dev_token (development only)
	PrincipalDevice = "device" // paired device with a session token
	PrincipalNode   = "node"   // node token or node mTLS certificate
)

// Principal is the authenticated caller of an API request.
type Principal struct {
	Kind string
	ID   string // device id / node id ("" for dev)
	// CertVerified: a client certificate from the Hub CA was verified and
	// matches ID.
	CertVerified bool
	// Signed: the request carried a valid Layer-3 signature by device ID.
	Signed bool
}

type principalKey struct{}

// PrincipalFromContext returns the caller set by the auth middleware.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithPrincipal attaches a principal (tests and in-process callers).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// AppAttestVerifier verifies an iOS App Attest attestation at pairing.
// clientDataHash = SHA256(public_key || approval_public_key ||
// encryption_public_key) as sent in PairRequest.
type AppAttestVerifier interface {
	VerifyAttestation(ctx context.Context, keyID string, attestation, clientDataHash []byte) error
}

// devAppAttest is the TODO placeholder: accepts everything (dev only).
type devAppAttest struct{}

func (devAppAttest) VerifyAttestation(context.Context, string, []byte, []byte) error { return nil }

type challenge struct {
	deviceID string
	expires  time.Time
}

// Security is the "security" service.
type Security struct {
	log     *slog.Logger
	cfg     Config
	host    kernel.Host
	db      *sql.DB
	ca      *pki.CA
	hubKey  *ecdsa.PrivateKey
	audit   *audit.Log
	pairing *pairing.Manager
	guard   *replay.Guard
	reqsig  *requestsig.Verifier

	attestMu sync.RWMutex
	attest   AppAttestVerifier

	chalMu sync.Mutex
	chal   map[string]challenge

	certMu     sync.Mutex
	serverCert *tls.Certificate

	clock func() time.Time
}

var _ kernel.Authenticator = (*Security)(nil)
var _ approval.KeyLookup = (*Security)(nil)

func (s *Security) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// SetClock overrides the clock (tests).
func (s *Security) SetClock(f func() time.Time) {
	s.clock = f
	s.guard.Now = f
}

// SetAppAttestVerifier installs a real App Attest verifier.
func (s *Security) SetAppAttestVerifier(v AppAttestVerifier) {
	s.attestMu.Lock()
	defer s.attestMu.Unlock()
	s.attest = v
}

// Close releases resources.
func (s *Security) Close() error { return s.audit.Close() }

// CA returns the Hub certificate authority.
func (s *Security) CA() *pki.CA { return s.ca }

// HubPublicKey returns the Hub identity public key (65-byte X9.63). It is
// put in pairing QRs, used in safety codes and signs audit checkpoints.
func (s *Security) HubPublicKey() []byte { return keys.MarshalPublicKey(&s.hubKey.PublicKey) }

// Audit returns the hub security audit log.
func (s *Security) Audit() *audit.Log { return s.audit }

// AuditCheckpoint signs the current audit head with the hub identity key.
func (s *Security) AuditCheckpoint() (*nexusv1.AuditCheckpoint, error) {
	return s.audit.Checkpoint(s.hubKey)
}

// ReplayGuard returns the guard shared by signed requests.
func (s *Security) ReplayGuard() *replay.Guard { return s.guard }

func (s *Security) record(kind string, data map[string]any) {
	if _, err := s.audit.Record(kind, data); err != nil {
		s.log.Error("security: audit append failed", "kind", kind, "err", err)
	}
}

// alert records an audit entry and sends a time-sensitive notification to
// all devices when a notifier is available (best effort).
func (s *Security) alert(kind, title, body string, data map[string]any) {
	s.record(kind, data)
	s.log.Warn("security alert: "+title, "kind", kind, "detail", body)
	if s.host == nil {
		return
	}
	n, err := kernel.Resolve[kernel.Notifier](s.host, kernel.SvcNotifier)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := n.Notify(ctx, kernel.Notification{Category: "ALERT", Title: title, Body: body, TimeSensitive: true}); err != nil {
			s.log.Warn("security: alert notification failed", "err", err)
		}
	}()
}

// ---------- TLS ----------

// ServerTLSConfig returns the Hub's TLS 1.3 server config. The server
// certificate (SANs = security.server_names) is issued on demand and renewed
// when a third of its lifetime remains. Client certificates of revoked or
// unknown devices/nodes are rejected during the handshake. Use
// pki.ClientCertIfGiven for a listener that must also accept unpaired
// devices (pairing).
func (s *Security) ServerTLSConfig(mode pki.ClientAuthMode) *tls.Config {
	return s.ca.ServerTLSConfig(nil, pki.ServerOptions{
		Mode:           mode,
		GetCertificate: s.getServerCert,
		Revoked:        s.certRevoked,
	})
}

func (s *Security) getServerCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	now := s.now()
	if c := s.serverCert; c != nil && c.Leaf != nil {
		life := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore)
		if now.Before(c.Leaf.NotAfter.Add(-life / 3)) {
			return c, nil
		}
	}
	c, err := s.ca.IssueServerCert(pki.ServerCertOptions{Hosts: s.cfg.ServerNames})
	if err != nil {
		return nil, err
	}
	s.serverCert = c
	s.record("cert.server_issued", map[string]any{"hosts": c.Leaf.DNSNames, "not_after": c.Leaf.NotAfter})
	return c, nil
}

func (s *Security) certRevoked(c *x509.Certificate) bool {
	kind, id, ok := pki.CertIdentity(c)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var revokedAt int64
	var err error
	switch kind {
	case pki.KindDevice:
		err = s.db.QueryRowContext(ctx, `SELECT revoked_at FROM security_devices WHERE id = ?`, id).Scan(&revokedAt)
	case pki.KindNode:
		err = s.db.QueryRowContext(ctx, `SELECT revoked_at FROM security_nodes WHERE id = ?`, id).Scan(&revokedAt)
	default:
		return true
	}
	return err != nil || revokedAt != 0
}

// ---------- Authenticator ----------

var publicProcedures = map[string]bool{
	nexusv1connect.DeviceServicePairProcedure:         true,
	nexusv1connect.DeviceServiceGetChallengeProcedure: true,
	nexusv1connect.DeviceServiceAuthenticateProcedure: true,
	// The node link authenticates nodes itself (nodelink calls
	// AuthenticateNode), so the generic middleware lets it through.
	nexusv1connect.NodeLinkServiceConnectProcedure: true,
}

// Public implements kernel.Authenticator: the pairing / challenge endpoints
// and any configured public_paths prefixes.
func (s *Security) Public(path string) bool {
	if publicProcedures[path] {
		return true
	}
	// Webhooks authenticate per hook (HMAC signature or hook secret) in the
	// triggers module, so they bypass session auth.
	if strings.HasPrefix(path, "/hooks/") {
		return true
	}
	for _, p := range s.cfg.PublicPaths {
		if p != "" && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Middleware implements kernel.Authenticator. It accepts, in order:
//
//  1. Authorization: Bearer <dev_token> (dev mode only),
//  2. Authorization: Bearer <node token> (+ X-Nexus-Node-Id) — static
//     config node_tokens or tokens issued by RedeemNodePairing,
//  3. Authorization: Bearer <session token> from DeviceService.Authenticate,
//  4. a verified node client certificate (mTLS) without a bearer token.
//
// A verified client certificate must belong to the same principal as the
// token. Requests carrying X-Nexus-Signature are verified (Layer 3) and must
// be signed by the session's device; with require_signed_requests, device
// sessions must sign every request. The Principal is available to handlers
// via PrincipalFromContext.
func (s *Security) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Public(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.authenticate(r)
		if err != nil {
			s.record("auth.failed", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr, "err": err.Error()})
			requestsig.WriteError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
			return
		}
		if requestsig.Present(r) || (s.cfg.RequireSignedRequests && p.Kind == PrincipalDevice) {
			dev, err := s.reqsig.Verify(r)
			if err == nil && p.Kind == PrincipalDevice && dev != p.ID {
				err = errors.New("request signed by a different device than the session")
			}
			if err == nil && p.Kind == PrincipalNode {
				err = errors.New("nodes cannot send device-signed requests")
			}
			if err != nil {
				s.record("auth.bad_signature", map[string]any{"path": r.URL.Path, "principal": p.ID, "err": err.Error()})
				status := http.StatusUnauthorized
				if errors.Is(err, requestsig.ErrTooLarge) {
					status = http.StatusRequestEntityTooLarge
				}
				requestsig.WriteError(w, status, "unauthenticated", err.Error())
				return
			}
			p.Signed = true
			if p.Kind == PrincipalDev {
				p.ID = dev
			}
			r = r.WithContext(requestsig.WithDeviceID(r.Context(), dev))
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func ctEq(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (s *Security) authenticate(r *http.Request) (Principal, error) {
	certKind, certID, hasCert := pki.PeerIdentity(r)
	if hasCert && s.certRevoked(r.TLS.PeerCertificates[0]) {
		return Principal{}, errors.New("client certificate revoked")
	}
	tok := bearer(r)
	var p Principal
	switch {
	case tok == "":
		if hasCert && certKind == pki.KindNode {
			return Principal{Kind: PrincipalNode, ID: certID, CertVerified: true}, nil
		}
		return Principal{}, errors.New("missing bearer token")
	case s.cfg.DevToken != "" && ctEq(tok, s.cfg.DevToken):
		p = Principal{Kind: PrincipalDev}
	default:
		matched := false
		nodeID := r.Header.Get(HeaderNodeID)
		for _, nt := range s.cfg.NodeTokens {
			id, t, ok := strings.Cut(nt, ":")
			if ok && id == nodeID && t != "" && ctEq(tok, t) {
				p = Principal{Kind: PrincipalNode, ID: nodeID}
				matched = true
			}
		}
		if !matched {
			var err error
			p, err = s.lookupToken(r.Context(), tok, r.Header.Get(HeaderNodeID))
			if err != nil {
				return Principal{}, err
			}
		}
	}
	if hasCert {
		wantKind := map[string]string{PrincipalDevice: pki.KindDevice, PrincipalNode: pki.KindNode}[p.Kind]
		if p.Kind != PrincipalDev && (certKind != wantKind || (p.ID != "" && certID != p.ID)) {
			return Principal{}, errors.New("client certificate does not match token")
		}
		if p.ID == "" {
			p.ID = certID
		}
		p.CertVerified = true
	}
	return p, nil
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func (s *Security) lookupToken(ctx context.Context, tok, nodeHeader string) (Principal, error) {
	h := hashToken(tok)
	var devID string
	err := s.db.QueryRowContext(ctx, `
		SELECT s.device_id FROM security_sessions s
		JOIN security_devices d ON d.id = s.device_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND d.revoked_at = 0`,
		h, s.now().UnixNano()).Scan(&devID)
	if err == nil {
		return Principal{Kind: PrincipalDevice, ID: devID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Principal{}, err
	}
	var nodeID string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM security_nodes WHERE token_hash = ? AND revoked_at = 0`, h).Scan(&nodeID)
	if err == nil {
		if nodeHeader != "" && nodeHeader != nodeID {
			return Principal{}, errors.New("node token does not match X-Nexus-Node-Id")
		}
		return Principal{Kind: PrincipalNode, ID: nodeID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Principal{}, err
	}
	return Principal{}, errors.New("invalid or expired token")
}

func newToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------- key lookups ----------

// ApprovalKey implements approval.KeyLookup from the hub's device table
// (revoked devices fail with approval.ErrRevoked).
func (s *Security) ApprovalKey(ctx context.Context, deviceID string) (*ecdsa.PublicKey, error) {
	var key []byte
	var revokedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT approval_public_key, revoked_at FROM security_devices WHERE id = ?`, deviceID).Scan(&key, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, approval.ErrUnknownDevice
	}
	if err != nil {
		return nil, err
	}
	if revokedAt != 0 {
		return nil, approval.ErrRevoked
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: device has no approval key", approval.ErrUnknownDevice)
	}
	return keys.ParsePublicKey(key)
}

// AuthKey returns a non-revoked device's auth (request-signing) key.
func (s *Security) AuthKey(ctx context.Context, deviceID string) (*ecdsa.PublicKey, error) {
	var key []byte
	var revokedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT public_key, revoked_at FROM security_devices WHERE id = ?`, deviceID).Scan(&key, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("unknown device")
	}
	if err != nil {
		return nil, err
	}
	if revokedAt != 0 {
		return nil, errors.New("device revoked")
	}
	return keys.ParsePublicKey(key)
}

// VerifyApproval verifies a device-signed decision for a pending approval:
// approval id, challenge and payload hash must match, neither the approval
// nor the signed decision may be expired, and the signing device must be
// paired, not revoked, and its approval key must verify the signature.
// A valid denial also returns nil; check d.Approve.
func (s *Security) VerifyApproval(ctx context.Context, d *nexusv1.SignedApproval, a *nexusv1.Approval) error {
	err := approval.VerifyFor(ctx, d, a, s, s.now())
	if err != nil {
		s.alert("approval.verify_failed", "Approval verification failed",
			"A signed approval was rejected: "+err.Error(),
			map[string]any{"approval_id": a.GetId(), "device_id": d.GetDeviceId(), "err": err.Error()})
		return err
	}
	s.record("approval.verified", map[string]any{"approval_id": a.GetId(), "device_id": d.GetDeviceId(), "approve": d.GetApprove()})
	return nil
}

// ---------- pairing (CLI hooks) ----------

// PairingOptions configure NewPairing.
type PairingOptions struct {
	// Kind is pairing.KindDevice (default) or pairing.KindNode.
	Kind string
	// Label is a free-form note shown in the audit log.
	Label string
	// HubURLs override security.hub_urls for this QR.
	HubURLs []string
}

// NewPairing enables pairing for 5 minutes by issuing a one-time secret and
// returns the QR payload. Render it with qr.URL() (nexus://pair?...) or
// qr.Base64(). The secret is persisted (hashed) in the hub database, so a
// CLI process sharing the data dir/database can call this while nexusd runs.
//
//	sec, _ := kernel.Resolve[*security.Security](proc, kernel.SvcSecurity)
//	qr, err := sec.NewPairing(ctx, security.PairingOptions{Kind: pairing.KindDevice})
//	fmt.Println(qr.URL())  // render as a QR code in the terminal
func (s *Security) NewPairing(ctx context.Context, o PairingOptions) (*pairing.QR, error) {
	if o.Kind == "" {
		o.Kind = pairing.KindDevice
	}
	urls := o.HubURLs
	if len(urls) == 0 {
		urls = s.cfg.HubURLs
	}
	if len(urls) == 0 {
		return nil, errors.New("security: no hub URL (set security.hub_urls or PairingOptions.HubURLs)")
	}
	tok, exp, err := s.pairing.New(ctx, o.Kind, o.Label)
	if err != nil {
		return nil, err
	}
	s.alert("pairing.enabled", "Pairing enabled",
		fmt.Sprintf("A %s pairing code was issued (valid until %s).", o.Kind, exp.Format(time.Kitchen)),
		map[string]any{"kind": o.Kind, "label": o.Label, "expires": exp})
	return &pairing.QR{
		Version:       1,
		Kind:          o.Kind,
		HubURLs:       urls,
		CAFingerprint: s.ca.Fingerprint(),
		Token:         tok,
		HubKey:        base64.RawURLEncoding.EncodeToString(s.HubPublicKey()),
		Expires:       exp.Unix(),
	}, nil
}

// NodePairing is the result of RedeemNodePairing.
type NodePairing struct {
	NodeID     string
	Token      string // bearer token for the node (shown once; stored hashed)
	ClientCert []byte // PEM mTLS client certificate for identityPub
	CACert     []byte // PEM Hub CA certificate
	SafetyCode string
}

// RedeemNodePairing completes node pairing (called by the nodelink/node
// service with what `nexus-node pair` sends). proof = HMAC-SHA256(
// HKDF(secret,"nexus/v1/pairing"), identityPub). Owner enrolment of the node
// key (OwnerStatement KIND_ENROL_NODE, signed on the phone) is separate.
func (s *Security) RedeemNodePairing(ctx context.Context, token, nodeID string, identityPub, proof []byte) (*NodePairing, error) {
	secret, _, err := s.pairing.Redeem(ctx, token, pairing.KindNode)
	if err != nil {
		s.record("pairing.failed", map[string]any{"kind": "node", "err": err.Error()})
		return nil, err
	}
	if err := pairing.VerifyProof(secret, identityPub, nil, nil, proof); err != nil {
		s.alert("pairing.failed", "Node pairing failed", "Bad pairing proof.", map[string]any{"kind": "node", "node_id": nodeID})
		return nil, err
	}
	pub, err := keys.ParsePublicKey(identityPub)
	if err != nil {
		return nil, err
	}
	if nodeID == "" {
		nodeID = newID("node_")
	}
	tok, err := newToken("nxn_")
	if err != nil {
		return nil, err
	}
	der, err := s.ca.IssueClientCert(pub, pki.ClientCertOptions{ID: nodeID, Kind: pki.KindNode})
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO security_nodes (id, public_key, token_hash, paired_at, revoked_at) VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(id) DO UPDATE SET public_key = excluded.public_key, token_hash = excluded.token_hash,
			paired_at = excluded.paired_at, revoked_at = 0`,
		nodeID, keys.MarshalPublicKey(pub), hashToken(tok), s.now().UnixNano())
	if err != nil {
		return nil, err
	}
	code := pairing.SafetyCode(s.HubPublicKey(), keys.MarshalPublicKey(pub))
	s.alert("node.paired", "New node paired", "Node "+nodeID+" was paired with the hub.",
		map[string]any{"node_id": nodeID, "key_id": keys.KeyID(pub)})
	return &NodePairing{NodeID: nodeID, Token: tok, ClientCert: pki.EncodeCertPEM(der), CACert: s.ca.CertPEM, SafetyCode: code}, nil
}

// AuthenticateNode checks a node's bearer token (used by the nodelink module,
// which then ignores its own static token map): accepted are security.node_tokens
// (any node id) and tokens issued by RedeemNodePairing for exactly nodeID.
func (s *Security) AuthenticateNode(ctx context.Context, nodeID, token string) error {
	if nodeID == "" || token == "" {
		return errors.New("missing node id or token")
	}
	for _, nt := range s.cfg.NodeTokens {
		// Entries are "node-id:token" so a token only authenticates its own
		// node; bare tokens (legacy) are rejected.
		id, tok, ok := strings.Cut(nt, ":")
		if ok && id == nodeID && tok != "" && ctEq(token, tok) {
			return nil
		}
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM security_nodes WHERE token_hash = ? AND revoked_at = 0`, hashToken(token)).Scan(&id)
	if err != nil || id != nodeID {
		s.record("auth.failed", map[string]any{"node_id": nodeID, "reason": "node token"})
		return errors.New("invalid node credentials")
	}
	return nil
}

// RevokeNode revokes a paired node's token and certificate.
func (s *Security) RevokeNode(ctx context.Context, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE security_nodes SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, s.now().UnixNano(), nodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("security: unknown or already revoked node")
	}
	s.alert("node.revoked", "Node revoked", "Node "+nodeID+" was revoked.", map[string]any{"node_id": nodeID})
	return nil
}

// ---------- device queries for other modules (notifier) ----------

// Devices returns all non-revoked devices (with push tokens).
func (s *Security) Devices(ctx context.Context) ([]*nexusv1.Device, error) {
	return s.listDevices(ctx)
}

// LiveActivityToken is a registered ActivityKit push token.
type LiveActivityToken struct {
	DeviceID     string
	Kind         nexusv1.LiveActivityTokenKind
	ActivityType string
	ActivityID   string
	TaskID       string
	Token        []byte
	Environment  string
	UpdatedAt    time.Time
}

// LiveActivityTokens lists tokens of non-revoked devices, optionally
// filtered by kind (UNSPECIFIED = all) and activity type ("" = all).
func (s *Security) LiveActivityTokens(ctx context.Context, kind nexusv1.LiveActivityTokenKind, activityType string) ([]LiveActivityToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.device_id, t.kind, t.activity_type, t.activity_id, t.task_id, t.token, t.environment, t.updated_at
		FROM security_live_activity_tokens t JOIN security_devices d ON d.id = t.device_id
		WHERE d.revoked_at = 0 AND (? = 0 OR t.kind = ?) AND (? = '' OR t.activity_type = ?)
		ORDER BY t.updated_at`, int32(kind), int32(kind), activityType, activityType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveActivityToken
	for rows.Next() {
		var t LiveActivityToken
		var k int32
		var at int64
		if err := rows.Scan(&t.DeviceID, &k, &t.ActivityType, &t.ActivityID, &t.TaskID, &t.Token, &t.Environment, &at); err != nil {
			return nil, err
		}
		t.Kind = nexusv1.LiveActivityTokenKind(k)
		t.UpdatedAt = time.Unix(0, at)
		out = append(out, t)
	}
	return out, rows.Err()
}
