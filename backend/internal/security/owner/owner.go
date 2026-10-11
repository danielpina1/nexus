// Package owner implements owner-root-signed statements (nexusv1.OwnerStatement)
// and the TrustStore that Nodes and the Hub keep of owner-signed authority
// (SECURITY.md §2, Layer 5, §6; ADR-0008).
//
// # Signed message (clients MUST match byte for byte)
//
//	M = ASCII "nexus/v1/owner-statement" || 0x00
//	    || u32be(kind)                                      (OwnerStatement.Kind enum value)
//	    || u64be(sequence)
//	    || i64be(issued_at.seconds)                         (nanos ignored, not signed)
//	    || u32be(len(subject_public_key)) || subject_public_key
//	    || u32be(len(subject_id))         || subject_id     (UTF-8)
//	    || u32be(len(policy_sha256))      || policy_sha256
//	    || u32be(len(payload))            || payload
//	signature = ECDSA P-256 / SHA-256 over M (digest SHA256(M)) with the owner
//	            root key. DER preferred; raw 64-byte r||s accepted.
//
// # Statement semantics in TrustStore.Apply
//
//	ENROL_DEVICE   subject_id = device id, subject_public_key = the device's
//	               *approval* public key (P-256); payload opaque (stored).
//	ENROL_NODE     subject_id = node id, subject_public_key = node identity key.
//	REVOKE_KEY     subject_public_key and/or subject_id; the key (or the
//	               principal's current key) is revoked forever and the
//	               principal removed. A revoked key can never be re-enrolled.
//	POLICY         policy_sha256 (32 bytes) becomes the last owner-signed policy.
//	APPROVE_UPDATE payload (e.g. release digest) recorded as last approved update.
//	LOCK / UNLOCK  kill switch state.
//
// sequence must be strictly greater than the last applied sequence
// (rollback / replay protection).
package owner

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/approval"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// Tag is the domain-separation prefix.
const Tag = "nexus/v1/owner-statement"

// Errors.
var (
	ErrBadSignature = errors.New("owner: bad owner signature")
	ErrRollback     = errors.New("owner: sequence not greater than last applied (rollback)")
	ErrMalformed    = errors.New("owner: malformed statement")
	ErrRevokedKey   = errors.New("owner: key is revoked")
)

// Message returns the exact signed bytes (see package doc).
func Message(st *nexusv1.OwnerStatement) []byte {
	var secs int64
	if st.GetIssuedAt() != nil {
		secs = st.GetIssuedAt().GetSeconds()
	}
	return keys.NewBuilder(Tag).
		U32(uint32(st.GetKind())).
		U64(st.GetSequence()).
		I64(secs).
		Bytes(st.GetSubjectPublicKey()).
		String(st.GetSubjectId()).
		Bytes(st.GetPolicySha256()).
		Bytes(st.GetPayload()).
		Message()
}

// Sign fills st.Signature with the owner root key.
func Sign(st *nexusv1.OwnerStatement, root crypto.Signer) error {
	sig, err := keys.Sign(root, Message(st))
	if err != nil {
		return err
	}
	st.Signature = sig
	return nil
}

// Verify checks st's signature against the owner root public key.
func Verify(st *nexusv1.OwnerStatement, root *ecdsa.PublicKey) error {
	if st == nil || len(st.Signature) == 0 {
		return ErrMalformed
	}
	if !keys.Verify(root, Message(st), st.Signature) {
		return ErrBadSignature
	}
	return nil
}

// Principal is an enrolled device or node.
type Principal struct {
	Kind        string `json:"kind"` // "device" | "node"
	ID          string `json:"id"`
	PublicKey   []byte `json:"public_key"` // 65-byte P-256
	EnrolledSeq uint64 `json:"enrolled_seq"`
	Payload     []byte `json:"payload,omitempty"`
}

type state struct {
	Version      int                   `json:"version"`
	OwnerRoot    []byte                `json:"owner_root"`
	Sequence     uint64                `json:"sequence"`
	PolicySHA256 []byte                `json:"policy_sha256,omitempty"`
	LastUpdate   []byte                `json:"last_update,omitempty"`
	Locked       bool                  `json:"locked"`
	Principals   map[string]*Principal `json:"principals"` // key: kind/id
	Revoked      map[string]uint64     `json:"revoked"`    // key id → sequence
}

// TrustStore holds the owner root public key and everything it has signed.
// Safe for concurrent use. When created with a path, every successful Apply
// is persisted atomically (0600) before it returns.
type TrustStore struct {
	mu   sync.RWMutex
	path string
	root *ecdsa.PublicKey
	s    state
}

