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

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
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
				case index.IsNotePath(rel), index.IsAttachmentPath(rel):
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
	ctx = WithOrigin(ctx, "external")
	if full {
		if _, err := v.Sync(ctx); err != nil {
			v.log.Warn("vault: sync after external change", "err", err)
		}
		v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RESYNC, "", "", "")
		return
	}
	var changed []string
	for rel := range pending {
		if !index.IsNotePath(rel) {
			if _, err := v.ix.UpsertAttachmentFile(ctx, v.root, rel); err != nil {
				v.log.Warn("vault: reindex attachment", "path", rel, "err", err)
			}
			continue
		}
		before, wasIndexed := v.ix.Get(ctx, rel)
		p := filepath.Join(v.root, filepath.FromSlash(rel))
		raw, rerr := os.ReadFile(p)
		if rerr != nil && !wasIndexed {
			continue // created and deleted again, or our own delete
		}
		ok, err := v.ix.UpsertFile(ctx, v.root, rel)
		if err != nil {
			v.log.Warn("vault: reindex", "path", rel, "err", err)
			continue
		}
		if !ok {
			continue
		}
		changed = append(changed, rel)
		id := ""
		if e, ok := v.ix.Get(ctx, rel); ok {
			id = e.ID
		} else if before != nil {
			id = before.ID
		}
		switch {
		case rerr != nil:
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_DELETED, rel, "", id)
			if v.snaps != nil {
				_ = v.snaps.record(snapEntry{Path: rel, At: v.now().UnixMilli(), Message: "deleted outside Nexus"}, nil)
			}
		case wasIndexed:
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_UPDATED, rel, "", id)
		default:
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED, rel, "", id)
		}
		if rerr == nil && v.snaps != nil {
			// Undo covers edits made in Obsidian or other editors too.
			_ = v.snaps.record(snapEntry{Path: rel, At: v.now().UnixMilli(), Message: "edited outside Nexus", PH: v.plainHash(raw)}, raw)
		}
		if rerr == nil {
			if plain, err := v.decode(raw); err == nil {
				v.recent.put(index.Hash(plain), plain)
			}
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
