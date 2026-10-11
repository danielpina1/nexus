// Package pki is the Hub's private certificate authority (SECURITY.md Layer 2).
//
// The CA (ECDSA P-256) issues only TLS certificates: short-lived client
// certificates for devices and nodes, and the Hub's server certificate. It
// cannot authorise actions (that is the owner root key's job).
//
// Files in the CA directory:
//
//	ca.crt  PEM certificate (0644)
//	ca.key  PEM PKCS#8 private key (0600)
//
// Clients pin the CA by its fingerprint: lowercase hex SHA-256 of the CA
// certificate DER (64 hex chars), delivered in the pairing QR.
//
// TLS configs are TLS 1.3 only and leave CurvePreferences at Go's defaults,
// which (Go 1.24+) include the hybrid post-quantum X25519MLKEM768 group.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// Default lifetimes.
const (
	CALifetime            = 10 * 365 * 24 * time.Hour
	DefaultClientLifetime = 30 * 24 * time.Hour
	DefaultServerLifetime = 30 * 24 * time.Hour
	clockSkew             = 5 * time.Minute
)

// Subject kinds encoded in client certificates (Subject OU and URI SAN
// nexus://<kind>/<id>).
const (
	KindDevice = "device"
	KindNode   = "node"
)

// CA is a loaded certificate authority.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
	now     func() time.Time
}

// LoadOrCreateCA loads ca.crt/ca.key from dir, creating a new CA named
// commonName when absent. The key file is written with mode 0600.
func LoadOrCreateCA(dir, commonName string) (*CA, error) {
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	certPEM, cerr := os.ReadFile(certPath)
	keyPEM, kerr := os.ReadFile(keyPath)
	if cerr == nil && kerr == nil {
		return ParseCA(certPEM, keyPEM)
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, cerr
	}
	if !errors.Is(kerr, os.ErrNotExist) && kerr != nil {
		return nil, kerr
	}
	if (cerr == nil) != (kerr == nil) {
		return nil, fmt.Errorf("pki: only one of %s / %s exists; refusing to overwrite", certPath, keyPath)
	}
	ca, err := NewCA(commonName)
	if err != nil {
		return nil, err
	}
	kp, err := keys.MarshalPrivateKeyPEM(ca.Key)
	if err != nil {
		return nil, err
	}
	if err := keys.WriteFileAtomic(keyPath, kp, 0o600); err != nil {
		return nil, err
	}
	if err := keys.WriteFileAtomic(certPath, ca.CertPEM, 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

// NewCA creates an in-memory CA.
func NewCA(commonName string) (*CA, error) {
	if commonName == "" {
		commonName = "Nexus Hub CA"
	}
	k, err := keys.Generate()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ski := sha256.Sum256(keys.MarshalPublicKey(&k.PublicKey))
	tmpl := &x509.Certificate{
		SerialNumber:          mustSerial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Nexus"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(CALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski[:20],
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, CertPEM: pemCert(der), Key: k}, nil
}

// ParseCA parses PEM certificate + key and checks they match.
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := ParseCertPEM(certPEM)
	if err != nil {
		return nil, err
	}
	k, err := keys.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&k.PublicKey) {
		return nil, errors.New("pki: CA key does not match certificate")
	}
	if !cert.IsCA {
		return nil, errors.New("pki: certificate is not a CA")
	}
	return &CA{Cert: cert, CertPEM: pemCert(cert.Raw), Key: k}, nil
}

// SetClock overrides the issuing clock (tests).
func (ca *CA) SetClock(now func() time.Time) { ca.now = now }

func (ca *CA) clock() time.Time {
	if ca.now != nil {
		return ca.now()
	}
	return time.Now()
}

// Fingerprint is the lowercase hex SHA-256 of the CA certificate DER.
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert) }

// Pool returns a pool containing only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// ClientCertOptions describe a device/node client certificate.
type ClientCertOptions struct {
	ID       string        // device or node id (Subject CN)
	Kind     string        // KindDevice or KindNode (Subject OU)
	Lifetime time.Duration // default DefaultClientLifetime
}

