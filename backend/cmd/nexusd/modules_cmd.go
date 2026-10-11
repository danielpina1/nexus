package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/kernel"
)

func cmdModules(args []string) error {
	fs := flag.NewFlagSet("modules", flag.ExitOnError)
	cfgPath := fs.String("config", "nexus.yaml", "config file")
	_ = fs.Parse(args)

	cfg, err := host.LoadConfigFile(*cfgPath)
	if err != nil {
		return err
	}
	enabled := map[string]bool{}
	infos, resolveErr := host.New(host.Options{Side: kernel.SideHub, Config: cfg}).EnabledModules()
	for _, m := range infos {
		enabled[m.Name] = true
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODULE\tENABLED\tREQUIRES\tDESCRIPTION")
	for _, m := range kernel.RegisteredModules(kernel.SideHub) {
		req := strings.Join(m.Requires, ",")
		if req == "" {
			req = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Name, yesNo(enabled[m.Name]), req, m.Description)
	}
	w.Flush()
	if resolveErr != nil {
		return fmt.Errorf("configuration is invalid: %w", resolveErr)
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
