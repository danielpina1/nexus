package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// VerifyJWT checks an ES256 provider token and returns its claims.
func verifyJWT(t *testing.T, tok string, pub *ecdsa.PublicKey) (hdr, claims map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	dec := base64.RawURLEncoding
	hb, err := dec.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	cb, err := dec.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	sig, err := dec.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: len %d err %v", len(sig), err)
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, d[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("jwt signature invalid")
	}
	_ = json.Unmarshal(hb, &hdr)
	_ = json.Unmarshal(cb, &claims)
	return hdr, claims
}

type fakeAPNs struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []*http.Request
	bodies   [][]byte
	protos   []string
	statuses []int // per call; last repeats
	reasons  []string
	calls    atomic.Int32
}

func newFake(t *testing.T) *fakeAPNs {
	f := &fakeAPNs{statuses: []int{200}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1)) - 1
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.bodies = append(f.bodies, b)
		f.protos = append(f.protos, r.Proto)
		st := f.statuses[min(n, len(f.statuses)-1)]
		reason := ""
		if len(f.reasons) > 0 {
			reason = f.reasons[min(n, len(f.reasons)-1)]
		}
		f.mu.Unlock()
		w.Header().Set("apns-id", "11111111-2222-3333-4444-555555555555")
		w.WriteHeader(st)
		if st != 200 {
			body := map[string]any{"reason": reason}
			if st == 410 {
				body["timestamp"] = int64(1760000000000)
			}
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

func testClient(t *testing.T, f *fakeAPNs) (*Client, *ecdsa.PrivateKey) {
	t.Helper()
	k, _ := keys.Generate()
	c, err := New(Config{
		Tokens:     &TokenSource{TeamID: "TEAM123456", KeyID: "KEY1234567", Key: k},
		BaseURL:    f.srv.URL,
		HTTPClient: f.srv.Client(),
		Backoff:    func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestPushHeadersAndJWT(t *testing.T) {
	f := newFake(t)
	c, k := testClient(t, f)
	exp := time.Unix(1760003600, 0)
	resp, err := c.Push(context.Background(), Request{
		DeviceToken: "abcd01", Topic: "com.example.nexus", PushType: PushTypeAlert,
		Priority: 10, CollapseID: "apr_1", Expiration: exp, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Payload: []byte(`{"aps":{}}`),
	})
	if err != nil || !resp.OK() {
		t.Fatalf("push: %v %v", resp, err)
	}
	if resp.ApnsID == "" {
		t.Error("apns-id not returned")
	}
	r := f.reqs[0]
	if f.protos[0] != "HTTP/2.0" {
		t.Errorf("proto = %s, want HTTP/2.0", f.protos[0])
	}
	if r.Method != "POST" || r.URL.Path != "/3/device/abcd01" {
		t.Errorf("request %s %s", r.Method, r.URL.Path)
	}
	want := map[string]string{
		"apns-topic": "com.example.nexus", "apns-push-type": "alert", "apns-priority": "10",
		"apns-collapse-id": "apr_1", "apns-expiration": "1760003600",
		"apns-id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "content-type": "application/json",
	}
	for h, v := range want {
		if got := r.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	auth := r.Header.Get("authorization")
	if !strings.HasPrefix(auth, "bearer ") {
		t.Fatalf("authorization = %q", auth)
	}
	hdr, claims := verifyJWT(t, strings.TrimPrefix(auth, "bearer "), &k.PublicKey)
	if hdr["alg"] != "ES256" || hdr["kid"] != "KEY1234567" || claims["iss"] != "TEAM123456" || claims["iat"] == nil {
		t.Errorf("jwt hdr=%v claims=%v", hdr, claims)
	}
	if string(f.bodies[0]) != `{"aps":{}}` {
		t.Errorf("body = %s", f.bodies[0])
	}
}

func TestTokenCache(t *testing.T) {
	k, _ := keys.Generate()
	now := time.Unix(1760000000, 0)
	ts := &TokenSource{TeamID: "T", KeyID: "K", Key: k, Now: func() time.Time { return now }}
	a, _ := ts.Token()
	now = now.Add(49 * time.Minute)
	b, _ := ts.Token()
	if a != b {
		t.Error("token refreshed before 50 min")
	}
	now = now.Add(2 * time.Minute)
	c, _ := ts.Token()
	if c == a {
		t.Error("token not refreshed after 50 min")
	}
	_, claims := verifyJWT(t, c, &k.PublicKey)
	if int64(claims["iat"].(float64)) != now.Unix() {
		t.Errorf("iat = %v", claims["iat"])
	}
}

func TestStatusHandling(t *testing.T) {
	cases := []struct {
		name         string
		statuses     []int
		reasons      []string
		wantStatus   int
		wantCalls    int32
		unregistered bool
	}{
		{"ok", []int{200}, nil, 200, 1, false},
		{"gone", []int{410}, []string{"Unregistered"}, 410, 1, true},
		{"bad request no retry", []int{400}, []string{"BadDeviceToken"}, 400, 1, false},
		{"forbidden no retry", []int{403}, []string{"BadCertificate"}, 403, 1, false},
		{"429 then ok", []int{429, 200}, []string{"TooManyRequests", ""}, 200, 2, false},
		{"5xx then ok", []int{503, 500, 200}, nil, 200, 3, false},
		{"5xx exhausted", []int{500}, []string{"InternalServerError"}, 500, 4, false},
		{"expired token refresh", []int{403, 200}, []string{"ExpiredProviderToken", ""}, 200, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.statuses, f.reasons = tc.statuses, tc.reasons
			c, _ := testClient(t, f)
			resp, err := c.Push(context.Background(), Request{DeviceToken: "aa", Payload: []byte("{}")})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.wantStatus || f.calls.Load() != tc.wantCalls || resp.Unregistered() != tc.unregistered {
				t.Errorf("status %d calls %d unregistered %v", resp.StatusCode, f.calls.Load(), resp.Unregistered())
			}
			if tc.unregistered && resp.Timestamp.UnixMilli() != 1760000000000 {
				t.Errorf("410 timestamp = %v", resp.Timestamp)
			}
		})
	}
}

func TestParsePrivateKeyP8(t *testing.T) {
	k, _ := keys.Generate()
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err := ParsePrivateKey(p8)
	if err != nil || !got.Equal(k) {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ParsePrivateKey([]byte("nope")); err == nil {
		t.Error("expected error")
	}
}

func TestHostSelection(t *testing.T) {
	c := &Client{cfg: Config{Environment: "production"}}
	if c.host("sandbox") != SandboxURL || c.host("") != ProductionURL || c.host("production") != ProductionURL {
		t.Error("host selection")
	}
	c.cfg.BaseURL = "https://x/"
	if c.host("sandbox") != "https://x" {
		t.Error("base_url override")
	}
}