// IssueClientCert issues a client-auth certificate for pub (the device's
// Secure-Enclave auth key or the node identity key) and returns its DER.
func (ca *CA) IssueClientCert(pub crypto.PublicKey, o ClientCertOptions) ([]byte, error) {
	if o.ID == "" || (o.Kind != KindDevice && o.Kind != KindNode) {
		return nil, errors.New("pki: client cert needs id and kind device|node")
	}
	if o.Lifetime <= 0 {
		o.Lifetime = DefaultClientLifetime
	}
	now := ca.clock()
	u := &url.URL{Scheme: "nexus", Host: o.Kind, Path: "/" + o.ID}
	tmpl := &x509.Certificate{
		SerialNumber:   mustSerial(),
		Subject:        pkix.Name{CommonName: o.ID, OrganizationalUnit: []string{o.Kind}, Organization: []string{"Nexus"}},
		NotBefore:      now.Add(-clockSkew),
		NotAfter:       now.Add(o.Lifetime),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:           []*url.URL{u},
		AuthorityKeyId: ca.Cert.SubjectKeyId,
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
}

// ServerCertOptions describe the Hub server certificate.
type ServerCertOptions struct {
	// Hosts are DNS names or IP literals placed in the SANs.
	Hosts    []string
	Lifetime time.Duration // default DefaultServerLifetime
}

// IssueServerCert generates a fresh key and server certificate.
func (ca *CA) IssueServerCert(o ServerCertOptions) (*tls.Certificate, error) {
	if len(o.Hosts) == 0 {
		o.Hosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	if o.Lifetime <= 0 {
		o.Lifetime = DefaultServerLifetime
	}
	k, err := keys.Generate()
	if err != nil {
		return nil, err
	}
	now := ca.clock()
	tmpl := &x509.Certificate{
		SerialNumber:   mustSerial(),
		Subject:        pkix.Name{CommonName: o.Hosts[0], Organization: []string{"Nexus"}},
		NotBefore:      now.Add(-clockSkew),
		NotAfter:       now.Add(o.Lifetime),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		AuthorityKeyId: ca.Cert.SubjectKeyId,
	}
	for _, h := range o.Hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &k.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: k, Leaf: leaf}, nil
}

// ClientAuthMode selects how the server treats client certificates.
type ClientAuthMode int

const (
	// RequireClientCert: no verified client certificate, no handshake.
	RequireClientCert ClientAuthMode = iota
	// ClientCertIfGiven verifies a certificate when presented (pairing
	// listeners, where a new device has no certificate yet).
	ClientCertIfGiven
)

// ServerOptions configure ServerTLSConfig.
type ServerOptions struct {
	Mode ClientAuthMode
	// Revoked, when set, rejects client certificates it returns true for
	// (deny-list by serial/subject).
	Revoked func(cert *x509.Certificate) bool
	// GetCertificate overrides the static certificate (e.g. auto-renewal).
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// ServerTLSConfig returns a TLS 1.3-only server config trusting only this
// CA for client certificates. Use it as http.Server.TLSConfig with
// ListenAndServeTLS("", "") / ServeTLS, which add the h2 ALPN protocol.
func (ca *CA) ServerTLSConfig(cert *tls.Certificate, o ServerOptions) *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientCAs:  ca.Pool(),
	}
	if o.GetCertificate != nil {
		cfg.GetCertificate = o.GetCertificate
	} else if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	switch o.Mode {
	case ClientCertIfGiven:
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	default:
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	if o.Revoked != nil {
		revoked := o.Revoked
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 && revoked(cs.PeerCertificates[0]) {
				return errors.New("pki: client certificate revoked")
			}
			return nil
		}
	}
	return cfg
}

// ClientTLSConfig returns a TLS 1.3-only client config that trusts only
// caCert (no system roots) and additionally pins caCert by fingerprint.
// cert may be nil (pairing before a client certificate exists).
func ClientTLSConfig(caCert *x509.Certificate, cert *tls.Certificate, serverName string) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	pin := sha256.Sum256(caCert.Raw)
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
		VerifyConnection: func(cs tls.ConnectionState) error {
			for _, chain := range cs.VerifiedChains {
				if len(chain) == 0 {
					continue
				}
				root := sha256.Sum256(chain[len(chain)-1].Raw)
				if subtle.ConstantTimeCompare(root[:], pin[:]) == 1 {
					return nil
				}
			}
			return errors.New("pki: server chain does not end at the pinned CA")
		},
	}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return cfg
}

// PinnedClientTLSConfig builds a client config from the CA PEM and checks
// it against the expected fingerprint (from the pairing QR) first.
func PinnedClientTLSConfig(caPEM []byte, fingerprint string, cert *tls.Certificate, serverName string) (*tls.Config, error) {
	ca, err := ParseCertPEM(caPEM)
	if err != nil {
		return nil, err
	}
	got := Fingerprint(ca)
	if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(fingerprint))) != 1 {
		return nil, fmt.Errorf("pki: CA fingerprint mismatch")
	}
	return ClientTLSConfig(ca, cert, serverName), nil
}

// PeerIdentity extracts (kind, id) from a verified client certificate on r.
func PeerIdentity(r *http.Request) (kind, id string, ok bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", "", false
	}
	return CertIdentity(r.TLS.PeerCertificates[0])
}

// CertIdentity extracts (kind, id) from a client certificate's URI SAN.
func CertIdentity(c *x509.Certificate) (kind, id string, ok bool) {
	for _, u := range c.URIs {
		if u.Scheme == "nexus" && (u.Host == KindDevice || u.Host == KindNode) {
			id := strings.TrimPrefix(u.Path, "/")
			if id != "" {
				return u.Host, id, true
			}
		}
	}
	return "", "", false
}

// Fingerprint returns lowercase hex SHA-256 of cert DER.
func Fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}

// ParseCertPEM parses the first CERTIFICATE block.
func ParseCertPEM(b []byte) (*x509.Certificate, error) {
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return nil, errors.New("pki: no CERTIFICATE PEM block")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}

// EncodeCertPEM PEM-encodes a DER certificate.
func EncodeCertPEM(der []byte) []byte { return pemCert(der) }

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func mustSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}
