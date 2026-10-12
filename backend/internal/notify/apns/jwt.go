package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Token lifetimes. Apple rejects provider tokens older than one hour and
// throttles refreshes more frequent than every 20 minutes.
const (
	TokenRefreshAfter = 50 * time.Minute
	tokenMaxAge       = 60 * time.Minute
)

// ParsePrivateKey parses an APNs auth key (.p8: PEM "PRIVATE KEY", PKCS#8,
// P-256). A PEM "EC PRIVATE KEY" (SEC 1) is also accepted.
func ParsePrivateKey(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	var key any
	var err error
	switch blk.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(blk.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("apns: key is not an ECDSA P-256 key")
	}
	return ec, nil
}

// TokenSource issues and caches ES256 provider tokens (JWT).
type TokenSource struct {
	TeamID string
	KeyID  string
	Key    *ecdsa.PrivateKey
	Now    func() time.Time

	mu     sync.Mutex
	token  string
	issued time.Time
}

func (s *TokenSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Token returns a cached token, refreshing it after TokenRefreshAfter.
func (s *TokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Sub(s.issued) < TokenRefreshAfter && !now.Before(s.issued) {
		return s.token, nil
	}
	tok, err := SignJWT(s.TeamID, s.KeyID, s.Key, now)
	if err != nil {
		return "", err
	}
	s.token, s.issued = tok, now
	return tok, nil
}

// Invalidate drops the cached token (after ExpiredProviderToken).
func (s *TokenSource) Invalidate() {
	s.mu.Lock()
	s.token = ""
	s.mu.Unlock()
}

// SignJWT builds the APNs provider token:
// base64url({"alg":"ES256","kid":keyID}) "." base64url({"iss":teamID,"iat":now})
// "." base64url(r||s) — 64-byte raw ECDSA P-256/SHA-256 signature (JOSE).
func SignJWT(teamID, keyID string, key *ecdsa.PrivateKey, now time.Time) (string, error) {
	if teamID == "" || keyID == "" || key == nil {
		return "", errors.New("apns: team_id, key_id and key are required")
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	cl, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix()})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(cl)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}
