package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

// AuthenticateTag is the domain tag of the challenge signature.
//
// Authenticate signed message (clients MUST match):
//
//	M = ASCII "nexus/v1/authenticate" || 0x00
//	    || u32be(len(device_id)) || device_id
//	    || u32be(len(challenge)) || challenge      (32 bytes from GetChallenge)
//	signature = ECDSA P-256 / SHA-256 over M with the device *auth* key.
const AuthenticateTag = "nexus/v1/authenticate"

// ChallengeTTL bounds how long a GetChallenge result can be used.
const ChallengeTTL = 2 * time.Minute

const maxChallenges = 10000

// AuthenticateMessage returns the bytes a device signs in Authenticate.
func AuthenticateMessage(deviceID string, challenge []byte) []byte {
	return keys.NewBuilder(AuthenticateTag).String(deviceID).Bytes(challenge).Message()
}

func newID(prefix string) string { return prefix + ulid.Make().String() }

type deviceService struct {
	s *Security
}

var _ nexusv1connect.DeviceServiceHandler = (*deviceService)(nil)

func cerr(code connect.Code, format string, a ...any) error {
	return connect.NewError(code, fmt.Errorf(format, a...))
}

// Pair registers a device using a one-time pairing token from the QR.
func (d *deviceService) Pair(ctx context.Context, req *connect.Request[nexusv1.PairRequest]) (*connect.Response[nexusv1.PairResponse], error) {
	s := d.s
	m := req.Msg
	authPub, err := keys.ParsePublicKey(m.PublicKey)
	if err != nil {
		return nil, cerr(connect.CodeInvalidArgument, "public_key: %v", err)
	}
	var approvalPub []byte
	if len(m.ApprovalPublicKey) > 0 {
		if approvalPub, err = keys.CanonicalPublicKey(m.ApprovalPublicKey); err != nil {
			return nil, cerr(connect.CodeInvalidArgument, "approval_public_key: %v", err)
		}
	}
	if len(m.EncryptionPublicKey) != 0 && len(m.EncryptionPublicKey) != 32 {
		return nil, cerr(connect.CodeInvalidArgument, "encryption_public_key must be 32-byte X25519")
	}
	secret, pend, err := s.pairing.Redeem(ctx, m.PairingToken, pairing.KindDevice)
	if err != nil {
		s.record("pairing.failed", map[string]any{"kind": "device", "err": err.Error()})
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err := pairing.VerifyProof(secret, m.PublicKey, m.ApprovalPublicKey, m.EncryptionPublicKey, m.PairingProof); err != nil {
		s.alert("pairing.failed", "Device pairing failed", "A pairing attempt presented a bad proof.",
			map[string]any{"kind": "device", "name": m.Name})
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err := d.checkAttestation(ctx, m); err != nil {
		s.alert("pairing.failed", "Device pairing failed", "App Attest verification failed.",
			map[string]any{"kind": "device", "name": m.Name, "err": err.Error()})
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	authBytes := keys.MarshalPublicKey(authPub)
	id := newID("dev_")
	now := s.now()
	kind := m.Kind
	if kind == nexusv1.DeviceKind_DEVICE_KIND_UNSPECIFIED {
		kind = nexusv1.DeviceKind_DEVICE_KIND_IOS
	}
	// The first (non-revoked) device becomes primary atomically.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO security_devices (id, name, kind, public_key, approval_public_key, encryption_public_key,
			is_primary, app_attest_key_id, paired_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, NOT EXISTS (SELECT 1 FROM security_devices WHERE revoked_at = 0), ?, ?, ?)`,
		id, m.Name, int32(kind), authBytes, nullBytes(approvalPub), nullBytes(m.EncryptionPublicKey),
		m.AppAttestKeyId, now.UnixNano(), now.UnixNano())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	der, err := s.ca.IssueClientCert(authPub, pki.ClientCertOptions{ID: id, Kind: pki.KindDevice})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	dev, err := s.getDevice(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	code := pairing.SafetyCode(s.HubPublicKey(), authBytes, approvalPub, m.EncryptionPublicKey)
	s.alert("device.paired", "New device paired",
		fmt.Sprintf("%q was paired with your hub. If this wasn't you, revoke it now.", m.Name),
		map[string]any{"device_id": id, "name": m.Name, "kind": kind.String(), "primary": dev.Primary,
			"auth_key_id": keys.KeyIDBytes(authBytes), "label": pend.Label})
	s.record("cert.client_issued", map[string]any{"device_id": id})
	return connect.NewResponse(&nexusv1.PairResponse{
		Device:            dev,
		SafetyCode:        code,
		ClientCertificate: pki.EncodeCertPEM(der),
	}), nil
}

func (d *deviceService) checkAttestation(ctx context.Context, m *nexusv1.PairRequest) error {
	s := d.s
	s.attestMu.RLock()
	v := s.attest
	s.attestMu.RUnlock()
	_, isDev := v.(devAppAttest)
	if len(m.AppAttestation) == 0 {
		if s.cfg.RequireAppAttest && (m.Kind == nexusv1.DeviceKind_DEVICE_KIND_IOS || m.Kind == nexusv1.DeviceKind_DEVICE_KIND_UNSPECIFIED) {
			return errors.New("app attestation required")
		}
		return nil
	}
	if isDev {
		if s.cfg.RequireAppAttest {
			return errors.New("app attestation required but no verifier is installed")
		}
		s.log.Warn("security: App Attest not verified (TODO: install an AppAttestVerifier); accepting in dev")
		return nil
	}
	h := sha256.New()
	h.Write(m.PublicKey)
	h.Write(m.ApprovalPublicKey)
	h.Write(m.EncryptionPublicKey)
	return v.VerifyAttestation(ctx, m.AppAttestKeyId, m.AppAttestation, h.Sum(nil))
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// GetChallenge returns a 32-byte single-use challenge (also for unknown
// devices, to avoid device enumeration).
func (d *deviceService) GetChallenge(ctx context.Context, req *connect.Request[nexusv1.GetChallengeRequest]) (*connect.Response[nexusv1.GetChallengeResponse], error) {
	if req.Msg.DeviceId == "" {
		return nil, cerr(connect.CodeInvalidArgument, "device_id required")
	}
	c := make([]byte, 32)
	if _, err := rand.Read(c); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s := d.s
	now := s.now()
	s.chalMu.Lock()
	if len(s.chal) >= maxChallenges {
		for k, v := range s.chal {
			if !now.Before(v.expires) {
				delete(s.chal, k)
			}
		}
	}
	if len(s.chal) >= maxChallenges {
		s.chalMu.Unlock()
		return nil, cerr(connect.CodeResourceExhausted, "too many pending challenges")
	}
	s.chal[string(c)] = challenge{deviceID: req.Msg.DeviceId, expires: now.Add(ChallengeTTL)}
	s.chalMu.Unlock()
	return connect.NewResponse(&nexusv1.GetChallengeResponse{Challenge: c}), nil
}

// Authenticate exchanges a signed challenge for a session access token.
func (d *deviceService) Authenticate(ctx context.Context, req *connect.Request[nexusv1.AuthenticateRequest]) (*connect.Response[nexusv1.AuthenticateResponse], error) {
	s := d.s
	m := req.Msg
	now := s.now()
	s.chalMu.Lock()
	c, ok := s.chal[string(m.Challenge)]
	delete(s.chal, string(m.Challenge)) // single use
	s.chalMu.Unlock()
	fail := func(reason string) error {
		s.record("auth.failed", map[string]any{"device_id": m.DeviceId, "reason": reason})
		return cerr(connect.CodeUnauthenticated, "authentication failed")
	}
	if !ok || c.deviceID != m.DeviceId || !now.Before(c.expires) {
		return nil, fail("unknown or expired challenge")
	}
	pub, err := s.AuthKey(ctx, m.DeviceId)
	if err != nil {
		return nil, fail(err.Error())
	}
	if !keys.Verify(pub, AuthenticateMessage(m.DeviceId, m.Challenge), m.Signature) {
		s.alert("auth.bad_signature", "Failed device authentication",
			"A device authentication signature failed to verify.", map[string]any{"device_id": m.DeviceId})
		return nil, cerr(connect.CodeUnauthenticated, "authentication failed")
	}
	tok, err := newToken("nxs_")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	exp := now.Add(s.cfg.SessionTTL)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM security_sessions WHERE expires_at <= ?`, now.UnixNano()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO security_sessions (token_hash, device_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(tok), m.DeviceId, exp.UnixNano()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE security_devices SET last_seen = ? WHERE id = ?`, now.UnixNano(), m.DeviceId)
	return connect.NewResponse(&nexusv1.AuthenticateResponse{AccessToken: tok, ExpiresAt: timestamppb.New(exp)}), nil
}

// callerDevice requires a device principal.
func callerDevice(ctx context.Context) (string, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", cerr(connect.CodeUnauthenticated, "not authenticated")
	}
	if p.Kind != PrincipalDevice || p.ID == "" {
		return "", cerr(connect.CodePermissionDenied, "a paired device session is required")
	}
	return p.ID, nil
}

// requireUser allows devices and the dev token, not nodes.
func requireUser(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return p, cerr(connect.CodeUnauthenticated, "not authenticated")
	}
	if p.Kind != PrincipalDevice && p.Kind != PrincipalDev {
		return p, cerr(connect.CodePermissionDenied, "not allowed for %s principals", p.Kind)
	}
	return p, nil
}

func (d *deviceService) RegisterPushToken(ctx context.Context, req *connect.Request[nexusv1.RegisterPushTokenRequest]) (*connect.Response[nexusv1.RegisterPushTokenResponse], error) {
	id, err := callerDevice(ctx)
	if err != nil {
		return nil, err
	}
	env := req.Msg.Environment
	if env != "" && env != "sandbox" && env != "production" {
		return nil, cerr(connect.CodeInvalidArgument, "environment must be sandbox or production")
	}
	if _, err := d.s.db.ExecContext(ctx, `UPDATE security_devices SET push_token = ?, push_environment = ? WHERE id = ?`,
		req.Msg.PushToken, env, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&nexusv1.RegisterPushTokenResponse{}), nil
}

func (d *deviceService) RegisterLiveActivityToken(ctx context.Context, req *connect.Request[nexusv1.RegisterLiveActivityTokenRequest]) (*connect.Response[nexusv1.RegisterLiveActivityTokenResponse], error) {
	id, err := callerDevice(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	switch m.Kind {
	case nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_PUSH_TO_START:
	case nexusv1.LiveActivityTokenKind_LIVE_ACTIVITY_TOKEN_KIND_UPDATE:
		if m.ActivityId == "" {
			return nil, cerr(connect.CodeInvalidArgument, "activity_id required for UPDATE tokens")
		}
	default:
		return nil, cerr(connect.CodeInvalidArgument, "kind required")
	}
	if len(m.Token) == 0 {
		return nil, cerr(connect.CodeInvalidArgument, "token required")
	}
	_, err = d.s.db.ExecContext(ctx, `
		INSERT INTO security_live_activity_tokens (device_id, kind, activity_type, activity_id, task_id, token, environment, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, kind, activity_type, activity_id) DO UPDATE SET
			task_id = excluded.task_id, token = excluded.token, environment = excluded.environment, updated_at = excluded.updated_at`,
		id, int32(m.Kind), m.ActivityType, m.ActivityId, m.TaskId, m.Token, m.Environment, d.s.now().UnixNano())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&nexusv1.RegisterLiveActivityTokenResponse{}), nil
}

func (d *deviceService) ListDevices(ctx context.Context, _ *connect.Request[nexusv1.ListDevicesRequest]) (*connect.Response[nexusv1.ListDevicesResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	devs, err := d.s.listDevices(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&nexusv1.ListDevicesResponse{Devices: devs}), nil
}

// RevokeDevice revokes a device immediately: sessions deleted, approvals and
// signed requests from its keys rejected, its client certificate refused.
//
// TODO(security): require an owner-signed REVOKE_KEY statement / Face ID
// approval from another device and propagate it to Nodes.
func (d *deviceService) RevokeDevice(ctx context.Context, req *connect.Request[nexusv1.RevokeDeviceRequest]) (*connect.Response[nexusv1.RevokeDeviceResponse], error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.s.RevokeDevice(ctx, req.Msg.Id, p); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&nexusv1.RevokeDeviceResponse{}), nil
}

var errNotFound = errors.New("security: device not found or already revoked")

// RevokeDevice revokes a device (also used by the kill switch / CLI).
func (s *Security) RevokeDevice(ctx context.Context, id string, by Principal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE security_devices SET revoked_at = ?, push_token = '', is_primary = 0 WHERE id = ? AND revoked_at = 0`,
		s.now().UnixNano(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM security_sessions WHERE device_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM security_live_activity_tokens WHERE device_id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.guard.Forget(id)
	s.alert("device.revoked", "Device revoked", "Device "+id+" was revoked.",
		map[string]any{"device_id": id, "by_kind": by.Kind, "by_id": by.ID})
	return nil
}

const deviceCols = `id, name, kind, public_key, approval_public_key, encryption_public_key, push_token, is_primary, paired_at, last_seen`

func scanDevice(sc interface{ Scan(...any) error }) (*nexusv1.Device, error) {
	var d nexusv1.Device
	var kind int32
	var primary int
	var paired, seen int64
	if err := sc.Scan(&d.Id, &d.Name, &kind, &d.PublicKey, &d.ApprovalPublicKey, &d.EncryptionPublicKey, &d.PushToken, &primary, &paired, &seen); err != nil {
		return nil, err
	}
	d.Kind = nexusv1.DeviceKind(kind)
	d.Primary = primary != 0
	d.PairedAt = timestamppb.New(time.Unix(0, paired))
	if seen != 0 {
		d.LastSeen = timestamppb.New(time.Unix(0, seen))
	}
	return &d, nil
}

func (s *Security) getDevice(ctx context.Context, id string) (*nexusv1.Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM security_devices WHERE id = ?`, id))
}

func (s *Security) listDevices(ctx context.Context) ([]*nexusv1.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM security_devices WHERE revoked_at = 0 ORDER BY paired_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nexusv1.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// sqlPairingStore persists pending pairings (hashed secrets) in the hub DB.
type sqlPairingStore struct{ db *sql.DB }

func (p *sqlPairingStore) Put(ctx context.Context, h []byte, pend pairing.Pending) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO security_pairings (secret_hash, kind, label, expires_at) VALUES (?, ?, ?, ?)`,
		h, pend.Kind, pend.Label, pend.ExpiresAt.UnixNano())
	return err
}

func (p *sqlPairingStore) Take(ctx context.Context, h []byte, now time.Time) (pairing.Pending, bool, error) {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM security_pairings WHERE expires_at <= ?`, now.UnixNano()); err != nil {
		return pairing.Pending{}, false, err
	}
	var pend pairing.Pending
	var exp int64
	err := p.db.QueryRowContext(ctx, `DELETE FROM security_pairings WHERE secret_hash = ? RETURNING kind, label, expires_at`, h).
		Scan(&pend.Kind, &pend.Label, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return pairing.Pending{}, false, nil
	}
	if err != nil {
		return pairing.Pending{}, false, err
	}
	pend.ExpiresAt = time.Unix(0, exp)
	return pend, true, nil
}
