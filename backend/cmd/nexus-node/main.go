// Command nexus-node is the Nexus node daemon.
//
//	nexus-node run [-config node.yaml]   connect to the hub and execute runs
//	nexus-node runtimes [-config ...]    list runtimes and their availability
//	nexus-node llm check|models|status [-config node.yaml]
//	nexus-node version
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/kernel"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	node.Version = version
	// Relative paths in node.yaml are relative to the file, wherever the
	// command is run from.
	rest, err := host.EnterConfigDir(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexus-node:", err)
		os.Exit(1)
	}
	os.Args = append(os.Args[:2], rest...)
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "runtimes":
		err = runtimes(os.Args[2:])
	case "pair":
		err = pair(os.Args[2:])
	case "llm":
		err = cmdLLM(os.Args[2:])
	case "version":
		fmt.Println("nexus-node", version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexus-node:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  nexus-node run [-config node.yaml] [-v]
  nexus-node runtimes [-config node.yaml]
  nexus-node pair [-config node.yaml] [-id NAME] '<nexus://pair URL>'
  nexus-node llm check|models|status [-config node.yaml]
  nexus-node version`)
}

func load(args []string, name string) (*host.Process, *slog.Logger, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("config", "node.yaml", "config file")
	verbose := fs.Bool("v", false, "debug logging")
	_ = fs.Parse(args)
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	cfg, err := host.LoadConfigFile(*cfgPath)
	if err != nil {
		return nil, nil, err
	}
	p := host.New(host.Options{Side: kernel.SideNode, Config: cfg, Logger: log})
	if err := p.Init(context.Background()); err != nil {
		return nil, nil, err
	}
	return p, log, nil
}

func run(args []string) error {
	p, log, err := load(args, "run")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := p.Start(ctx); err != nil {
		return err
	}
	log.Info("nexus-node running", "version", version, "modules", p.ModuleNames(), "runtimes", p.Runtimes().Names())
	<-ctx.Done()
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.Stop(sctx)
}

func runtimes(args []string) error {
	p, _, err := load(args, "runtimes")
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RUNTIME\tAVAILABLE\tVERSION")
	for _, name := range p.Runtimes().Names() {
		rt, _ := p.Runtimes().Get(name)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		v, ok := rt.Available(ctx)
		cancel()
		fmt.Fprintf(tw, "%s\t%v\t%s\n", name, ok, v)
	}
	return tw.Flush()
}
