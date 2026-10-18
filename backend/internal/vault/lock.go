package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrLocked is returned by Open when another process holds the vault's
// writer lock.
var ErrLocked = errors.New("vault: locked by another process")

// writerLock is the single-writer lock: an exclusive flock(2) on
// <vault>/.nexus/lock held for the lifetime of the Vault. The kernel drops
// it when the process dies, so a crashed hub never leaves a stale lock.
type writerLock struct {
	f *os.File
}

func acquireLock(root string) (*writerLock, error) {
	dir := filepath.Join(root, stateDirName)
	if err := mkdirAllSync(dir); err != nil {
		return nil, err
	}
	ensureStateGitignore(dir)
	p := filepath.Join(dir, "lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		b, _ := os.ReadFile(p)
		f.Close()
		if errors.Is(err, ErrLocked) {
			owner := strings.TrimSpace(string(b))
			if owner != "" {
				return nil, fmt.Errorf("%w (pid %s holds %s)", ErrLocked, owner, p)
			}
			return nil, fmt.Errorf("%w (%s)", ErrLocked, p)
		}
		return nil, err
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return &writerLock{f: f}, nil
}

func (l *writerLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}

// ensureStateGitignore keeps runtime state out of git.
func ensureStateGitignore(dir string) {
	p := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = writeAtomic(p, []byte("# Nexus runtime state (lock, write-ahead journal) — not versioned.\nlock\njournal/\n"))
}
