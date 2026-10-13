package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnterConfigDir(t *testing.T) {
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	dir := t.TempDir()
	got, err := EnterConfigDir([]string{"-v", "-config", filepath.Join(dir, "node.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if got[2] != "node.yaml" {
		t.Fatalf("args = %v", got)
	}
	cwd, _ := os.Getwd()
	if r, _ := filepath.EvalSymlinks(cwd); r != mustEval(t, dir) {
		t.Fatalf("cwd = %s, want %s", cwd, dir)
	}
	got, _ = EnterConfigDir([]string{"--config=" + filepath.Join(dir, "x.yaml")})
	if got[0] != "--config=x.yaml" {
		t.Fatalf("args = %v", got)
	}
	got, _ = EnterConfigDir([]string{"run"})
	if got[0] != "run" {
		t.Fatalf("args = %v", got)
	}
}

func mustEval(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
