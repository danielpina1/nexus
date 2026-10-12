package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Dir is where workflow notes live inside the vault.
const Dir = "workflows"

// Catalog holds the parsed workflows/*.md notes. It mirrors the vault's
// agent catalog: invalid notes are kept with Errors set, duplicate ids are
// reported on the later file (by name).
type Catalog struct {
	dir func() string // resolved lazily (the vault may start after us)
	log *slog.Logger

	mu   sync.RWMutex
	byID map[string]*Workflow
	list []*Workflow
	sig  string
}

// NewCatalog returns a catalog reading from dir() (empty = no workflows).
func NewCatalog(dir func() string, log *slog.Logger) *Catalog {
	if log == nil {
		log = slog.Default()
	}
	return &Catalog{dir: dir, log: log, byID: map[string]*Workflow{}}
}

// signature fingerprints the directory listing (names, sizes, mtimes).
func signature(dir string, ents []os.DirEntry) string {
	h := sha256.New()
	h.Write([]byte(dir))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "\x00%s\x00%d\x00%d", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isNote(e os.DirEntry) bool {
	n := e.Name()
	return !e.IsDir() && strings.EqualFold(filepath.Ext(n), ".md") && !strings.HasPrefix(n, ".")
}

// Reload re-reads the directory when it changed (or always with force).
// It reports whether the catalog changed.
func (c *Catalog) Reload(force bool) (bool, error) {
	dir := ""
	if c.dir != nil {
		dir = c.dir()
	}
	var ents []os.DirEntry
	if dir != "" {
		all, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		for _, e := range all {
			if isNote(e) {
				ents = append(ents, e)
			}
		}
	}
	sig := signature(dir, ents)
	c.mu.RLock()
	same := sig == c.sig
	c.mu.RUnlock()
	if same && !force {
		return false, nil
	}
	byID := map[string]*Workflow{}
	var list []*Workflow
	for _, e := range ents {
		rel := Dir + "/" + e.Name()
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			c.log.Warn("workflow: read note", "path", rel, "err", err)
			continue
		}
		w := Parse(rel, b)
		if prev, dup := byID[w.ID]; dup {
			w.Errors = append(w.Errors, fmt.Sprintf("duplicate id %q (also in %s)", w.ID, prev.Path))
			list = append(list, w)
			continue
		}
		if len(w.Errors) > 0 {
			c.log.Warn("workflow: invalid note", "path", rel, "errors", w.Errors)
		}
		byID[w.ID] = w
		list = append(list, w)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	c.mu.Lock()
	c.byID, c.list, c.sig = byID, list, sig
	c.mu.Unlock()
	return true, nil
}

// Watch polls the directory every interval and calls onChange after a reload.
func (c *Catalog) Watch(ctx context.Context, every time.Duration, onChange func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			changed, err := c.Reload(false)
			if err != nil {
				c.log.Warn("workflow: reload", "err", err)
				continue
			}
			if changed && onChange != nil {
				onChange()
			}
		}
	}
}

// Get returns a workflow by id (valid or not).
func (c *Catalog) Get(id string) (*Workflow, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	w, ok := c.byID[id]
	return w, ok
}

// List returns every parsed workflow note, sorted by id.
func (c *Catalog) List() []*Workflow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]*Workflow(nil), c.list...)
}
