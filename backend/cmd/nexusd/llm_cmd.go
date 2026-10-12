package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/llm"
)

// cmdLLM implements `nexusd llm check|models|status` for the hub's "llm" section.
func cmdLLM(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return llm.RunCLI(ctx, args, llm.CLIOptions{
		Prog:          "nexusd",
		Section:       "llm",
		DefaultConfig: "nexus.yaml",
		Load:          loadLLMSection,
	})
}

func loadLLMSection(path, section string, out any) error {
	cfg, err := host.LoadConfigFile(path)
	if err != nil {
		return err
	}
	return cfg.Section(section, out)
}
