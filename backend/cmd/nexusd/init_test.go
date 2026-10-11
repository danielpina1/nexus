package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit(t *testing.T) {
	tmpl := t.TempDir()
	os.MkdirAll(filepath.Join(tmpl, "agents"), 0o755)
	os.MkdirAll(filepath.Join(tmpl, "inbox"), 0o755)
	os.WriteFile(filepath.Join(tmpl, "agents", "a.md"), []byte("# a"), 0o644)

	dir := filepath.Join(t.TempDir(), "hub")
	if err := cmdInit([]string{"-dir", dir, "-template", tmpl}); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "nexus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "dev_token: CHANGE_ME") || !strings.Contains(string(cfg), "dev_token: ") {
		t.Fatal("dev token not generated")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "vault", "agents", "a.md")); err != nil || string(b) != "# a" {
		t.Fatalf("vault not copied: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "vault", "inbox")); err != nil || !fi.IsDir() {
		t.Fatal("empty dir not copied")
	}
	// Idempotent: keeps existing files.
	if err := cmdInit([]string{"-dir", dir, "-template", tmpl}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "nexus.yaml"))
	if string(again) != string(cfg) {
		t.Fatal("config overwritten without -force")
	}
	if err := cmdInit([]string{"-dir", t.TempDir(), "-template", filepath.Join(tmpl, "missing")}); err == nil {
		t.Fatal("missing template accepted")
	}
}
