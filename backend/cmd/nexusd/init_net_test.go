package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/hubserver"
	"github.com/danielpina1/nexus/backend/internal/security"
)

func TestInitHubURLFlags(t *testing.T) {
	tmpl := t.TempDir()
	dir := t.TempDir()
	err := cmdInit([]string{"-dir", dir, "-template", tmpl,
		"-hub-url", "https://nexus.home.arpa:7700,https://10.20.0.10:7700",
		"-hub-url", "https://hub.vpn.example:7700/", "-listen", "10.20.0.10:7700"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := host.LoadConfigFile(filepath.Join(dir, "nexus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := hubserver.LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Listen != "10.20.0.10:7700" || !srv.TLS.Auto {
		t.Fatalf("server: %+v", srv)
	}
	var sec security.Config
	if err := cfg.Section("security", &sec); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sec.HubURLs, []string{"https://nexus.home.arpa:7700", "https://10.20.0.10:7700", "https://hub.vpn.example:7700"}) {
		t.Fatalf("hub_urls %v", sec.HubURLs)
	}
	if !slices.Equal(sec.ServerNames, []string{"nexus.home.arpa", "10.20.0.10", "hub.vpn.example", "localhost", "127.0.0.1"}) {
		t.Fatalf("server_names %v", sec.ServerNames)
	}
	if sec.DevToken == "" || sec.DevToken == "CHANGE_ME" {
		t.Fatal("dev token")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "nexus.yaml"))
	if !bytes.Contains(raw, []byte("# --- Security")) {
		t.Fatal("comments lost")
	}
}

func TestInitDefaultsAndErrors(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit([]string{"-dir", dir, "-template", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := host.LoadConfigFile(filepath.Join(dir, "nexus.yaml"))
	srv, _ := hubserver.LoadConfig(cfg)
	var sec security.Config
	_ = cfg.Section("security", &sec)
	if srv.Listen != ":7700" || !srv.TLS.Auto || !slices.Equal(sec.HubURLs, []string{DefaultHubURL}) {
		t.Fatalf("defaults: %+v %+v", srv, sec)
	}

	// http:// hub URLs (dev) turn TLS off.
	nc, err := newNetConfig([]string{"http://localhost:7700"}, "")
	if err != nil || nc.TLS {
		t.Fatalf("http: %+v %v", nc, err)
	}
	for _, bad := range [][]string{{"ftp://x"}, {"https://"}, {"https://h:7700/path"}, {"https://a:7700", "http://b:7700"}, {"https://h:99999"}} {
		if _, err := newNetConfig(bad, ":7700"); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if _, err := newNetConfig(nil, "7700"); err == nil {
		t.Error("bad listen accepted")
	}
	var out strings.Builder
	printNextSteps(&out, "nexus.yaml", nc)
	if !strings.Contains(out.String(), "nexus-node pair") || !strings.Contains(out.String(), "WireGuard") {
		t.Fatal(out.String())
	}
}
