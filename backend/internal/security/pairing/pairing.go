// Package pairing implements the in-person pairing ceremony primitives
// (SECURITY.md §3): one-time pairing secrets, the QR payload, the
// pairing proof and the emoji safety code.
//
// # Pairing secret
//
// 32 random bytes, valid 5 minutes, single use. In the QR / PairRequest it
// is carried as base64url without padding (43 chars) — the "pairing token".
//
// # Pairing proof (clients MUST match)
//
//	key   = HKDF-SHA256(IKM = secret (the 32 raw bytes), salt = empty,
//	                    info = ASCII "nexus/v1/pairing", L = 32)
//	proof = HMAC-SHA256(key, public_key || approval_public_key || encryption_public_key)
//
// where the three keys are exactly the bytes sent in PairRequest
// (recommended: P-256 keys as 65-byte X9.63, X25519 as 32 raw bytes; a Node
// sends its identity key as public_key and leaves the others empty).
// CryptoKit: HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: secret),
// info: Data("nexus/v1/pairing".utf8), outputByteCount: 32), then
// HMAC<SHA256>.authenticationCode(for: auth+approval+enc, using: key).
//
// # Safety code
//
//	keys   = the Hub identity public key (65-byte P-256) and the device's
//	         public_key, approval_public_key, encryption_public_key
//	         (P-256 keys in 65-byte form, X25519 32 raw; empty keys skipped)
//	sorted = keys sorted bytewise ascending (lexicographic)
//	digest = SHA256(ASCII "nexus/v1/safety-code" || 0x00 ||
//	                for each k in sorted: u32be(len(k)) || k)
//	code   = Emojis[digest[i] >> 2] for i = 0..5, joined with a single space
//
// # QR payload
//
//	nexus://pair?v=1&k=device&hub=<url>&hub=<url2>&ca=<sha256 hex>&s=<token>&hk=<base64url hub pubkey>&exp=<unix seconds>
//
// ParseQR also accepts the same fields as base64url(JSON) (see QR).
package pairing

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// Constants.
const (
	Info          = "nexus/v1/pairing"
	SafetyCodeTag = "nexus/v1/safety-code"
	SecretLen     = 32
	DefaultTTL    = 5 * time.Minute
	KindDevice    = "device"
	KindNode      = "node"
)

// Errors.
var (
	ErrInvalidToken = errors.New("pairing: invalid, expired or already used pairing token")
	ErrBadProof     = errors.New("pairing: bad pairing proof")
)

// Emojis is the fixed 64-entry safety-code table (index = 6-bit value).
// Clients must embed exactly this table in this order.
var Emojis = [64]string{
	"🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼",
	"🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
	"🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺",
	"🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
	"🐢", "🐍", "🦎", "🐙", "🦑", "🦀", "🐡", "🐠",
	"🐟", "🐬", "🐳", "🦈", "🐊", "🐘", "🦒", "🌵",
	"🌲", "🌴", "🍄", "🌻", "🌙", "🍎", "🍌", "🍇",
	"🍓", "🍒", "🍍", "🥕", "🌽", "🍕", "🎈", "🔑",
}

// NewSecret returns a fresh secret and its token encoding.
func NewSecret() (token string, secret []byte, err error) {
	secret = make([]byte, SecretLen)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(secret), secret, nil
}

// DecodeToken decodes a pairing token into the raw secret.
func DecodeToken(token string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != SecretLen {
		return nil, ErrInvalidToken
	}
	return b, nil
}

// ProofKey derives the HMAC key from the secret.
func ProofKey(secret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, nil, Info, 32)
}

// Proof computes the pairing proof.
func Proof(secret, publicKey, approvalKey, encryptionKey []byte) ([]byte, error) {
	k, err := ProofKey(secret)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, k)
	m.Write(publicKey)
	m.Write(approvalKey)
	m.Write(encryptionKey)
	return m.Sum(nil), nil
}

// VerifyProof checks a proof in constant time.
func VerifyProof(secret, publicKey, approvalKey, encryptionKey, proof []byte) error {
	want, err := Proof(secret, publicKey, approvalKey, encryptionKey)
	if err != nil {
		return err
	}
	if !hmac.Equal(want, proof) {
		return ErrBadProof
	}
	return nil
}

// SafetyCode derives the 6-emoji code from the given public keys (order
// irrelevant; empty keys skipped).
func SafetyCode(pubKeys ...[]byte) string {
	var ks [][]byte
	for _, k := range pubKeys {
		if len(k) > 0 {
			ks = append(ks, k)
		}
	}
	sort.Slice(ks, func(i, j int) bool { return bytes.Compare(ks[i], ks[j]) < 0 })
	b := keys.NewBuilder(SafetyCodeTag)
	for _, k := range ks {
		b.Bytes(k)
	}
	d := sha256.Sum256(b.Message())
	out := make([]string, 6)
	for i := range out {
		out[i] = Emojis[d[i]>>2]
	}
	return strings.Join(out, " ")
}

