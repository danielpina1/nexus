package vault

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/danielpina1/nexus/backend/internal/vault/index"
)

// Watch reindexes notes edited outside Nexus (Obsidian, git pull, editors).
// Events are debounced; changed notes are reindexed incrementally and
// listeners (agent catalog…) are notified. Directory-level changes
// (renames, deletions of folders) trigger a full incremental Sync. The
// returned stop function blocks until the watcher has exited.
func (v *Vault) Watch(ctx context.Context, debounce time.Duration) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := addTree(w, v.root); err != nil {
		w.Close()
		return nil, err
	}
	if debounce <= 0 {
		debounce = 300 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer w.Close()
		pending := map[string]bool{}
		full := false
		var timer *time.Timer
		var fire <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				v.log.Warn("vault: watcher error", "err", err)
				full = true // events may have been dropped
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				rel, err := filepath.Rel(v.root, ev.Name)
				if err != nil {
					continue
				}
				rel = filepath.ToSlash(rel)
				if hidden(rel) {
					continue
				}
				newDir := false
				if ev.Op&fsnotify.Create != 0 {
					if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
						// Files may already exist inside it: rescan.
						_ = addTree(w, ev.Name)
						newDir = true
					}
				}
				switch {
				case newDir:
					full = true
				case index.IsNotePath(rel):
					pending[rel] = true
				case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && filepath.Ext(rel) == "":
					full = true // probably a directory
				default:
					continue
				}
				if timer == nil {
					timer = time.NewTimer(debounce)
					fire = timer.C
				} else {
					timer.Reset(debounce)
				}
			case <-fire:
				timer, fire = nil, nil
				v.applyChanges(ctx, pending, full)
				pending, full = map[string]bool{}, false
			}
		}
	}()
	return func() { cancel(); wg.Wait() }, nil
}

func (v *Vault) applyChanges(ctx context.Context, pending map[string]bool, full bool) {
	if full {
		if _, err := v.Sync(ctx); err != nil {
			v.log.Warn("vault: sync after external change", "err", err)
		}
		return
	}
	var changed []string
	for rel := range pending {
		ok, err := v.ix.UpsertFile(ctx, v.root, rel)
		if err != nil {
			v.log.Warn("vault: reindex", "path", rel, "err", err)
			continue
		}
		if ok {
			changed = append(changed, rel)
		}
	}
	if len(changed) > 0 {
		v.notify(changed)
	}
}

func hidden(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." {
			return true
		}
	}
	return false
}

func addTree(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}
