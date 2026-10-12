package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/danielpina1/nexus/backend/internal/host"
	"github.com/danielpina1/nexus/backend/internal/security"
	"github.com/danielpina1/nexus/backend/internal/security/pairing"
	"github.com/danielpina1/nexus/backend/internal/store"
)

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	cfgPath := fs.String("config", "nexus.yaml", "hub config file (same as nexusd serve)")
	kind := fs.String("kind", pairing.KindDevice, "what to pair: device (iPhone/web) or node (a machine running nexus-node)")
	label := fs.String("label", "", "note recorded in the audit log, e.g. \"Daniel's iPhone\"")
	var hubURLs urlList
	fs.Var(&hubURLs, "hub-url", "override security.hub_urls for this pairing (repeatable)")
	light := fs.Bool("light", false, "terminal has a light background (draw dark QR modules)")
	_ = fs.Parse(args)

	if *kind != pairing.KindDevice && *kind != pairing.KindNode {
		return fmt.Errorf("pair: -kind must be %q or %q", pairing.KindDevice, pairing.KindNode)
	}
	cfg, err := host.LoadConfigFile(*cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q, err := newPairing(ctx, cfg, security.PairingOptions{Kind: *kind, Label: *label, HubURLs: hubURLs})
	if err != nil {
		return err
	}
	return printPairing(os.Stdout, q, !*light)
}

// newPairing opens the hub's store and security state (CA, hub key) from
// the config's data dir and issues a pairing. The pending secret is stored
// hashed in the hub database, so this works while nexusd serve is running.
func newPairing(ctx context.Context, cfg *host.Config, o security.PairingOptions) (*pairing.QR, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	var stCfg struct {
		Path string `yaml:"path"`
	}
	if err := cfg.Section("store", &stCfg); err != nil {
		return nil, fmt.Errorf("config store: %w", err)
	}
	if stCfg.Path == "" {
		stCfg.Path = filepath.Join(dataDir, "store", "nexus.db")
	}
	var secCfg security.Config
	if err := cfg.Section("security", &secCfg); err != nil {
		return nil, fmt.Errorf("config security: %w", err)
	}
	secDir := filepath.Join(dataDir, "security")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(stCfg.Path)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	sec, err := security.New(ctx, security.Options{
		Config: secCfg, Store: st, DataDir: secDir,
		Logger:      slog.New(slog.DiscardHandler),
		MemoryAudit: true, // the running hub owns audit.log
	})
	if err != nil {
		return nil, err
	}
	defer sec.Close()
	return sec.NewPairing(ctx, o)
}

// printPairing writes the QR code, the URL and the instructions.
func printPairing(w io.Writer, q *pairing.QR, darkTerminal bool) error {
	u := q.URL()
	exp := time.Unix(q.Expires, 0)
	what := "a device (iPhone / browser)"
	if q.Kind == pairing.KindNode {
		what = "a machine (nexus-node)"
	}
	fmt.Fprintf(w, "Pairing %s — single use, valid for %s (until %s)\n\n",
		what, time.Until(exp).Round(time.Second), exp.Format("15:04:05"))
	code, err := qr.Encode(u, qr.L)
	if err != nil {
		return err
	}
	fmt.Fprint(w, renderQR(code, darkTerminal))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s\n\n", u)
	fmt.Fprintf(w, "Hub URLs:       %s\nCA fingerprint: %s\n\n", strings.Join(q.HubURLs, ", "), q.CAFingerprint)
	if q.Kind == pairing.KindNode {
		fmt.Fprintf(w, "On the machine, run:\n\n  nexus-node pair '%s'\n\n", u)
		fmt.Fprintln(w, "It prints a 6-emoji safety code. Compare it with the hub log line")
		fmt.Fprintln(w, `"node paired" (safety_code=...). If they differ, revoke the node.`)
	} else {
		fmt.Fprintln(w, "Scan with the Nexus iPhone app on your home Wi-Fi (or the VPN).")
		fmt.Fprintln(w, "Both the app and the hub log show a 6-emoji safety code; they must match.")
	}
	return nil
}

// renderQR draws a QR code with Unicode half blocks (two module rows per
// text line) and a 2-module quiet zone. darkTerminal draws the light
// modules (light glyphs on a dark background read as a normal QR code).
func renderQR(c *qr.Code, darkTerminal bool) string {
	const quiet = 2
	dark := func(x, y int) bool {
		b := c.Black(x, y) // false outside the code = quiet zone
		if darkTerminal {
			return !b
		}
		return b
	}
	var sb strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		for x := -quiet; x < c.Size+quiet; x++ {
			top, bot := dark(x, y), dark(x, y+1)
			switch {
			case top && bot:
				sb.WriteString("█")
			case top:
				sb.WriteString("▀")
			case bot:
				sb.WriteString("▄")
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
