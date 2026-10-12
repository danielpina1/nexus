// Command nexus-browser-login opens a visible Chrome window on a browser
// agent's persistent profile so you can log in to a site once (cookies stay
// in the profile and later headless runs reuse them). It does not need
// nexus-node to be running — but stop runs of that agent first: Chrome cannot
// open one profile twice.
//
//	go run ./internal/runtime/browser/cmd/nexus-browser-login \
//	    -data-dir ./data -profile grocery-continente -url https://www.continente.pt/
//
// Log in, tick "remember me" if offered, then close the window.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/danielpina1/nexus/backend/internal/runtime/browser"
)

func main() {
	dataDir := flag.String("data-dir", "./data", "nexus-node data_dir (profiles live in <data_dir>/runtime.browser/profiles)")
	profilesDir := flag.String("profiles-dir", "", "override runtime.browser.profiles_dir")
	profile := flag.String("profile", "", "profile name: the agent id (e.g. grocery-continente) or runtime.browser.agents.<id>.profile")
	url := flag.String("url", "", "page to open (e.g. https://www.continente.pt/)")
	chrome := flag.String("chrome", "", "Chrome/Chromium binary (default: auto-detect)")
	flag.Parse()
	if *profile == "" {
		fmt.Fprintln(os.Stderr, "usage: nexus-browser-login -profile <agent-id> [-url URL] [-data-dir DIR | -profiles-dir DIR]")
		os.Exit(2)
	}
	dir := *profilesDir
	if dir == "" {
		dir = filepath.Join(*dataDir, "runtime.browser", "profiles")
	}
	prof, err := filepath.Abs(browser.ProfileDir(dir, *profile))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := browser.FindChrome(*chrome)
	if bin == "" {
		fmt.Fprintln(os.Stderr, "no Chrome/Chromium found; pass -chrome or set CHROME_PATH")
		os.Exit(1)
	}
	if err := os.MkdirAll(prof, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	args := []string{"--user-data-dir=" + prof, "--no-first-run", "--no-default-browser-check", "--password-store=basic", "--use-mock-keychain"}
	if os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	if *url != "" {
		args = append(args, *url)
	}
	fmt.Printf("Opening %s with profile %s\nLog in, then close the window.\n", bin, prof)
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chrome:", err)
		os.Exit(1)
	}
	fmt.Println("Profile saved. Headless runs of this agent will reuse the session.")
}
