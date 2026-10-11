package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/hubserver"
	"github.com/danielpina1/nexus/backend/kernel"
)

// shutdownTimeout bounds graceful shutdown of the server and modules.
const shutdownTimeout = 15 * time.Second

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "nexus.yaml", "config file (missing file = defaults)")
	debug := fs.Bool("debug", false, "debug logging")
	_ = fs.Parse(args)

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := host.LoadConfigFile(*cfgPath)
	if err != nil {
		return err
	}
	srvCfg, err := hubserver.LoadConfig(cfg)
	if err != nil {
		return fmt.Errorf("config server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	proc := host.New(host.Options{Side: kernel.SideHub, Config: cfg, Logger: log, Mux: mux})
	if err := proc.Init(ctx); err != nil {
		return err
	}
	log.Info("modules initialised", "modules", proc.ModuleNames(), "config", *cfgPath, "version", version)

	auth := hubserver.AuthFrom(proc.Lookup)
	if auth == nil {
		log.Warn("no auth module enabled: the API is unauthenticated")
	}
	srv := hubserver.New(hubserver.Options{Config: srvCfg, Mux: mux, Logger: log, Auth: auth, Version: version})
	if err := srv.Listen(); err != nil {
		return err
	}
	if err := proc.Start(ctx); err != nil {
		return err
	}
	errc := srv.Serve()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case serveErr = <-errc:
		log.Error("server stopped", "err", serveErr)
	}
	stop() // a second signal kills the process immediately

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("server shutdown", "err", err)
	}
	if err := proc.Stop(sctx); err != nil {
		log.Warn("module shutdown", "err", err)
	}
	log.Info("bye")
	return serveErr
}
