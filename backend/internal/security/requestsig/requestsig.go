// Package requestsig implements signed requests (SECURITY.md Layer 3) as an
// HTTP middleware usable in front of Connect handlers.
//
// # Headers (clients MUST send)
//
//	X-Nexus-Device:    device id (e.g. dev_01J...)
//	X-Nexus-Timestamp: Unix time in seconds, decimal (e.g. 1791763200)
//	X-Nexus-Nonce:     >= 16 random bytes, base64url without padding
//	                   (standard base64 also accepted)
//	X-Nexus-Counter:   per-device monotonic counter, decimal uint64, >= 1
//	                   (optional unless the server requires counters)
//	X-Nexus-Signature: base64 (standard, padded) of the ECDSA P-256
//	                   signature (DER, or raw 64-byte r||s) with the device
//	                   *auth* key
//
// # Signed string (exact)
//
//	S = METHOD + "\n" + TARGET + "\n" + hex(SHA256(body)) + "\n" +
//	    TIMESTAMP + "\n" + NONCE + "\n" + DEVICE_ID + "\n" + COUNTER
//
//	METHOD    upper-case HTTP method ("POST")
//	TARGET    request path plus "?query" if present, exactly as on the request
//	          line (e.g. "/nexus.v1.TaskService/CreateTask")
//	hex(...)  lowercase hex SHA-256 of the raw request body bytes as sent
//	          (after any compression), "e3b0c442...b855" for an empty body
//	TIMESTAMP, NONCE, DEVICE_ID, COUNTER
//	          the header values verbatim; COUNTER is "" if the header is absent
//
//	signature = ECDSA P-256 / SHA-256 over UTF-8(S) (digest SHA256(S));
//	            CryptoKit: authKey.signature(for: Data(S.utf8)).derRepresentation
//
// Verification: timestamp within ±60 s, nonce not reused (per device),
// counter strictly increasing per device when present, signature valid for
// the device's current (non-revoked) auth key.
package requestsig

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
)

// Header names.
const (
	HeaderDevice    = "X-Nexus-Device"
	HeaderTimestamp = "X-Nexus-Timestamp"
	HeaderNonce     = "X-Nexus-Nonce"
	HeaderCounter   = "X-Nexus-Counter"
	HeaderSignature = "X-Nexus-Signature"
)

// DefaultMaxBody bounds the body buffered for hashing.
const DefaultMaxBody = 8 << 20

// Errors.
var (
	ErrMissing      = errors.New("requestsig: missing signature headers")
	ErrMalformed    = errors.New("requestsig: malformed signature headers")
	ErrBadSignature = errors.New("requestsig: bad signature")
	ErrTooLarge     = errors.New("requestsig: body too large")
)

// CanonicalString builds S (see package doc).
func CanonicalString(method, target string, bodySHA256 []byte, timestamp, nonce, deviceID, counter string) string {
	return strings.Join([]string{
		strings.ToUpper(method), target, hex.EncodeToString(bodySHA256),
		timestamp, nonce, deviceID, counter,
	}, "\n")
}

// KeyLookup returns the current auth public key of a device; it must fail
// for unknown or revoked devices.
type KeyLookup func(ctx context.Context, deviceID string) (*ecdsa.PublicKey, error)

// Verifier checks signed requests.
type Verifier struct {
	Keys  KeyLookup
	Guard *replay.Guard // required; use replay.New(0)
	// MaxBodyBytes (default DefaultMaxBody).
	MaxBodyBytes int64
	// Optional lets requests without X-Nexus-Signature through unchanged
	// (the middleware then adds no identity). Requests that do carry a
	// signature are always verified.
	Optional bool
}

type ctxKey struct{}

// DeviceID returns the device that signed the request, if verified.
func DeviceID(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(ctxKey{}).(string)
	return s, ok
}

// WithDeviceID marks ctx as carrying a verified signer (tests, composition).
func WithDeviceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Present reports whether r carries a request signature.
func Present(r *http.Request) bool { return r.Header.Get(HeaderSignature) != "" }

// Verify checks r's signature. It buffers and restores r.Body.
func (v *Verifier) Verify(r *http.Request) (string, error) {
	h := r.Header
	dev, ts, nonceStr, ctrStr, sigStr := h.Get(HeaderDevice), h.Get(HeaderTimestamp), h.Get(HeaderNonce), h.Get(HeaderCounter), h.Get(HeaderSignature)
	if dev == "" || ts == "" || nonceStr == "" || sigStr == "" {
		return "", ErrMissing
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: timestamp", ErrMalformed)
	}
	nonce, err := decodeB64(nonceStr)
	if err != nil {
		return "", fmt.Errorf("%w: nonce", ErrMalformed)
	}
	var counter uint64
	if ctrStr != "" {
		counter, err = strconv.ParseUint(ctrStr, 10, 64)
		if err != nil || counter == 0 {
			return "", fmt.Errorf("%w: counter", ErrMalformed)
		}
	}
	sig, err := decodeB64(sigStr)
	if err != nil {
		return "", fmt.Errorf("%w: signature encoding", ErrMalformed)
	}
	max := v.MaxBodyBytes
	if max <= 0 {
		max = DefaultMaxBody
	}
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, max+1))
		r.Body.Close()
		if err != nil {
			return "", err
		}
		if int64(len(body)) > max {
			return "", ErrTooLarge
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	bh := sha256.Sum256(body)

	if v.Keys == nil || v.Guard == nil {
		return "", errors.New("requestsig: verifier not configured")
	}
	pub, err := v.Keys(r.Context(), dev)
	if err != nil {
		return "", fmt.Errorf("requestsig: device %q: %w", dev, err)
	}
	s := CanonicalString(r.Method, r.URL.RequestURI(), bh[:], ts, nonceStr, dev, ctrStr)
	if !keys.Verify(pub, []byte(s), sig) {
		return "", ErrBadSignature
	}
	if err := v.Guard.Check(dev, nonce, counter, time.Unix(secs, 0)); err != nil {
		return "", err
	}
	return dev, nil
}

// Middleware verifies signed requests and stores the device id in the
// request context (see DeviceID). Failures get a Connect-style 401 JSON body.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Present(r) && v.Optional {
			next.ServeHTTP(w, r)
			return
		}
		dev, err := v.Verify(r)
		if err != nil {
			status := http.StatusUnauthorized
			code := "unauthenticated"
			if errors.Is(err, ErrTooLarge) {
				status, code = http.StatusRequestEntityTooLarge, "resource_exhausted"
			}
			WriteError(w, status, code, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(WithDeviceID(r.Context(), dev)))
	})
}

// WriteError writes a Connect-protocol style JSON error.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

func decodeB64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("bad base64")
}

// Sign adds signature headers to req (Go clients: CLI, nodes, tests). It
// buffers and restores req.Body. counter 0 omits X-Nexus-Counter.
func Sign(req *http.Request, deviceID string, signer crypto.Signer, counter uint64, now time.Time) error {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	nonceStr := base64.RawURLEncoding.EncodeToString(nonce)
	ctr := ""
	if counter > 0 {
		ctr = strconv.FormatUint(counter, 10)
	}
	bh := sha256.Sum256(body)
	s := CanonicalString(req.Method, req.URL.RequestURI(), bh[:], ts, nonceStr, deviceID, ctr)
	sig, err := keys.Sign(signer, []byte(s))
	if err != nil {
		return err
	}
	req.Header.Set(HeaderDevice, deviceID)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonceStr)
	if ctr != "" {
		req.Header.Set(HeaderCounter, ctr)
	}
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return nil
}
