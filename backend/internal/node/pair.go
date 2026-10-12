package node

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

// PairOptions configure Pair.
type PairOptions struct {
	// URL is the nexus://pair URL from `nexusd pair -kind node` or the
	// dashboard (base64url JSON payloads are accepted too).
	URL string
	// NodeID requested from the hub ("" = the hub assigns one).
	NodeID string
	// DataDir is the node module data dir (<data_dir>/node); the identity
	// key, certificate, CA and token are written there.
	DataDir string
	// Transport overrides the HTTP transport factory (tests). tc is the
	// pinned pairing TLS config (nil for http:// hubs).
	Transport func(tc *tls.Config) http.RoundTripper
	// Now overrides the clock (tests).
	Now func() time.Time
}

// PairResult is what a successful Pair wrote and learned.
type PairResult struct {
	NodeID     string
	HubURL     string // the hub URL that answered
	SafetyCode string // 6 emojis; compare with the hub log / dashboard
	TokenFile  string
	CAFile     string
	CertFile   string
	KeyFile    string
}

// Pair performs node pairing (docs/WIRE_FORMATS.md "Pairing"): it creates
// (or reuses) the node identity key, proves knowledge of the pairing secret
// with proof = HMAC-SHA256(HKDF(secret, "nexus/v1/pairing"), identity_pub),
// calls DeviceService.PairNode over TLS pinned to the CA fingerprint from
// the URL, checks the returned CA, certificate and safety code, and writes
// the credentials into DataDir.
func Pair(ctx context.Context, o PairOptions) (*PairResult, error) {
	q, err := pairing.ParseQR(o.URL)
	if err != nil {
		return nil, err
	}
	if q.Kind != pairing.KindNode {
		return nil, fmt.Errorf("pairing: this is a %q pairing code; create one with `nexusd pair -kind node`", q.Kind)
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if q.Expires != 0 && now().Unix() > q.Expires {
		return nil, errors.New("pairing: code expired; create a new one with `nexusd pair -kind node`")
	}
	hubKey, err := base64.RawURLEncoding.DecodeString(q.HubKey)
	if err != nil || len(hubKey) == 0 {
		return nil, errors.New("pairing: bad hub key in pairing URL")
	}
	secret, err := pairing.DecodeToken(q.Token)
	if err != nil {
		return nil, err
	}
	if o.DataDir == "" {
		return nil, errors.New("pairing: data dir required")
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(o.DataDir, IdentityKeyFile)
	key, err := keys.LoadOrCreatePrivateKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("pairing: identity key: %w", err)
	}
	pub := keys.MarshalPublicKey(&key.PublicKey)
	proof, err := pairing.Proof(secret, pub, nil, nil)
	if err != nil {
		return nil, err
	}
	req := &nexusv1.PairNodeRequest{PairingToken: q.Token, NodeId: o.NodeID, PublicKey: pub, PairingProof: proof}

	var resp *nexusv1.PairNodeResponse
	var hubURL string
	var errs []error
	for _, u := range q.HubURLs {
		u = strings.TrimRight(u, "/")
		var tc *tls.Config
		var rt http.RoundTripper
		if strings.HasPrefix(u, "https://") {
			tc = pairingTLSConfig(q.CAFingerprint)
		}
		if o.Transport != nil {
			rt = o.Transport(tc)
		} else {
			tr := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, TLSClientConfig: tc}
			if tc == nil {
				p := new(http.Protocols)
				p.SetHTTP1(true)
				p.SetUnencryptedHTTP2(true)
				tr.Protocols = p
			}
			rt = tr
		}
		cl := nexusv1connect.NewDeviceServiceClient(&http.Client{Transport: rt, Timeout: 30 * time.Second}, u)
		r, err := cl.PairNode(ctx, connect.NewRequest(req))
		if err == nil {
			resp, hubURL = r.Msg, u
			break
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
		// The hub answered: the token is consumed, other URLs won't help.
		if c := connect.CodeOf(err); c == connect.CodePermissionDenied || c == connect.CodeInvalidArgument {
			break
		}
	}
	if resp == nil {
		return nil, fmt.Errorf("pairing failed: %w", errors.Join(errs...))
	}

	// Check what the hub sent before writing anything.
	ca, err := pki.ParseCertPEM(resp.CaCertificate)
	if err != nil {
		return nil, fmt.Errorf("pairing: hub CA: %w", err)
	}
	if pki.Fingerprint(ca) != strings.ToLower(q.CAFingerprint) {
		return nil, errors.New("pairing: hub CA does not match the fingerprint in the pairing URL")
	}
	cert, err := pki.ParseCertPEM(resp.ClientCertificate)
	if err != nil {
		return nil, fmt.Errorf("pairing: client certificate: %w", err)
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		return nil, fmt.Errorf("pairing: client certificate not issued by the hub CA: %w", err)
	}
	if _, id, ok := pki.CertIdentity(cert); !ok || id != resp.NodeId {
		return nil, errors.New("pairing: client certificate is not for this node")
	}
	keyPEM, err := keys.MarshalPrivateKeyPEM(key)
	if err != nil {
		return nil, err
	}
	if _, err := tls.X509KeyPair(resp.ClientCertificate, keyPEM); err != nil {
		return nil, fmt.Errorf("pairing: client certificate does not match the identity key: %w", err)
	}
	if want := pairing.SafetyCode(hubKey, pub); resp.SafetyCode != want {
		return nil, errors.New("pairing: safety code mismatch (hub key differs from the pairing URL)")
	}
	if resp.Token == "" {
		return nil, errors.New("pairing: hub returned no token")
	}

	res := &PairResult{
		NodeID: resp.NodeId, HubURL: hubURL, SafetyCode: resp.SafetyCode,
		TokenFile: filepath.Join(o.DataDir, TokenFileName),
		CAFile:    filepath.Join(o.DataDir, HubCAFile),
		CertFile:  filepath.Join(o.DataDir, ClientCertFile),
		KeyFile:   keyPath,
	}
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{res.TokenFile, []byte(resp.Token + "\n"), 0o600},
		{res.CAFile, resp.CaCertificate, 0o644},
		{res.CertFile, resp.ClientCertificate, 0o644},
	} {
		if err := keys.WriteFileAtomic(f.path, f.data, f.mode); err != nil {
			return nil, err
		}
	}
	return res, nil
}
