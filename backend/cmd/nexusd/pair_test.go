package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rsc.io/qr"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
)

func TestPairCommand(t *testing.T) {
	dir := t.TempDir()
	cfgYAML := "data_dir: " + filepath.Join(dir, "data") + "\nsecurity:\n  hub_urls: [\"https://nexus.home.arpa:7700\"]\n"
	cfg, err := host.ParseConfig([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q, err := newPairing(ctx, cfg, security.PairingOptions{Kind: pairing.KindNode, Label: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := printPairing(&out, q, true); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"nexus://pair?", "k=node", "nexus-node pair '", "safety code", "█"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	// The CA and the hashed secret live in the hub's data dir / database,
	// and the audit log of a running hub is not touched.
	if _, err := os.Stat(filepath.Join(dir, "data", "security", "pki", "ca.crt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "security", "audit.log")); !os.IsNotExist(err) {
		t.Fatalf("audit.log written: %v", err)
	}
	// A second process (the hub) sees the same CA fingerprint.
	q2, err := newPairing(ctx, cfg, security.PairingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if q2.CAFingerprint != q.CAFingerprint || q2.Kind != pairing.KindDevice {
		t.Fatal("CA changed between processes")
	}
	// No hub URL configured → helpful error.
	empty, _ := host.ParseConfig([]byte("data_dir: " + filepath.Join(dir, "d2") + "\n"))
	if _, err := newPairing(ctx, empty, security.PairingOptions{}); err == nil || !strings.Contains(err.Error(), "hub_urls") {
		t.Fatalf("no hub url: %v", err)
	}
}

func TestRenderQR(t *testing.T) {
	c, err := qr.Encode("nexus://pair?v=1", qr.L)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(renderQR(c, false), "\n"), "\n")
	if want := (c.Size + 4 + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines, want %d", len(lines), want)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n != c.Size+4 {
			t.Fatalf("width %d want %d", n, c.Size+4)
		}
	}
	// Light terminal: quiet zone is blank; dark terminal: quiet zone is filled.
	if !strings.HasPrefix(lines[0], "  ") || !strings.HasPrefix(strings.Split(renderQR(c, true), "\n")[0], "██") {
		t.Fatal("quiet zone")
	}
}
