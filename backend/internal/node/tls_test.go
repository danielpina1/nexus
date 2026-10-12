package node

import (
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

// writePairingFiles writes what `nexus-node pair` leaves in the data dir.
func writePairingFiles(t *testing.T, ca *pki.CA, dir, nodeID string) {
	t.Helper()
	k, err := keys.LoadOrCreatePrivateKey(filepath.Join(dir, IdentityKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	der, err := ca.IssueClientCert(&k.PublicKey, pki.ClientCertOptions{ID: nodeID, Kind: pki.KindNode})
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		ClientCertFile: pki.EncodeCertPEM(der),
		HubCAFile:      ca.CertPEM,
		TokenFileName:  []byte("nxn_secret\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveFilesAndClientTLS(t *testing.T) {
	ca, err := pki.NewCA("")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// Nothing paired: no files, system roots.
	var empty Config
	if err := empty.ResolveFiles(dir); err != nil || empty.CAFile != "" || empty.Token != "" {
		t.Fatalf("%+v %v", empty, err)
	}
	tc, err := ClientTLSConfig(empty)
	if err != nil || tc.RootCAs != nil || tc.MinVersion != tls.VersionTLS13 {
		t.Fatal("default TLS config")
	}

	writePairingFiles(t, ca, dir, "mac")
	var c Config
	if err := c.ResolveFiles(dir); err != nil {
		t.Fatal(err)
	}
	if c.Token != "nxn_secret" || c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" {
		t.Fatalf("resolved: %+v", c)
	}
	// An explicit token wins over the token file.
	c2 := Config{Token: "explicit"}
	_ = c2.ResolveFiles(dir)
	if c2.Token != "explicit" {
		t.Fatal("token precedence")
	}
	if err := (&Config{CertFile: "x"}).ResolveFiles(""); err == nil {
		t.Fatal("cert without key accepted")
	}

	tc, err = ClientTLSConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Certificates) != 1 || tc.RootCAs == nil || tc.MinVersion != tls.VersionTLS13 {
		t.Fatal("paired TLS config")
	}

	// Handshake against a hub cert from the pinned CA (client cert required
	// and verified) succeeds; against another CA it fails.
	serve := func(srvCA *pki.CA) (string, chan string) {
		cert, err := srvCA.IssueServerCert(pki.ServerCertOptions{Hosts: []string{"localhost"}})
		if err != nil {
			t.Fatal(err)
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCA.ServerTLSConfig(cert, pki.ServerOptions{Mode: pki.RequireClientCert}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		peer := make(chan string, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			tconn := conn.(*tls.Conn)
			if err := tconn.Handshake(); err != nil {
				peer <- "error"
				return
			}
			_, id, _ := pki.CertIdentity(tconn.ConnectionState().PeerCertificates[0])
			peer <- id
			io.WriteString(conn, "ok")
		}()
		return ln.Addr().String(), peer
	}
	dial := func(addr string) error {
		cfg := tc.Clone()
		cfg.ServerName = "localhost"
		conn, err := tls.Dial("tcp", addr, cfg)
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = io.ReadAll(conn)
		return err
	}
	addr, peer := serve(ca)
	if err := dial(addr); err != nil {
		t.Fatal(err)
	}
	if id := <-peer; id != "mac" {
		t.Fatalf("server saw client %q", id)
	}
	other, _ := pki.NewCA("")
	addr, _ = serve(other)
	if err := dial(addr); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("foreign CA accepted: %v", err)
	}
}

func TestPairingTLSConfigPinsFingerprint(t *testing.T) {
	ca, _ := pki.NewCA("")
	cert, _ := ca.IssueServerCert(pki.ServerCertOptions{Hosts: []string{"localhost"}})
	ln, err := tls.Listen("tcp", "127.0.0.1:0", ca.ServerTLSConfig(cert, pki.ServerOptions{Mode: pki.ClientCertIfGiven}))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	try := func(fp, host string) error {
		cfg := pairingTLSConfig(fp)
		cfg.ServerName = host
		c, err := tls.Dial("tcp", ln.Addr().String(), cfg)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := try(ca.Fingerprint(), "localhost"); err != nil {
		t.Fatal(err)
	}
	if err := try(strings.ToUpper(ca.Fingerprint()), "localhost"); err != nil {
		t.Fatal("fingerprint case:", err)
	}
	other, _ := pki.NewCA("")
	if err := try(other.Fingerprint(), "localhost"); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
	if err := try(ca.Fingerprint(), "evil.example"); err == nil {
		t.Fatal("wrong host accepted")
	}
}