// NewTrustStore creates an empty store trusting root. path may be "".
func NewTrustStore(root *ecdsa.PublicKey, path string) *TrustStore {
	return &TrustStore{path: path, root: root, s: state{
		Version:    1,
		OwnerRoot:  keys.MarshalPublicKey(root),
		Principals: map[string]*Principal{},
		Revoked:    map[string]uint64{},
	}}
}

// LoadTrustStore reads a store persisted by Apply.
func LoadTrustStore(path string) (*TrustStore, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("owner: %s: %w", path, err)
	}
	root, err := keys.ParsePublicKey(s.OwnerRoot)
	if err != nil {
		return nil, fmt.Errorf("owner: %s: owner root: %w", path, err)
	}
	if s.Principals == nil {
		s.Principals = map[string]*Principal{}
	}
	if s.Revoked == nil {
		s.Revoked = map[string]uint64{}
	}
	return &TrustStore{path: path, root: root, s: s}, nil
}

// Save persists the store (Apply does this automatically).
func (t *TrustStore) Save() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.saveLocked()
}

func (t *TrustStore) saveLocked() error {
	if t.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(&t.s, "", "  ")
	if err != nil {
		return err
	}
	return keys.WriteFileAtomic(t.path, b, 0o600)
}

func pkey(kind, id string) string { return kind + "/" + id }

// Apply verifies st and applies it. On any error the store is unchanged.
func (t *TrustStore) Apply(st *nexusv1.OwnerStatement) error {
	if err := Verify(st, t.root); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if st.Sequence <= t.s.Sequence {
		return fmt.Errorf("%w (got %d, last %d)", ErrRollback, st.Sequence, t.s.Sequence)
	}
	prev := cloneState(t.s)
	if err := t.applyLocked(st); err != nil {
		t.s = prev
		return err
	}
	t.s.Sequence = st.Sequence
	if err := t.saveLocked(); err != nil {
		t.s = prev
		return fmt.Errorf("owner: persist: %w", err)
	}
	return nil
}

func (t *TrustStore) applyLocked(st *nexusv1.OwnerStatement) error {
	switch st.Kind {
	case nexusv1.OwnerStatement_KIND_ENROL_DEVICE, nexusv1.OwnerStatement_KIND_ENROL_NODE:
		kind := "device"
		if st.Kind == nexusv1.OwnerStatement_KIND_ENROL_NODE {
			kind = "node"
		}
		if st.SubjectId == "" {
			return fmt.Errorf("%w: subject_id required", ErrMalformed)
		}
		pub, err := keys.CanonicalPublicKey(st.SubjectPublicKey)
		if err != nil {
			return fmt.Errorf("%w: subject key: %v", ErrMalformed, err)
		}
		if _, revoked := t.s.Revoked[keys.KeyIDBytes(pub)]; revoked {
			return ErrRevokedKey
		}
		t.s.Principals[pkey(kind, st.SubjectId)] = &Principal{
			Kind: kind, ID: st.SubjectId, PublicKey: pub, EnrolledSeq: st.Sequence,
			Payload: append([]byte(nil), st.Payload...),
		}
	case nexusv1.OwnerStatement_KIND_REVOKE_KEY:
		if len(st.SubjectPublicKey) == 0 && st.SubjectId == "" {
			return fmt.Errorf("%w: revoke needs a key or an id", ErrMalformed)
		}
		if len(st.SubjectPublicKey) > 0 {
			pub, err := keys.CanonicalPublicKey(st.SubjectPublicKey)
			if err != nil {
				return fmt.Errorf("%w: subject key: %v", ErrMalformed, err)
			}
			kid := keys.KeyIDBytes(pub)
			t.s.Revoked[kid] = st.Sequence
			for k, p := range t.s.Principals {
				if keys.KeyIDBytes(p.PublicKey) == kid {
					delete(t.s.Principals, k)
				}
			}
		}
		if st.SubjectId != "" {
			for _, kind := range []string{"device", "node"} {
				if p, ok := t.s.Principals[pkey(kind, st.SubjectId)]; ok {
					t.s.Revoked[keys.KeyIDBytes(p.PublicKey)] = st.Sequence
					delete(t.s.Principals, pkey(kind, st.SubjectId))
				}
			}
		}
	case nexusv1.OwnerStatement_KIND_POLICY:
		if len(st.PolicySha256) != 32 {
			return fmt.Errorf("%w: policy_sha256 must be 32 bytes", ErrMalformed)
		}
		t.s.PolicySHA256 = append([]byte(nil), st.PolicySha256...)
	case nexusv1.OwnerStatement_KIND_APPROVE_UPDATE:
		if len(st.Payload) == 0 {
			return fmt.Errorf("%w: update digest required in payload", ErrMalformed)
		}
		t.s.LastUpdate = append([]byte(nil), st.Payload...)
	case nexusv1.OwnerStatement_KIND_LOCK:
		t.s.Locked = true
	case nexusv1.OwnerStatement_KIND_UNLOCK:
		t.s.Locked = false
	default:
		return fmt.Errorf("%w: unknown kind %v", ErrMalformed, st.Kind)
	}
	return nil
}

