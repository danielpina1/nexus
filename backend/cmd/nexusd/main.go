// Command nexusd is the Nexus hub: it loads the hub modules, serves the
// Connect API and the web dashboard.
//
//	nexusd serve   [-config nexus.yaml]
//	nexusd init    [-dir .] [-template path/to/vault-template] [-force]
//	nexusd modules [-config nexus.yaml]
//	nexusd version
package main

import (
	"fmt"
	"os"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `nexusd — the Nexus hub

Usage:
  nexusd serve   [-config nexus.yaml]        run the hub
  nexusd init    [-dir .] [-template DIR]    create nexus.yaml and ./vault
  nexusd modules [-config nexus.yaml]        list hub modules and whether they run
  nexusd version                             print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "init":
		err = cmdInit(args)
	case "modules":
		err = cmdModules(args)
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
