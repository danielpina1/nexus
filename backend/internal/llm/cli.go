package llm

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// ErrCheckFailed is returned by RunCLI when `llm check` found failures.
var ErrCheckFailed = errors.New("llm check failed")

// CLIOptions wires RunCLI into a binary.
type CLIOptions struct {
	Prog          string // "nexusd" or "nexus-node"
	Section       string // config section: "llm" (hub) or "node.llm" (node)
	DefaultConfig string // default -config value
	// Load decodes the config section from the file at path.
	Load   func(path, section string, out any) error
	Stdout io.Writer
	Stderr io.Writer
	// Getenv overrides environment lookup (tests).
	Getenv func(string) string
	// ClientOptions are passed to New (tests).
	ClientOptions []Option
}

func (o *CLIOptions) usage() string {
	return fmt.Sprintf(`usage:
  %[1]s llm check  [-config %[2]s] [-alias NAME] [-quick] [-timeout 90s]
      probe every provider and alias: reachability, auth, model list,
      chat, tool calling, JSON mode, streaming, embeddings
  %[1]s llm models [-config %[2]s] [-provider NAME]
      list the models each provider serves and the aliases pointing at them
  %[1]s llm status [-config %[2]s]
      show the configured providers and how every alias resolves
`, o.Prog, o.DefaultConfig)
}

// RunCLI implements the `llm` subcommand of nexusd and nexus-node.
func RunCLI(ctx context.Context, args []string, o CLIOptions) error {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if len(args) == 0 {
		fmt.Fprint(o.Stderr, o.usage())
		return errors.New("llm: missing subcommand")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "check", "models", "status":
	case "help", "-h", "--help":
		fmt.Fprint(o.Stdout, o.usage())
		return nil
	default:
		fmt.Fprint(o.Stderr, o.usage())
		return fmt.Errorf("llm: unknown subcommand %q", sub)
	}

	fs := flag.NewFlagSet("llm "+sub, flag.ContinueOnError)
	fs.SetOutput(o.Stderr)
	cfgPath := fs.String("config", o.DefaultConfig, "config file")
	alias := fs.String("alias", "", "check: probe only this alias")
	quick := fs.Bool("quick", false, "check: chat probe only (skip tools/json/stream)")
	timeout := fs.Duration("timeout", 90*time.Second, "check: timeout per probe")
	provider := fs.String("provider", "", "models: only this provider")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	c, err := o.client(*cfgPath)
	if err != nil {
		return err
	}
	switch sub {
	case "check":
		fmt.Fprintf(o.Stdout, "%s llm check — %s (section %s)\n\n", o.Prog, *cfgPath, o.Section)
		rep := Check(ctx, c, CheckOptions{Alias: *alias, Quick: *quick, Timeout: *timeout})
		rep.Print(o.Stdout)
		if rep.Failed() {
			return ErrCheckFailed
		}
		return nil
	case "models":
		return o.models(ctx, c, *provider)
	default:
		return o.status(ctx, c)
	}
}

func (o *CLIOptions) client(path string) (*Client, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	var cfg Config
	if err := o.Load(path, o.Section, &cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("no providers configured under %q in %s (see docs/MODELS.md)", o.Section, path)
	}
	// Diagnostics should be quick and honest: one retry at most.
	one := 1
	if cfg.MaxRetries == nil || *cfg.MaxRetries > one {
		cfg.MaxRetries = &one
	}
	opts := []Option{
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithBackoff(func(int) time.Duration { return 500 * time.Millisecond }),
	}
	if o.Getenv != nil {
		opts = append(opts, WithGetenv(o.Getenv))
	}
	opts = append(opts, o.ClientOptions...)
	return New(cfg, opts...)
}

func (o *CLIOptions) models(ctx context.Context, c *Client, only string) error {
	if only != "" && !c.HasProvider(only) {
		return fmt.Errorf("unknown provider %q", only)
	}
	var failed bool
	byTarget := map[Target][]string{}
	if c.cfg.AliasesAuto {
		dctx, cancel := context.WithTimeout(ctx, listTimeout)
		_, _ = c.Discover(dctx)
		cancel()
	}
	for _, a := range c.AliasTable(ctx) {
		for i, t := range a.Targets {
			label := a.Name
			switch {
			case i > 0:
				label += " (fallback)"
			case a.Auto:
				label += " (auto)"
			}
			byTarget[t] = append(byTarget[t], label)
		}
	}
	tw := tabwriter.NewWriter(o.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tALIASES")
	for _, p := range c.providerOrder() {
		if only != "" && p != only {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, listTimeout)
		models, err := c.ListModels(lctx, p)
		cancel()
		if err != nil {
			failed = true
			_, detail, hint := diagnose(c, p, "", err)
			if hint != "" {
				detail += " — " + hint
			}
			fmt.Fprintf(tw, "%s\t✗ %s\t\n", p, detail)
			continue
		}
		if len(models) == 0 {
			fmt.Fprintf(tw, "%s\t(no models)\t\n", p)
		}
		for _, m := range models {
			labels := byTarget[Target{p, m.ID}]
			sort.Strings(labels)
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p, m.ID, strings.Join(labels, ", "))
		}
	}
	tw.Flush()
	if failed {
		return errors.New("llm: some providers could not be listed")
	}
	return nil
}

func (o *CLIOptions) status(ctx context.Context, c *Client) error {
	tw := tabwriter.NewWriter(o.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tAPI\tBASE URL\tDEFAULT")
	for _, p := range c.Status() {
		def := ""
		if p.Name == c.defaultProvider {
			def = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.API, p.BaseURL, def)
	}
	tw.Flush()
	fmt.Fprintln(o.Stdout)
	tw = tabwriter.NewWriter(o.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ALIAS\tRESOLVES TO\tSOURCE")
	for _, a := range c.AliasTable(ctx) {
		if a.Err != nil {
			fmt.Fprintf(tw, "%s\t✗ %s\t\n", a.Name, a.Err)
			continue
		}
		parts := make([]string, len(a.Targets))
		for i, t := range a.Targets {
			parts[i] = t.String()
		}
		src := "configured"
		if a.Auto {
			src = "auto"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Name, strings.Join(parts, " → "), src)
	}
	return tw.Flush()
}
