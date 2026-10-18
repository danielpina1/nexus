package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/vault"
)

const vaultUsage = `usage:
  nexusd vault backup  [-config nexus.yaml] [-dir DIR] [-push]
                                  write a backup now (works while the hub runs)
  nexusd vault list    [-config nexus.yaml] [-dir DIR]
  nexusd vault verify  [-config nexus.yaml] [-dir DIR] [NAME|FILE ...]
                                  verify backups (default: the newest)
  nexusd vault restore -from FILE -to DIR [-force]
                                  restore a backup into DIR (stop the hub first
                                  when DIR is the live vault)
  nexusd vault check   [-config nexus.yaml] [-full]
                                  read-only integrity check (safe while the hub runs)
`

// cmdVault implements `nexusd vault backup|list|verify|restore|check`.
func cmdVault(args []string) error {
	return runVaultCmd(args, os.Stdout)
}

func runVaultCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(vaultUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("vault "+sub, flag.ContinueOnError)
	fs.SetOutput(out)
	cfgPath := fs.String("config", "nexus.yaml", "hub config file")
	dir := fs.String("dir", "", "backup directory (default vault.backup.dir)")
	push := fs.Bool("push", false, "also push to vault.backup.remote")
	full := fs.Bool("full", false, "full check (content hashes, links, attachments)")
	from := fs.String("from", "", "backup file to restore")
	to := fs.String("to", "", "directory to restore into")
	force := fs.Bool("force", false, "move a non-empty target aside before restoring")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	vc, dataDir, err := loadVaultConfig(*cfgPath)
	if err != nil {
		return err
	}
	backupDir := *dir
	if backupDir == "" {
		backupDir = vault.ExpandHome(vc.Backup.Dir)
	}
	switch sub {
	case "backup":
		if backupDir == "" {
			return errors.New("no backup directory: set vault.backup.dir or pass -dir")
		}
		o := vault.BackupOptions{Dir: backupDir, Retention: vc.Backup.Keep, NoBundle: vc.Backup.Bundle != nil && !*vc.Backup.Bundle, Branch: vc.Backup.Branch}
		if *push {
			if vc.Backup.Remote == "" {
				return errors.New("-push needs vault.backup.remote")
			}
			o.Remote = vc.Backup.Remote
		}
		res, err := vault.CreateBackup(ctx, vc.Path, o)
		if res != nil {
			for _, b := range res.Created {
				fmt.Fprintf(out, "wrote %s (%s, sha256 %s)\n", b.Path, humanSize(b.Size), b.SHA256[:12])
			}
			for _, p := range res.Pruned {
				fmt.Fprintf(out, "pruned %s\n", p)
			}
			if res.PushResult != "" {
				fmt.Fprintf(out, "pushed: %s\n", res.PushResult)
			}
		}
		return err
	case "list":
		if backupDir == "" {
			return errors.New("no backup directory: set vault.backup.dir or pass -dir")
		}
		bs, err := vault.ListBackups(backupDir)
		if err != nil {
			return err
		}
		if len(bs) == 0 {
			fmt.Fprintf(out, "no backups in %s\n", backupDir)
		}
		for _, b := range bs {
			fmt.Fprintf(out, "%s  %-6s  %8s  %s\n", b.Created.Format("2006-01-02 15:04:05"), b.Kind, humanSize(b.Size), b.Name)
		}
		return nil
	case "verify":
		files := fs.Args()
		if len(files) == 0 {
			bs, err := vault.ListBackups(backupDir)
			if err != nil {
				return err
			}
			if len(bs) == 0 {
				return fmt.Errorf("no backups in %q", backupDir)
			}
			for _, b := range bs {
				if b.Created.Equal(bs[0].Created) {
					files = append(files, b.Path)
				}
			}
		}
		bad := 0
		for _, f := range files {
			if !strings.ContainsRune(f, filepath.Separator) && backupDir != "" {
				f = filepath.Join(backupDir, f)
			}
			n, problems, err := vault.VerifyBackup(ctx, f)
			if err != nil {
				return err
			}
			if len(problems) == 0 {
				fmt.Fprintf(out, "OK   %s (%d files)\n", f, n)
				continue
			}
			bad++
			fmt.Fprintf(out, "FAIL %s\n", f)
			for _, p := range problems {
				fmt.Fprintf(out, "     %s\n", p)
			}
		}
		if bad > 0 {
			return fmt.Errorf("%d backup(s) failed verification", bad)
		}
		return nil
	case "restore":
		if *from == "" || *to == "" {
			return errors.New("restore needs -from FILE and -to DIR")
		}
		if abs, _ := filepath.Abs(*to); abs != "" {
			if live, _ := filepath.Abs(vc.Path); live == abs {
				fmt.Fprintln(out, "restoring over the live vault: make sure the hub is stopped")
			}
		}
		aside, err := vault.RestoreBackup(ctx, *from, *to, *force)
		if aside != "" {
			fmt.Fprintf(out, "previous contents moved to %s\n", aside)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "restored %s into %s\n", *from, *to)
		return nil
	case "check":
		o := vault.Options{Root: vc.Path, NoLock: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if len(vc.Encryption.Folders) > 0 {
			if key, err := vc.Encryption.LoadKey(dataDir); err == nil {
				o.Encryption = &vault.EncryptionOptions{Folders: vc.Encryption.Folders, Key: key}
			} else {
				fmt.Fprintf(out, "warning: encryption key unavailable (%v); encrypted notes will be reported unreadable\n", err)
			}
		}
		v, err := vault.Open(o)
		if err != nil {
			return err
		}
		defer v.Close()
		start := time.Now()
		if _, err := v.Sync(ctx); err != nil {
			return err
		}
		rep, err := v.Check(ctx, *full)
		if err != nil {
			return err
		}
		for _, i := range rep.Issues {
			if i.Kind == vault.IssueIndexMissing || i.Kind == vault.IssueIndexOrphan || i.Kind == vault.IssueIndexStale {
				continue // the CLI uses a fresh in-memory index
			}
			fmt.Fprintf(out, "%-7s %-20s %s  %s\n", i.Severity, i.Kind, i.Path, i.Detail)
		}
		fmt.Fprintf(out, "%s (%s)\n", rep.Summary(), time.Since(start).Round(time.Millisecond))
		if rep.Serious() > 0 {
			return fmt.Errorf("%d problem(s) found; run Repair from the dashboard or the VaultService.Repair RPC", rep.Serious())
		}
		return nil
	}
	return errors.New(vaultUsage)
}

func loadVaultConfig(path string) (*vault.Config, string, error) {
	cfg, err := host.LoadConfigFile(path)
	if err != nil {
		return nil, "", err
	}
	vc := &vault.Config{Path: "./vault"}
	if err := cfg.Section("vault", vc); err != nil {
		return nil, "", fmt.Errorf("vault config: %w", err)
	}
	if vc.Path == "" {
		vc.Path = "./vault"
	}
	vc.Path = vault.ExpandHome(vc.Path)
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	return vc, filepath.Join(dataDir, "vault"), nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
