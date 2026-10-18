package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultCmd(t *testing.T) {
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	backups := filepath.Join(dir, "backups")
	os.MkdirAll(filepath.Join(vaultDir, "people"), 0o755)
	os.WriteFile(filepath.Join(vaultDir, "people", "Ana.md"), []byte("---\nid: A\n---\n# Ana\n"), 0o644)
	cfg := filepath.Join(dir, "nexus.yaml")
	os.WriteFile(cfg, []byte("data_dir: "+filepath.Join(dir, "data")+"\nvault:\n  path: "+vaultDir+"\n  backup:\n    dir: "+backups+"\n"), 0o644)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runVaultCmd(args, &out)
		return out.String(), err
	}
	out, err := run("backup", "-config", cfg)
	if err != nil || !strings.Contains(out, "wrote ") {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	out, err = run("list", "-config", cfg)
	if err != nil || !strings.Contains(out, "tar.gz") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = run("verify", "-config", cfg)
	if err != nil || !strings.Contains(out, "OK ") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	ents, _ := os.ReadDir(backups)
	target := filepath.Join(dir, "restored")
	out, err = run("restore", "-config", cfg, "-from", filepath.Join(backups, ents[0].Name()), "-to", target)
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(target, "people", "Ana.md")); err != nil {
		t.Fatal("restored file missing")
	}
	out, err = run("check", "-config", cfg, "-full")
	if err != nil || !strings.Contains(out, "No problems in 1 notes") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(vaultDir, "dup.md"), []byte("---\nid: A\n---\n"), 0o644)
	if out, err := run("check", "-config", cfg); err == nil || !strings.Contains(out, "duplicate_id") {
		t.Fatalf("check with problems: %v\n%s", err, out)
	}
	if _, err := run("bogus"); err == nil {
		t.Fatal("unknown subcommand accepted")
	}
}