func cloneState(s state) state {
	c := s
	c.Principals = make(map[string]*Principal, len(s.Principals))
	for k, v := range s.Principals {
		cp := *v
		c.Principals[k] = &cp
	}
	c.Revoked = make(map[string]uint64, len(s.Revoked))
	for k, v := range s.Revoked {
		c.Revoked[k] = v
	}
	return c
}

// OwnerRoot returns the owner root public key.
func (t *TrustStore) OwnerRoot() *ecdsa.PublicKey { return t.root }

// Sequence is the last applied statement sequence.
func (t *TrustStore) Sequence() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.s.Sequence
}

// PolicySHA256 is the hash of the last owner-signed policy (nil if none).
func (t *TrustStore) PolicySHA256() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]byte(nil), t.s.PolicySHA256...)
}

// LastApprovedUpdate is the payload of the last APPROVE_UPDATE.
func (t *TrustStore) LastApprovedUpdate() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]byte(nil), t.s.LastUpdate...)
}

// Locked reports whether the kill switch is engaged.
func (t *TrustStore) Locked() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.s.Locked
}

// Principal returns an enrolled device ("device") or node ("node").
func (t *TrustStore) Principal(kind, id string) (Principal, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p, ok := t.s.Principals[pkey(kind, id)]
	if !ok {
		return Principal{}, false
	}
	return *p, true
}

// Principals lists all enrolled principals.
func (t *TrustStore) Principals() []Principal {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Principal, 0, len(t.s.Principals))
	for _, p := range t.s.Principals {
		out = append(out, *p)
	}
	return out
}

// IsRevoked reports whether a P-256 key (any accepted encoding) is revoked.
func (t *TrustStore) IsRevoked(pub []byte) bool {
	c, err := keys.CanonicalPublicKey(pub)
	if err != nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, r := t.s.Revoked[keys.KeyIDBytes(c)]
	return r
}

// ApprovalKey implements approval.KeyLookup with owner-enrolled device keys:
// only devices enrolled by an owner statement and not revoked are trusted.
func (t *TrustStore) ApprovalKey(_ context.Context, deviceID string) (*ecdsa.PublicKey, error) {
	t.mu.RLock()
	p, ok := t.s.Principals[pkey("device", deviceID)]
	var pub []byte
	revoked := false
	if ok {
		pub = p.PublicKey
		_, revoked = t.s.Revoked[keys.KeyIDBytes(pub)]
	}
	t.mu.RUnlock()
	if !ok {
		return nil, approval.ErrUnknownDevice
	}
	if revoked {
		return nil, approval.ErrRevoked
	}
	return keys.ParsePublicKey(pub)
}

var _ approval.KeyLookup = (*TrustStore)(nil)

// EdgeVerifier is the Node-side approval check (SECURITY.md Layer 5). It
// satisfies the node package's ApprovalVerifier interface
// (VerifyApproval(ctx, *nexusv1.ApprovalResolution) error) and can be
// published under node.SvcApprovalVerifier.
//
// It rejects everything while the owner kill switch is engaged, recomputes
// the action hash from Approval.payload (RFC 8785 JCS) and requires it to
// equal payload_hash, then verifies the device signature against
// owner-enrolled, non-revoked device keys (approval.VerifyFor). The runtime
// must execute exactly Approval.payload for the binding to hold.
type EdgeVerifier struct {
	Trust *TrustStore
	Now   func() time.Time
}

// ErrLocked is returned while the kill switch is engaged.
var ErrLocked = errors.New("owner: node is locked by the owner (kill switch)")

// VerifyApproval implements the node ApprovalVerifier.
func (v EdgeVerifier) VerifyApproval(ctx context.Context, res *nexusv1.ApprovalResolution) error {
	if v.Trust == nil {
		return errors.New("owner: EdgeVerifier without trust store")
	}
	if res == nil || res.Decision == nil || res.Approval == nil || res.Approval.Payload == nil {
		return fmt.Errorf("%w: resolution needs approval, payload and decision", approval.ErrMalformed)
	}
	if v.Trust.Locked() {
		return ErrLocked
	}
	h, err := approval.ActionHash(res.Approval.Payload)
	if err != nil {
		return err
	}
	if !bytes.Equal(h, res.Approval.PayloadHash) {
		return approval.ErrHashMismatch
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	return approval.VerifyFor(ctx, res.Decision, res.Approval, v.Trust, now)
}
