package anthrex

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// SocketPath returns the daemon socket, following crates/proto/src/paths.rs:
//
//   - override (Nexus config `anthrex.socket`) when set;
//   - $ANTHREX_SOCKET when set;
//   - macOS: $TMPDIR/anthrex-<uid>/daemon.sock ($TMPDIR defaults to /tmp);
//   - elsewhere: $XDG_RUNTIME_DIR/anthrex/daemon.sock, or
//     /tmp/anthrex-<uid>/daemon.sock without a runtime dir.
//
// The Rust side uses dirs::runtime_dir(), which on Linux is $XDG_RUNTIME_DIR
// (only when absolute) and None elsewhere.
func SocketPath(override string) string {
	return socketPath(override, os.Getenv, runtime.GOOS, os.Getuid())
}

func socketPath(override string, getenv func(string) string, goos string, uid int) string {
	if override != "" {
		return expandHome(override)
	}
	if p := getenv("ANTHREX_SOCKET"); p != "" {
		return p
	}
	return filepath.Join(socketDir(getenv, goos, uid), "daemon.sock")
}

func socketDir(getenv func(string) string, goos string, uid int) string {
	u := strconv.Itoa(uid)
	if goos == "darwin" {
		tmp := getenv("TMPDIR")
		if tmp == "" {
			tmp = "/tmp"
		}
		return filepath.Join(tmp, "anthrex-"+u)
	}
	if goos == "linux" {
		if rt := getenv("XDG_RUNTIME_DIR"); rt != "" && filepath.IsAbs(rt) {
			return filepath.Join(rt, "anthrex")
		}
	}
	return "/tmp/anthrex-" + u
}

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
