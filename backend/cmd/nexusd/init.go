package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/danielpina1/nexus/backend/configs"
	"github.com/danielpina1/nexus/backend/internal/hubserver"
)

func cmdInit(args []string) error {
	fset := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fset.String("dir", ".", "directory to initialise")
	tmpl := fset.String("template", "", "vault template directory (default: search for vault-template/ nearby)")
	force := fset.Bool("force", false, "overwrite an existing nexus.yaml")
	var hubURLs urlList
	fset.Var(&hubURLs, "hub-url", "URL phones and nodes use to reach the hub, e.g. https://nexus.home.arpa:7700 (repeatable or comma-separated)")
	listen := fset.String("listen", hubserver.DefaultListen, `server.listen address, e.g. "10.20.0.10:7700"`)
	_ = fset.Parse(args)

	nc, err := newNetConfig(hubURLs, *listen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(*dir, "nexus.yaml")
	wrote, err := writeConfig(cfgPath, *force, nc)
	if err != nil {
		return err
	}
	if err := initVault(filepath.Join(*dir, "vault"), *tmpl); err != nil {
		return err
	}
	if wrote {
		printNextSteps(os.Stdout, cfgPath, nc)
	}
	return nil
}

// writeConfig writes the embedded example with a freshly generated dev token
// and the network settings; it reports whether the file was written.
func writeConfig(path string, force bool, nc netConfig) (bool, error) {
	if _, err := os.Stat(path); err == nil && !force {
		fmt.Printf("✓ %s already exists (use -force to overwrite)\n", path)
		return false, nil
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return false, err
	}
	token := hex.EncodeToString(tok)
	body := bytes.Replace(configs.NexusExample, []byte("dev_token: CHANGE_ME"), []byte("dev_token: "+token), 1)
	body, err := nc.apply(body)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return false, err
	}
	fmt.Printf("✓ wrote %s\n  dev token: %s\n  export NEXUS_TOKEN=%s\n", path, token, token)
	return true, nil
}

// initVault copies the vault template into dst unless dst already exists.
func initVault(dst, tmpl string) error {
	if _, err := os.Stat(dst); err == nil {
		fmt.Printf("✓ %s already exists, leaving it alone\n", dst)
		return nil
	}
	src, err := findTemplate(tmpl)
	if err != nil {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("copy vault template: %w", err)
	}
	fmt.Printf("✓ created %s from %s\n", dst, src)
	return nil
}

// findTemplate returns explicit if set, else the first vault-template/ found
// next to the working directory or the executable.
func findTemplate(explicit string) (string, error) {
	if explicit != "" {
		if fi, err := os.Stat(explicit); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("vault template %q is not a directory", explicit)
		}
		return explicit, nil
	}
	var candidates []string
	for _, up := range []string{".", "..", "../.."} {
		candidates = append(candidates, filepath.Join(up, "vault-template"))
	}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		for _, up := range []string{".", "..", "../.."} {
			candidates = append(candidates, filepath.Join(d, up, "vault-template"))
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("vault-template not found; pass -template /path/to/nexus/vault-template")
}

// copyTree copies regular files and directories from src to dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(p, target)
		default:
			return nil // skip symlinks and special files
		}
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
