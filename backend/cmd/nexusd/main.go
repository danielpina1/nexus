// Command nexusd is the Nexus hub: it loads the hub modules, serves the
// Connect API and the web dashboard.
//
//	nexusd serve   [-config nexus.yaml]
//	nexusd init    [-dir .] [-template path/to/vault-template] [-force]
//	nexusd modules [-config nexus.yaml]
//	nexusd llm     check|models|status [-config nexus.yaml]
//	nexusd router  eval [-config nexus.yaml] [-hash] [-folds 10]
//	nexusd version
package main

import (
	"fmt"
	"github.com/danielpina1/nexus/backend/internal/host"
	"os"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `nexusd — the Nexus hub

Usage:
  nexusd serve   [-config nexus.yaml]        run the hub
  nexusd init    [-dir .] [-template DIR] [-hub-url URL] [-listen ADDR]
                                             create nexus.yaml and ./vault
  nexusd pair    [-kind device|node] [-label NAME] [-config nexus.yaml]
                                             issue a 5-minute pairing QR / URL
  nexusd modules [-config nexus.yaml]        list hub modules and whether they run
  nexusd llm check|models|status [-config nexus.yaml]
                                             diagnose model providers and aliases
  nexusd router eval [-config nexus.yaml] [-hash] [-folds N]
                                             replay logged routing decisions (accuracy, calibration)
  nexusd version                             print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd != "init" { // init has its own -dir; others resolve paths from the config's folder
		var err error
		if args, err = host.EnterConfigDir(args); err != nil {
			fmt.Fprintln(os.Stderr, "nexusd:", err)
			os.Exit(1)
		}
	}
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "init":
		err = cmdInit(args)
	case "modules":
		err = cmdModules(args)
	case "pair":
		err = cmdPair(args)
	case "llm":
		err = cmdLLM(args)
	case "router":
		err = cmdRouter(args)
	case "version", "-v", "--version":
		fmt.Println("nexusd", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "nexusd: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexusd:", err)
		os.Exit(1)
	}
}
