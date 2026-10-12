package security

import (
	"context"
	"crypto/tls"
	"errors"
	"regexp"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/security/pki"
)

// defaultServerNames are the hub certificate SANs when security.server_names
// is empty (pki.IssueServerCert's defaults).
var defaultServerNames = []string{"localhost", "127.0.0.1", "::1"}

// AddServerNames adds SANs (DNS names or IP literals, e.g. the listen IP)
// to the hub server certificate; the next handshake issues a new one.
func (s *Security) AddServerNames(names ...string) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	cur := s.cfg.ServerNames
	if len(cur) == 0 {
		cur = append([]string(nil), defaultServerNames...)
	}
	changed := false
	for _, n := range names {
		if n != "" && !slices.Contains(cur, n) {
			cur = append(cur, n)
			changed = true
		}
	}
	if changed || len(s.cfg.ServerNames) == 0 {
		s.cfg.ServerNames = cur
		s.serverCert = nil
	}
}

// ServerNames returns the hub certificate SANs in effect.
func (s *Security) ServerNames() []string {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	if len(s.cfg.ServerNames) == 0 {
		return append([]string(nil), defaultServerNames...)
	}
	return append([]string(nil), s.cfg.ServerNames...)
}

// HubTLSConfig is the hub listener's TLS config (server.tls.auto): TLS 1.3,
// certificate from the Hub CA with SANs = security.server_names +
// extraHosts, client certificates verified when given (devices and nodes
// pair first, then present their certificates).
func (s *Security) HubTLSConfig(extraHosts []string) *tls.Config {
	s.AddServerNames(extraHosts...)
	return s.ServerTLSConfig(pki.ClientCertIfGiven)
}

func pairingKind(k nexusv1.PairingKind) (string, bool) {
	switch k {
	case nexusv1.PairingKind_PAIRING_KIND_UNSPECIFIED, nexusv1.PairingKind_PAIRING_KIND_DEVICE:
		return pairing.KindDevice, true
	case nexusv1.PairingKind_PAIRING_KIND_NODE:
		return pairing.KindNode, true
	}
	return "", false
}

// CreatePairing issues a 5-minute pairing URL (dashboard "Pair a device").
func (d *deviceService) CreatePairing(ctx context.Context, req *connect.Request[nexusv1.CreatePairingRequest]) (*connect.Response[nexusv1.CreatePairingResponse], error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	kind, ok := pairingKind(req.Msg.Kind)
	if !ok {
		return nil, cerr(connect.CodeInvalidArgument, "unknown pairing kind")
	}
	label := req.Msg.Label
	if len(label) > 128 {
		return nil, cerr(connect.CodeInvalidArgument, "label too long")
	}
	if label == "" {
		label = "web"
	}
	label += " (by " + p.Kind
	if p.ID != "" {
		label += " " + p.ID
	}
	label += ")"
	qr, err := d.s.NewPairing(ctx, PairingOptions{Kind: kind, Label: label})
	if err != nil {
		return nil, cerr(connect.CodeFailedPrecondition, "%v", err)
	}
	rk := nexusv1.PairingKind_PAIRING_KIND_DEVICE
	if kind == pairing.KindNode {
		rk = nexusv1.PairingKind_PAIRING_KIND_NODE
	}
	return connect.NewResponse(&nexusv1.CreatePairingResponse{
		Url:           qr.URL(),
		ExpiresAt:     timestamppb.New(time.Unix(qr.Expires, 0)),
		Kind:          rk,
		HubUrls:       qr.HubURLs,
		CaFingerprint: qr.CAFingerprint,
	}), nil
}

var nodeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// PairNode redeems a node pairing token (public, proof-authenticated).
func (d *deviceService) PairNode(ctx context.Context, req *connect.Request[nexusv1.PairNodeRequest]) (*connect.Response[nexusv1.PairNodeResponse], error) {
	m := req.Msg
	if m.NodeId != "" && !nodeIDRe.MatchString(m.NodeId) {
		return nil, cerr(connect.CodeInvalidArgument, "node_id must match %s", nodeIDRe)
	}
	if _, err := keys.CanonicalPublicKey(m.PublicKey); err != nil {
		return nil, cerr(connect.CodeInvalidArgument, "public_key: %v", err)
	}
	if m.PairingToken == "" || len(m.PairingProof) == 0 {
		return nil, cerr(connect.CodeInvalidArgument, "pairing_token and pairing_proof required")
	}
	// The proof covers the key bytes exactly as sent.
	np, err := d.s.RedeemNodePairing(ctx, m.PairingToken, m.NodeId, m.PublicKey, m.PairingProof)
	if err != nil {
		if errors.Is(err, pairing.ErrInvalidToken) || errors.Is(err, pairing.ErrBadProof) {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	d.s.log.Info("node paired — compare the safety code shown by nexus-node",
		"node_id", np.NodeID, "safety_code", np.SafetyCode)
	return connect.NewResponse(&nexusv1.PairNodeResponse{
		NodeId:            np.NodeID,
		Token:             np.Token,
		ClientCertificate: np.ClientCert,
		CaCertificate:     np.CACert,
		SafetyCode:        np.SafetyCode,
	}), nil
}
