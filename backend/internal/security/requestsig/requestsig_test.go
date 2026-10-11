package requestsig

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
)

func TestCanonicalString(t *testing.T) {
	h := sha256.Sum256(nil)
	got := CanonicalString("post", "/nexus.v1.X/Y", h[:], "1700000000", "bm9uY2U", "dev_1", "")
	want := "POST\n/nexus.v1.X/Y\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n1700000000\nbm9uY2U\ndev_1\n"
	if got != want {
		t.Fatalf("%q", got)
	}
}

type env struct {
	key     *ecdsa.PrivateKey
	v       *Verifier
	srv     http.Handler
	revoked map[string]bool
}

func newEnv(t *testing.T) *env {
	k, _ := keys.Generate()
	e := &env{key: k, revoked: map[string]bool{}}
	e.v = &Verifier{
		Keys: func(_ context.Context, id string) (*ecdsa.PublicKey, error) {
			if id != "dev_1" || e.revoked[id] {
				return nil, errors.New("unknown or revoked")
			}
			return &k.PublicKey, nil
		},
		Guard:        replay.New(time.Minute),
		MaxBodyBytes: 1024,
	}
	e.srv = e.v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := DeviceID(r.Context())
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, id+":"+string(b))
	}))
	return e
}

func (e *env) req(t *testing.T, body string, counter uint64, mut func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/nexus.v1.TaskService/CreateTask", strings.NewReader(body))
	if err := Sign(r, "dev_1", e.key, counter, time.Now()); err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	return w
}

func TestMiddlewareHappyPath(t *testing.T) {
	e := newEnv(t)
	w := e.req(t, `{"a":1}`, 1, nil)
	if w.Code != 200 || w.Body.String() != `dev_1:{"a":1}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = e.req(t, ``, 2, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = e.req(t, `x`, 0, nil) // counter optional by default
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestMiddlewareRejections(t *testing.T) {
	cases := map[string]func(*http.Request){
		"tampered body": func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"a":2}`)) },
		"tampered path": func(r *http.Request) { r.URL.Path = "/nexus.v1.TaskService/CancelTask" },
		"tampered method": func(r *http.Request) {
			r.Method = http.MethodPut
		},
		"other device":   func(r *http.Request) { r.Header.Set(HeaderDevice, "dev_2") },
		"bumped counter": func(r *http.Request) { r.Header.Set(HeaderCounter, "99") },
		"missing sig":    func(r *http.Request) { r.Header.Del(HeaderSignature) },
		"bad timestamp":  func(r *http.Request) { r.Header.Set(HeaderTimestamp, "abc") },
		"zero counter":   func(r *http.Request) { r.Header.Set(HeaderCounter, "0") },
		"garbage sig":    func(r *http.Request) { r.Header.Set(HeaderSignature, "!!!") },
		"shifted timestamp": func(r *http.Request) {
			ts, _ := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
			r.Header.Set(HeaderTimestamp, strconv.FormatInt(ts+1, 10))
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			w := e.req(t, `{"a":1}`, 1, mut)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), `"unauthenticated"`) {
				t.Fatalf("body %s", w.Body)
			}
		})
	}
}

func TestReplayAndStale(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b"))
	_ = Sign(r, "dev_1", e.key, 5, time.Now())
	hdr := r.Header.Clone()
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	// Exact replay.
	r2 := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b"))
	r2.Header = hdr
	w = httptest.NewRecorder()
	e.srv.ServeHTTP(w, r2)
	if w.Code != 401 {
		t.Fatal("replay accepted")
	}
	// Lower counter with fresh nonce.
	w = e.req(t, "b", 4, nil)
	if w.Code != 401 {
		t.Fatal("counter regression accepted")
	}
	// Stale timestamp.
	r3 := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b"))
	_ = Sign(r3, "dev_1", e.key, 10, time.Now().Add(-2*time.Minute))
	w = httptest.NewRecorder()
	e.srv.ServeHTTP(w, r3)
	if w.Code != 401 {
		t.Fatal("stale accepted")
	}
}

func TestRevokedAndWrongKey(t *testing.T) {
	e := newEnv(t)
	e.revoked["dev_1"] = true
	if w := e.req(t, "b", 1, nil); w.Code != 401 {
		t.Fatal("revoked accepted")
	}
	e2 := newEnv(t)
	other, _ := keys.Generate()
	r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b"))
	_ = Sign(r, "dev_1", other, 1, time.Now())
	w := httptest.NewRecorder()
	e2.srv.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("wrong key accepted")
	}
}

func TestBodyLimitAndOptional(t *testing.T) {
	e := newEnv(t)
	w := e.req(t, strings.Repeat("x", 2048), 1, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", w.Code)
	}
	// Unsigned request: rejected unless Optional.
	r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b"))
	w = httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unsigned accepted")
	}
	e.v.Optional = true
	w = httptest.NewRecorder()
	e.srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("b")))
	if w.Code != 200 || w.Body.String() != ":b" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
