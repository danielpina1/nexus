package node

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

// Files written by `nexus-node pair` into the node data dir
// (<data_dir>/node). Config fields left empty fall back to them.
const (
	IdentityKeyFile = "identity.key" // P-256 node identity key, PKCS#8 PEM, 0600
	ClientCertFile  = "node.crt"     // mTLS client certificate issued by the Hub CA
	HubCAFile       = "hub-ca.crt"   // Hub CA certificate (pinned)
	TokenFileName   = "node.token"   // node bearer token, 0600
)

// ResolveFiles fills token_file/ca_file/cert_file/key_file from the pairing
// files in dataDir when unset, and loads the token from token_file when no
// token is configured (node.token / NEXUS_NODE_TOKEN win).
func (c *Config) ResolveFiles(dataDir string) error {
	fallback := func(p *string, name string) {
		if *p != "" || dataDir == "" {
			return
		}
		f := filepath.Join(dataDir, name)
		if _, err := os.Stat(f); err == nil {
			*p = f
		}
	}
	fallback(&c.TokenFile, TokenFileName)
	fallback(&c.CAFile, HubCAFile)
	fallback(&c.CertFile, ClientCertFile)
	if c.CertFile != "" {
		fallback(&c.KeyFile, IdentityKeyFile)
	}
	if c.Token == "" && c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return fmt.Errorf("node: token_file: %w", err)
		}
		c.Token = strings.TrimSpace(string(b))
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return errors.New("node: cert_file and key_file must be set together")
	}
	return nil
}

// ClientTLSConfig is the node's TLS client config for an https:// hub:
// TLS 1.3 only; with ca_file the Hub CA is the only trusted root (no system
// roots) and is pinned; with cert_file/key_file the node presents its
// client certificate (mTLS). Without ca_file the system roots are used (a
// hub with a publicly trusted certificate).
func ClientTLSConfig(c Config) (*tls.Config, error) {
	var cert *tls.Certificate
	if c.CertFile != "" {
		kp, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("node: client certificate: %w", err)
		}
		cert = &kp
	}
	if c.CAFile == "" {
		tc := &tls.Config{MinVersion: tls.VersionTLS13}
		if cert != nil {
			tc.Certificates = []tls.Certificate{*cert}
		}
		return tc, nil
	}
	b, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("node: ca_file: %w", err)
	}
	ca, err := pki.ParseCertPEM(b)
	if err != nil {
		return nil, fmt.Errorf("node: ca_file %s: %w", c.CAFile, err)
	}
	if !ca.IsCA {
		return nil, fmt.Errorf("node: ca_file %s is not a CA certificate", c.CAFile)
	}
	return pki.ClientTLSConfig(ca, cert, ""), nil
}

// pairingTLSConfig verifies the hub before the node has the CA: the server
// chain must contain a certificate whose SHA-256 fingerprint is the QR's
// ca fingerprint, and the leaf must chain to it for serverName.
func pairingTLSConfig(caFingerprint string) *tls.Config {
	want := strings.ToLower(caFingerprint)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Standard verification is replaced by VerifyConnection below
		// (pinned root from the pairing QR instead of system roots).
		InsecureSkipVerify: true, //nolint:gosec // verified in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("pairing: hub sent no certificate")
			}
			var root *x509.Certificate
			for _, c := range cs.PeerCertificates {
				if pki.Fingerprint(c) == want {
					root = c
				}
			}
			if root == nil {
				return errors.New("pairing: hub certificate is not issued by the CA in the pairing URL (wrong hub or interception)")
			}
			pool := x509.NewCertPool()
			pool.AddCert(root)
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:     pool,
				DNSName:   cs.ServerName,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return fmt.Errorf("pairing: hub certificate: %w", err)
			}
			return nil
		},
	}
}
