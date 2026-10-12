package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

// chromeNames are looked up on PATH, in order.
var chromeNames = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
	"headless_shell", "microsoft-edge",
}

// FindChrome returns a Chrome/Chromium executable: configured path, then
// $CHROME_PATH, then PATH, then well-known install locations (macOS app
// bundles, Playwright's /opt/pw-browsers and ~/.cache/ms-playwright).
// It returns "" when none is found.
func FindChrome(configured string) string {
	for _, p := range []string{configured, os.Getenv("CHROME_PATH")} {
		if p == "" {
			continue
		}
		if fp, err := exec.LookPath(p); err == nil {
			return fp
		}
	}
	for _, n := range chromeNames {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var cands []string
	if runtime.GOOS == "darwin" {
		cands = append(cands,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	cands = append(cands, "/usr/bin/google-chrome", "/usr/bin/chromium", "/snap/bin/chromium")
	roots := []string{"/opt/pw-browsers"}
	if v := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); v != "" && v != "0" {
		roots = append([]string{v}, roots...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".cache", "ms-playwright"))
	}
	for _, root := range roots {
		cands = append(cands, playwrightChromes(root)...)
	}
	for _, c := range cands {
		if isExec(c) {
			return c
		}
	}
	return ""
}

// playwrightChromes lists chromium binaries in a Playwright browsers dir,
// newest first, full Chromium before headless_shell.
func playwrightChromes(root string) []string {
	var full, shell []string
	for _, pat := range []string{
		filepath.Join(root, "chromium-*", "chrome-linux*", "chrome"),
		filepath.Join(root, "chromium-*", "chrome-mac*", "Chromium.app", "Contents", "MacOS", "Chromium"),
		filepath.Join(root, "chromium-*", "chrome-mac*", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"),
	} {
		m, _ := filepath.Glob(pat)
		full = append(full, m...)
	}
	m, _ := filepath.Glob(filepath.Join(root, "chromium_headless_shell-*", "chrome-*", "headless_shell"))
	shell = append(shell, m...)
	sort.Sort(sort.Reverse(sort.StringSlice(full)))
	sort.Sort(sort.Reverse(sort.StringSlice(shell)))
	return append(full, shell...)
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
