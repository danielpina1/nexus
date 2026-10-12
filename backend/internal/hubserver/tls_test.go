package hubserver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
	"github.com/danielpina1/nexus/backend/internal/store"
)

func newSecurity(t *testing.T) *security.Security {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sec, err := security.New(context.Background(), security.Options{
		Config:  security.Config{ServerNames: []string{"nexus.home.arpa", "localhost"}},
		Store:   st,
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sec.Close() })
	return sec
}

func TestTLSAuto(t *testing.T) {
	sec := newSecurity(t)
	mux := http.NewServeMux()
	mux.Handle("/nexus.v1.TestService/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Proto)
	}))
	var cfg Config
	cfg.Listen = "127.0.0.1:0"
	cfg.TLS.Auto = true
	srv := New(Options{Config: cfg, Mux: mux, Logger: quiet, TLS: sec})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	errc := srv.Serve()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		<-errc
	})
	_, port, _ := net.SplitHostPort(srv.Addr().String())

	// A client that trusts (and pins) only the hub CA connects over h2,
	// both by DNS name and by the listen IP (added to the SANs).
	for _, host := range []string{"nexus.home.arpa", "127.0.0.1"} {
		tc := pki.ClientTLSConfig(sec.CA().Cert, nil, host)
		tr := &http.Transport{
			TLSClientConfig:   tc,
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+port)
			},
		}
		c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
		resp, err := c.Get("https://" + net.JoinHostPort(host, port) + "/nexus.v1.TestService/Ping")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "HTTP/2.0" || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
			t.Fatalf("%s: proto %q tls %+v", host, b, resp.TLS)
		}
		tr.CloseIdleConnections()
	}

	// A client using the system roots does not trust the hub.
	c := &http.Client{Timeout: 5 * time.Second}
	if _, err := c.Get("https://127.0.0.1:" + port + "/healthz"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("untrusted client: %v", err)
	}
	// Plain HTTP is not served on the TLS port.
	if resp, err := c.Get("http://127.0.0.1:" + port + "/healthz"); err == nil && resp.StatusCode == http.StatusOK {
		t.Fatal("plain HTTP accepted")
	}
}

func TestTLSAutoErrors(t *testing.T) {
	var cfg Config
	cfg.Listen = "127.0.0.1:0"
	cfg.TLS.Auto = true
	if err := New(Options{Config: cfg, Logger: quiet}).Listen(); err == nil || !strings.Contains(err.Error(), "security module") {
		t.Fatalf("auto without source: %v", err)
	}
	cfg.TLS.Cert = "x.pem"
	if err := New(Options{Config: cfg, Logger: quiet, TLS: newSecurity(t)}).Listen(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("auto + cert: %v", err)
	}
}

func TestListenHosts(t *testing.T) {
	for in, want := range map[string]string{":7700": "", "0.0.0.0:7700": "", "[::]:7700": "", "10.20.0.10:7700": "10.20.0.10", "nexus.home.arpa:7700": "nexus.home.arpa"} {
		got := strings.Join(listenHosts(in), ",")
		if got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}
