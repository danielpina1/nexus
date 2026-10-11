package pki

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

func TestLoadOrCreateCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	ca, err := LoadOrCreateCA(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode().Perm())
	}
	ca2, err := LoadOrCreateCA(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if ca.Fingerprint() != ca2.Fingerprint() || len(ca.Fingerprint()) != 64 {
		t.Fatal("fingerprint changed on reload")
	}
	// Half-present state refuses to overwrite.
	os.Remove(filepath.Join(dir, "ca.crt"))
	if _, err := LoadOrCreateCA(dir, ""); err == nil {
		t.Fatal("expected error with missing cert but present key")
	}
}

func TestParseCAMismatch(t *testing.T) {
	a, _ := NewCA("a")
	b, _ := NewCA("b")
	kp, _ := keys.MarshalPrivateKeyPEM(b.Key)
	if _, err := ParseCA(a.CertPEM, kp); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

func newServer(t *testing.T, ca *CA, o ServerOptions) *httptest.Server {
	t.Helper()
	srvCert, err := ca.IssueServerCert(ServerCertOptions{Hosts: []string{"hub.test", "127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := PeerIdentity(r)
		if !ok {
			io.WriteString(w, "anon")
			return
		}
		io.WriteString(w, kind+":"+id)
	}))
	s.TLS = ca.ServerTLSConfig(srvCert, o)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func clientCert(t *testing.T, ca *CA, kind, id string) *tls.Certificate {
	k, _ := keys.Generate()
	der, err := ca.IssueClientCert(&k.PublicKey, ClientCertOptions{ID: id, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func get(t *testing.T, s *httptest.Server, cfg *tls.Config) (string, error) {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr}
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	resp, err := c.Get("https://127.0.0.1:" + port + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestMutualTLS(t *testing.T) {
	ca, _ := NewCA("")
	revokedID := "dev_revoked"
	s := newServer(t, ca, ServerOptions{Revoked: func(c *x509.Certificate) bool { return c.Subject.CommonName == revokedID }})

	// Happy path.
	body, err := get(t, s, ClientTLSConfig(ca.Cert, clientCert(t, ca, KindDevice, "dev_1"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if body != "device:dev_1" {
		t.Fatalf("body %q", body)
	}

	// No client cert → handshake fails.
	if _, err := get(t, s, ClientTLSConfig(ca.Cert, nil, "")); err == nil {
		t.Fatal("expected failure without client cert")
	}

	// Client cert from another CA → fails.
	other, _ := NewCA("other")
	if _, err := get(t, s, ClientTLSConfig(ca.Cert, clientCert(t, other, KindDevice, "x"), "")); err == nil {
		t.Fatal("expected failure with foreign cert")
	}

	// Revoked → fails.
	if _, err := get(t, s, ClientTLSConfig(ca.Cert, clientCert(t, ca, KindDevice, revokedID), "")); err == nil {
		t.Fatal("expected failure with revoked cert")
	}

	// Client pinning a different CA rejects the server.
	if _, err := get(t, s, ClientTLSConfig(other.Cert, clientCert(t, ca, KindDevice, "dev_1"), "")); err == nil {
		t.Fatal("expected failure with wrong pinned CA")
	}
}

func TestClientCertIfGiven(t *testing.T) {
	ca, _ := NewCA("")
	s := newServer(t, ca, ServerOptions{Mode: ClientCertIfGiven})
	body, err := get(t, s, ClientTLSConfig(ca.Cert, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if body != "anon" {
		t.Fatalf("body %q", body)
	}
	body, err = get(t, s, ClientTLSConfig(ca.Cert, clientCert(t, ca, KindNode, "node_1"), ""))
	if err != nil || body != "node:node_1" {
		t.Fatalf("%q %v", body, err)
	}
}

func TestPinnedClientTLSConfig(t *testing.T) {
	ca, _ := NewCA("")
	if _, err := PinnedClientTLSConfig(ca.CertPEM, ca.Fingerprint(), nil, "hub"); err != nil {
		t.Fatal(err)
	}
	other, _ := NewCA("")
	if _, err := PinnedClientTLSConfig(ca.CertPEM, other.Fingerprint(), nil, "hub"); err == nil {
		t.Fatal("fingerprint mismatch accepted")
	}
}

func TestServerTLSConfigDefaults(t *testing.T) {
	ca, _ := NewCA("")
	cfg := ca.ServerTLSConfig(nil, ServerOptions{})
	if cfg.MinVersion != tls.VersionTLS13 || cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("bad defaults")
	}
	if len(cfg.CurvePreferences) != 0 {
		t.Fatal("curve preferences must stay default (X25519MLKEM768)")
	}
}
