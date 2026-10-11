package hubserver

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/danielpina1/nexus/backend/internal/host"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeAuth accepts "Bearer good" and treats /pair/ as public.
type fakeAuth struct{}

func (fakeAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (fakeAuth) Public(p string) bool { return strings.HasPrefix(p, "/pair/") }

func okHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
}

func newTestServer(t *testing.T, cfg Config, withAuth bool) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/nexus.v1.TestService/", okHandler("api"))
	mux.Handle("/pair/", okHandler("pair"))
	mux.Handle("/panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	o := Options{Config: cfg, Mux: mux, Logger: quiet, Version: "test"}
	if withAuth {
		o.Auth = fakeAuth{}
	}
	return New(o).Handler()
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func webDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("js"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("svg"), 0o644))
	return dir
}

func TestRouting(t *testing.T) {
	cfg := Config{WebDir: webDir(t)}
	h := newTestServer(t, cfg, true)
	good := map[string]string{"Authorization": "Bearer good"}
	tests := []struct {
		name, method, path string
		hdr                map[string]string
		status             int
		body               string
		cache              string
	}{
		{"healthz public", "GET", "/healthz", nil, 200, `"status":"ok"`, ""},
		{"api needs auth", "POST", "/nexus.v1.TestService/Do", nil, 401, "unauthenticated", ""},
		{"api with auth", "POST", "/nexus.v1.TestService/Do", good, 200, "api", ""},
		{"public module path", "POST", "/pair/start", nil, 200, "pair", ""},
		{"spa root", "GET", "/", nil, 200, "spa", "no-cache"},
		{"spa route", "GET", "/tasks/tsk_1", nil, 200, "spa", "no-cache"},
		{"static file", "GET", "/favicon.svg", nil, 200, "svg", ""},
		{"hashed asset", "GET", "/assets/app.js", nil, 200, "js", "public, max-age=31536000, immutable"},
		{"missing asset", "GET", "/assets/nope.js", nil, 404, "", ""},
		{"unknown api", "POST", "/nexus.v1.Missing/Do", good, 404, "", ""},
		{"traversal rejected", "GET", "/../../etc/passwd", nil, 400, "", ""},
		{"panic recovered", "GET", "/panic", good, 500, "internal server error", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(h, tt.method, tt.path, tt.hdr)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.body) {
				t.Fatalf("body = %q, want %q", w.Body.String(), tt.body)
			}
			if tt.cache != "" && w.Header().Get("Cache-Control") != tt.cache {
				t.Fatalf("cache-control = %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestNoAuthNoWeb(t *testing.T) {
	h := newTestServer(t, Config{}, false)
	if w := do(h, "POST", "/nexus.v1.TestService/Do", nil); w.Code != 200 {
		t.Fatalf("api without auth module: %d", w.Code)
	}
	if w := do(h, "GET", "/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("placeholder: %d %q", w.Code, w.Body.String())
	}
	if w := do(h, "GET", "/other", nil); w.Code != 404 {
		t.Fatalf("other: %d", w.Code)
	}
}

func TestCORS(t *testing.T) {
	preflight := func(origin string) map[string]string {
		return map[string]string{
			"Origin":                         origin,
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "connect-protocol-version,content-type",
		}
	}
	tests := []struct {
		name       string
		origins    []string
		method     string
		hdr        map[string]string
		status     int
		allowOrig  string
		wantHeader bool
	}{
		{"default dev origin preflight", nil, "OPTIONS", preflight("http://localhost:5173"), 204, "http://localhost:5173", true},
		{"disallowed origin", nil, "OPTIONS", preflight("https://evil.example"), 401, "", false},
		{"wildcard", []string{"*"}, "OPTIONS", preflight("https://any.example"), 204, "https://any.example", true},
		{"disabled", []string{}, "OPTIONS", preflight("http://localhost:5173"), 401, "", false},
		{"actual request", nil, "POST", map[string]string{"Origin": "http://localhost:5173", "Authorization": "Bearer good"}, 200, "http://localhost:5173", false},
		{"no origin", nil, "POST", map[string]string{"Authorization": "Bearer good"}, 200, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{}
			cfg.CORS.AllowedOrigins = tt.origins
			h := newTestServer(t, cfg, true)
			w := do(h, tt.method, "/nexus.v1.TestService/Do", tt.hdr)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.allowOrig {
				t.Fatalf("allow-origin = %q, want %q", got, tt.allowOrig)
			}
			if tt.allowOrig != "" && !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Grpc-Status-Details-Bin") {
				t.Fatal("missing expose headers")
			}
			if tt.wantHeader {
				ah := w.Header().Get("Access-Control-Allow-Headers")
				for _, want := range []string{"Connect-Protocol-Version", "Connect-Timeout-Ms", "Authorization", "Content-Type", "Grpc-Timeout", "X-Grpc-Web", "X-User-Agent"} {
					if !strings.Contains(ah, want) {
						t.Fatalf("allow-headers %q missing %s", ah, want)
					}
				}
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	c, err := host.ParseConfig([]byte("server:\n  listen: \":9999\"\n  web_dir: /x\n  cors:\n    allowed_origins: [\"https://a\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9999" || cfg.WebDir != "/x" || len(cfg.CORS.AllowedOrigins) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
	cfg, _ = LoadConfig(nil)
	if cfg.Listen != DefaultListen || len(cfg.CORS.AllowedOrigins) != len(DefaultCORSOrigins) {
		t.Fatalf("defaults = %+v", cfg)
	}
}

// TestServeH2CAndShutdown checks h2c prior-knowledge, streaming flushes
// through the middleware, and that Shutdown ends long-lived streams.
func TestServeH2CAndShutdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nexus.v1.Stream/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // a long-lived stream
	})
	cfg := Config{Listen: "127.0.0.1:0"}
	s := New(Options{Config: cfg, Mux: mux, Logger: quiet, ShutdownGrace: 100 * time.Millisecond})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	errc := s.Serve()
	base := "http://" + s.Addr().String()

	h2 := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	resp, err := h2.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 {
		t.Fatalf("healthz over h2c: %s %d", resp.Proto, resp.StatusCode)
	}

	resp, err = h2.Get(base + "/nexus.v1.Stream/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "first\n" {
		t.Fatalf("stream first chunk: %q %v", buf, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
	// The stream body must end.
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream not terminated by shutdown")
	}
}

func TestServeBeforeListen(t *testing.T) {
	s := New(Options{Logger: quiet})
	if err := <-s.Serve(); err == nil {
		t.Fatal("expected error")
	}
}
