package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/hubserver"
	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	_ "github.com/danielpina1/nexus/backend/internal/store"
	"github.com/danielpina1/nexus/backend/kernel"
)

// startHub runs an in-process hub (store + security modules) with
// server.tls.auto on 127.0.0.1.
func startHub(t *testing.T) (*security.Security, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := host.ParseConfig([]byte("data_dir: " + dir + "\nmodules:\n  enable: [store, security]\n"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	proc := host.New(host.Options{Side: kernel.SideHub, Config: cfg, Logger: log, Mux: mux})
	ctx := context.Background()
	if err := proc.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var sc hubserver.Config
	sc.Listen = "127.0.0.1:0"
	sc.TLS.Auto = true
	srv := hubserver.New(hubserver.Options{Config: sc, Mux: mux, Logger: log,
		Auth: hubserver.AuthFrom(proc.Lookup), TLS: hubserver.TLSFrom(proc.Lookup)})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	errc := srv.Serve()
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		<-errc
		proc.Stop(sctx)
	})
	v, _ := proc.Lookup(kernel.SvcSecurity)
	sec, ok := v.(*security.Security)
	if !ok {
		t.Fatal("security service missing")
	}
	_, port, _ := strings.Cut(srv.Addr().String(), ":")
	return sec, "https://localhost:" + port
}

func TestNodePairRoundTrip(t *testing.T) {
	sec, hubURL := startHub(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(cfgPath, []byte("# my node\ndata_dir: "+filepath.Join(dir, "data")+"\nnode:\n  node_id: mac-studio\n  token: old\n  tags: [mac]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A tampered CA fingerprint is refused during the TLS handshake, before
	// the (single-use) token is sent.
	qr, err := sec.NewPairing(ctx, security.PairingOptions{Kind: pairing.KindNode, HubURLs: []string{"https://127.0.0.1:1", hubURL}})
	if err != nil {
		t.Fatal(err)
	}
	good := qr.CAFingerprint
	qr.CAFingerprint = strings.Repeat("ab", 32)
	if err := pairNode(ctx, io.Discard, cfgPath, qr.URL(), "", node.PairOptions{}); err == nil || !strings.Contains(err.Error(), "not issued by the CA") {
		t.Fatalf("tampered fingerprint: %v", err)
	}
	// The first hub URL is unreachable; the second one answers.
	qr.CAFingerprint = good
	var out strings.Builder
	if err := pairNode(ctx, &out, cfgPath, qr.URL(), "", node.PairOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Safety code:") || !strings.Contains(out.String(), "mac-studio") {
		t.Fatal(out.String())
	}
	// Single use.
	if err := pairNode(ctx, io.Discard, cfgPath, qr.URL(), "", node.PairOptions{}); err == nil {
		t.Fatal("pairing URL reused")
	}

	// node.yaml now points at the hub and the credential files.
	cfg, err := host.LoadConfigFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var nc node.Config
	if err := cfg.Section("node", &nc); err != nil {
		t.Fatal(err)
	}
	if nc.HubURL != hubURL || nc.NodeID != "mac-studio" || nc.Token != "" || len(nc.Tags) != 1 {
		t.Fatalf("node.yaml: %+v", nc)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "# my node") {
		t.Fatal("comments lost")
	}
	if fi, err := os.Stat(nc.KeyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity key: %v", err)
	}
	if fi, err := os.Stat(nc.TokenFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v", err)
	}
	if err := nc.ResolveFiles(""); err != nil {
		t.Fatal(err)
	}
	if err := sec.AuthenticateNode(ctx, "mac-studio", nc.Token); err != nil {
		t.Fatal(err)
	}

	// The node connects with TLS pinned to the CA, presenting its client
	// certificate and token: the hub authenticates it as node mac-studio
	// (nodes may not list devices → PermissionDenied, not Unauthenticated).
	tc, err := node.ClientTLSConfig(nc)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: true}, Timeout: 10 * time.Second}
	cl := nexusv1connect.NewDeviceServiceClient(hc, nc.HubURL)
	req := connect.NewRequest(&nexusv1.ListDevicesRequest{})
	req.Header().Set("Authorization", "Bearer "+nc.Token)
	req.Header().Set(security.HeaderNodeID, nc.NodeID)
	if _, err := cl.ListDevices(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("node call: %v", err)
	}
	// A wrong token with the valid certificate is rejected.
	req = connect.NewRequest(&nexusv1.ListDevicesRequest{})
	req.Header().Set("Authorization", "Bearer nxn_wrong")
	if _, err := cl.ListDevices(ctx, req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestReorderArgs(t *testing.T) {
	got := strings.Join(reorder([]string{"nexus://pair?x", "-config", "n.yaml", "-id=a"}), " ")
	if got != "-config n.yaml -id=a nexus://pair?x" {
		t.Fatal(got)
	}
}
