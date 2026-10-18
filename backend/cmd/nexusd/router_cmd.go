package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/llm"
	"github.com/danielpina1/nexus/backend/internal/router/learn"
	"github.com/danielpina1/nexus/backend/internal/sqlitex"
)

// cmdRouter implements `nexusd router eval`: an offline, leave-one-out
// replay of the logged routing decisions (docs/ROUTING.md).
func cmdRouter(args []string) error {
	if len(args) == 0 || args[0] != "eval" {
		return errors.New("usage: nexusd router eval [-config nexus.yaml] [-hash] [-folds N]")
	}
	fs := flag.NewFlagSet("router eval", flag.ContinueOnError)
	cfgPath := fs.String("config", "nexus.yaml", "hub config file")
	hash := fs.Bool("hash", false, "use the built-in hashing embedder instead of the embed model")
	folds := fs.Int("folds", 10, "folds when there are more than 300 examples (else leave-one-out)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := host.LoadConfigFile(*cfgPath)
	if err != nil {
		return err
	}
	var storeCfg struct {
		Path string `yaml:"path"`
	}
	if err := cfg.Section("store", &storeCfg); err != nil {
		return err
	}
	dbPath := storeCfg.Path
	if dbPath == "" {
		dataDir := cfg.DataDir
		if dataDir == "" {
			dataDir = "./data"
		}
		dbPath = filepath.Join(dataDir, "store", "nexus.db")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no hub database at %s (run the hub first): %w", dbPath, err)
	}
	db, err := sqlitex.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	var vaultCfg struct {
		Path string `yaml:"path"`
	}
	if err := cfg.Section("vault", &vaultCfg); err != nil {
		return err
	}
	if vaultCfg.Path == "" {
		vaultCfg.Path = "./vault"
	}
	cands, err := learn.CandidatesFromVault(vaultCfg.Path)
	if err != nil {
		return err
	}
	var lcfg learn.Config
	if err := cfg.Section("router.learn", &lcfg); err != nil {
		return err
	}
	var emb learn.Embedder = learn.HashEmbedder{}
	if !*hash && lcfg.EmbedModel != "hash" {
		var llmCfg llm.Config
		if err := cfg.Section("llm", &llmCfg); err == nil && len(llmCfg.Providers) > 0 {
			if c, err := llm.New(llmCfg); err == nil {
				alias := lcfg.EmbedModel
				if alias == "" {
					alias = "embed"
				}
				pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				_, perr := c.Embed(pctx, alias, []string{"probe"})
				cancel()
				if perr == nil {
					emb = learn.LLMEmbedder(c, alias)
				} else {
					fmt.Fprintf(os.Stderr, "embedding model %q unavailable (%v); using the hashing embedder\n", alias, perr)
				}
			}
		}
	}
	rep, err := learn.Evaluate(ctx, db, emb, cands, lcfg, *folds)
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	return nil
}