// QR is the pairing QR payload.
type QR struct {
	Version       int      `json:"v"`
	Kind          string   `json:"k"`   // "device" | "node"
	HubURLs       []string `json:"hub"` // e.g. https://hub.tailnet.ts.net:7700
	CAFingerprint string   `json:"ca"`  // lowercase hex SHA-256 of CA cert DER
	Token         string   `json:"s"`   // base64url pairing secret
	HubKey        string   `json:"hk"`  // base64url 65-byte hub identity key
	Expires       int64    `json:"exp"` // Unix seconds
}

// URL encodes the QR as a nexus://pair URL.
func (q *QR) URL() string {
	v := url.Values{}
	v.Set("v", strconv.Itoa(q.Version))
	v.Set("k", q.Kind)
	for _, h := range q.HubURLs {
		v.Add("hub", h)
	}
	v.Set("ca", q.CAFingerprint)
	v.Set("s", q.Token)
	v.Set("hk", q.HubKey)
	v.Set("exp", strconv.FormatInt(q.Expires, 10))
	return "nexus://pair?" + v.Encode()
}

// Base64 encodes the QR as base64url(JSON) without padding.
func (q *QR) Base64() string {
	b, _ := json.Marshal(q)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ParseQR decodes a nexus://pair URL or a base64url JSON payload.
func ParseQR(s string) (*QR, error) {
	s = strings.TrimSpace(s)
	q := &QR{}
	if strings.HasPrefix(s, "nexus://") {
		u, err := url.Parse(s)
		if err != nil || u.Host != "pair" {
			return nil, fmt.Errorf("pairing: not a nexus://pair URL")
		}
		v := u.Query()
		q.Version, _ = strconv.Atoi(v.Get("v"))
		q.Kind = v.Get("k")
		q.HubURLs = v["hub"]
		q.CAFingerprint = v.Get("ca")
		q.Token = v.Get("s")
		q.HubKey = v.Get("hk")
		q.Expires, _ = strconv.ParseInt(v.Get("exp"), 10, 64)
	} else {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
		if err != nil {
			return nil, fmt.Errorf("pairing: bad QR payload: %w", err)
		}
		if err := json.Unmarshal(b, q); err != nil {
			return nil, fmt.Errorf("pairing: bad QR payload: %w", err)
		}
	}
	if q.Version != 1 || len(q.HubURLs) == 0 || len(q.CAFingerprint) != 64 || q.Token == "" {
		return nil, errors.New("pairing: incomplete QR payload")
	}
	if _, err := DecodeToken(q.Token); err != nil {
		return nil, err
	}
	return q, nil
}

// Pending is an issued, not yet redeemed pairing.
type Pending struct {
	Kind      string
	Label     string
	ExpiresAt time.Time
}

// Store persists pending pairings keyed by SHA256(secret). Take must be
// atomic (single use) and must not return expired entries.
type Store interface {
	Put(ctx context.Context, secretHash []byte, p Pending) error
	Take(ctx context.Context, secretHash []byte, now time.Time) (Pending, bool, error)
}

// MemoryStore is an in-process Store.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Pending
}

// Put implements Store.
func (s *MemoryStore) Put(_ context.Context, h []byte, p Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Pending{}
	}
	s.m[string(h)] = p
	return nil
}

// Take implements Store.
func (s *MemoryStore) Take(_ context.Context, h []byte, now time.Time) (Pending, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.m { // prune expired
		if !now.Before(p.ExpiresAt) {
			delete(s.m, k)
		}
	}
	p, ok := s.m[string(h)]
	delete(s.m, string(h))
	return p, ok, nil
}

// Manager issues and redeems pairing secrets.
type Manager struct {
	Store Store
	TTL   time.Duration
	Now   func() time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// New issues a secret of the given kind; returns the token and expiry.
func (m *Manager) New(ctx context.Context, kind, label string) (string, time.Time, error) {
	if kind != KindDevice && kind != KindNode {
		return "", time.Time{}, fmt.Errorf("pairing: unknown kind %q", kind)
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	token, secret, err := NewSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := m.now().Add(ttl)
	h := sha256.Sum256(secret)
	if err := m.Store.Put(ctx, h[:], Pending{Kind: kind, Label: label, ExpiresAt: exp}); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// Redeem consumes a token (single use, even if later checks fail) and
// returns the raw secret for proof verification.
func (m *Manager) Redeem(ctx context.Context, token, wantKind string) ([]byte, Pending, error) {
	secret, err := DecodeToken(token)
	if err != nil {
		return nil, Pending{}, err
	}
	h := sha256.Sum256(secret)
	p, ok, err := m.Store.Take(ctx, h[:], m.now())
	if err != nil {
		return nil, Pending{}, err
	}
	if !ok || !m.now().Before(p.ExpiresAt) || (wantKind != "" && p.Kind != wantKind) {
		return nil, Pending{}, ErrInvalidToken
	}
	return secret, p, nil
}
