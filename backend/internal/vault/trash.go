package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Trash
//
// Deleting a note (or attachment) moves it to <vault>/.trash/<item id>/<original
// path>, with <vault>/.trash/<item id>.json holding the original path, title
// and deletion time. .trash/ is Obsidian's trash folder too; the vault keeps
// it out of git (.trash/.gitignore) — with git, the deletion itself is a
// commit, so History still has every version. Items are purged after
// vault.trash_days (default 30).

const trashDir = ".trash"

// TrashItem is a trashed file.
type TrashItem struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Title     string    `json:"title,omitempty"`
	DeletedAt time.Time `json:"deleted_at"`
	Size      int64     `json:"size"`
	RunID     string    `json:"run_id,omitempty"`
}

// Proto converts an item to its API form.
func (t TrashItem) Proto() *nexusv1.TrashItem {
	return &nexusv1.TrashItem{Id: t.ID, Path: t.Path, Title: t.Title, DeletedAt: timestampOf(t.DeletedAt), SizeBytes: t.Size}
}

func (v *Vault) trashRoot() string { return filepath.Join(v.root, trashDir) }

func (v *Vault) ensureTrash() error {
	if err := mkdirAllSync(v.trashRoot()); err != nil {
		return err
	}
	gi := filepath.Join(v.trashRoot(), ".gitignore")
	if !fileExists(gi) {
		return writeAtomic(gi, []byte("# Nexus/Obsidian trash: not versioned (deletions are commits).\n*\n"))
	}
	return nil
}

// cleanAnyPath validates a note or attachment path.
func (v *Vault) cleanAnyPath(ctx context.Context, idOrPath string) (string, error) {
	if e, ok := v.ix.Lookup(ctx, idOrPath); ok {
		return e.Path, nil
	}
	c, err := CleanPath(idOrPath)
	if err != nil {
		return "", err
	}
	if path.Ext(c) == "" {
		c += ".md"
	}
	return c, nil
}

// Delete moves a note or attachment to the trash. baseCommit (optional)
// guards against deleting a note that changed since it was read.
func (v *Vault) Delete(ctx context.Context, idOrPath, baseCommit, msg string) (*TrashItem, error) {
	rel, err := v.cleanAnyPath(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	p, err := v.abs(rel)
	if err != nil {
		return nil, err
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
	}
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%w: %s is a folder", ErrInvalid, rel)
	}
	isNote := index.IsNotePath(rel)
	if baseCommit != "" && isNote {
		if err := v.checkBase(ctx, rel, baseCommit); err != nil {
			return nil, err
		}
	}
	item := &TrashItem{ID: v.now().UTC().Format("20060102-150405") + "-" + strings.ToLower(NewID()[20:]), Path: rel, DeletedAt: v.now().UTC(), Size: fi.Size(), RunID: runID(ctx)}
	id := ""
	if isNote {
		if b, _, err := v.ReadRaw(ctx, rel); err == nil {
			n := note.Parse(rel, b)
			item.Title, id = n.Title, n.ID
		}
	}
	if err := v.ensureTrash(); err != nil {
		return nil, err
	}
	meta, _ := json.MarshalIndent(item, "", "  ")
	raw, _ := os.ReadFile(p)
	err = v.runJournal("trash "+rel, []journalOp{
		{Kind: "write", Path: trashDir + "/" + item.ID + ".json", Data: meta},
		{Kind: "move", From: rel, Path: trashDir + "/" + item.ID + "/" + rel},
	})
	if err != nil {
		return nil, err
	}
	if isNote {
		if err := v.ix.Remove(ctx, rel); err != nil {
			v.log.Warn("vault: index remove", "path", rel, "err", err)
		}
	} else {
		_ = v.ix.RemoveAttachment(ctx, rel)
	}
	if v.snaps != nil {
		if !v.snaps.has(rel) {
			_ = v.snaps.record(snapEntry{Path: rel, At: v.now().Add(-time.Millisecond).UnixMilli(), Message: "(before first Nexus change)", PH: v.plainHash(raw)}, raw)
		}
		_ = v.snaps.record(snapEntry{Path: rel, At: v.now().UnixMilli(), Message: "deleted", Run: runID(ctx)}, nil)
	}
	if msg == "" {
		msg = "vault: delete " + rel
	}
	if v.git != nil {
		v.git.schedule([]string{rel}, msg, runID(ctx))
	}
	v.notify([]string{rel})
	v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_DELETED, rel, "", id)
	return item, nil
}

// ListTrash lists trashed items, newest first.
func (v *Vault) ListTrash(ctx context.Context) ([]TrashItem, error) {
	ents, err := os.ReadDir(v.trashRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []TrashItem
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(v.trashRoot(), e.Name()))
		if err != nil {
			continue
		}
		var it TrashItem
		if json.Unmarshal(b, &it) == nil && it.ID == strings.TrimSuffix(e.Name(), ".json") {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out, nil
}

func (v *Vault) trashItem(id string) (*TrashItem, error) {
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return nil, fmt.Errorf("%w: bad trash id", ErrInvalid)
	}
	b, err := os.ReadFile(filepath.Join(v.trashRoot(), id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: trash item %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var it TrashItem
	if err := json.Unmarshal(b, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// RestoreFromTrash moves a trashed item back to its original path (or to
// dest), choosing a free name when the path is taken.
func (v *Vault) RestoreFromTrash(ctx context.Context, id, dest string) (string, error) {
	it, err := v.trashItem(id)
	if err != nil {
		return "", err
	}
	if dest == "" {
		dest = it.Path
	}
	dest, err = CleanPath(dest)
	if err != nil {
		return "", err
	}
	if path.Ext(dest) != path.Ext(it.Path) {
		return "", fmt.Errorf("%w: restore must keep the %q extension", ErrInvalid, path.Ext(it.Path))
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	if dest, err = v.uniquePath(dest); err != nil {
		return "", err
	}
	if _, err := v.abs(dest); err != nil {
		return "", err
	}
	src := trashDir + "/" + it.ID + "/" + it.Path
	if !fileExists(filepath.Join(v.root, filepath.FromSlash(src))) {
		return "", fmt.Errorf("%w: trashed file for %s is missing", ErrNotFound, it.ID)
	}
	if err := v.runJournal("restore "+it.Path+" from trash", []journalOp{{Kind: "move", From: src, Path: dest}}); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(v.trashRoot(), it.ID+".json"))
	_ = os.RemoveAll(filepath.Join(v.trashRoot(), it.ID))
	id2 := ""
	if index.IsNotePath(dest) {
		if _, err := v.ix.UpsertFile(ctx, v.root, dest); err != nil {
			v.log.Warn("vault: index", "path", dest, "err", err)
		}
		if e, ok := v.ix.Get(ctx, dest); ok {
			id2 = e.ID
		}
		if v.snaps != nil {
			if raw, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(dest))); err == nil {
				_ = v.snaps.record(snapEntry{Path: dest, At: v.now().UnixMilli(), Message: "restored from trash", PH: v.plainHash(raw)}, raw)
			}
		}
	} else {
		_, _ = v.ix.UpsertAttachmentFile(ctx, v.root, dest)
	}
	if v.git != nil {
		v.git.schedule([]string{dest}, "vault: restore "+dest+" from trash", runID(ctx))
	}
	v.notify([]string{dest})
	v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED, dest, "", id2)
	return dest, nil
}

// PurgeTrash permanently deletes one item (id) or every item deleted more
// than olderThan ago (0 = all). It returns the number purged.
func (v *Vault) PurgeTrash(ctx context.Context, id string, olderThan time.Duration) (int, error) {
	if id != "" {
		if _, err := v.trashItem(id); err != nil {
			return 0, err
		}
		_ = os.RemoveAll(filepath.Join(v.trashRoot(), id))
		return 1, os.Remove(filepath.Join(v.trashRoot(), id+".json"))
	}
	items, err := v.ListTrash(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	cut := v.now().Add(-olderThan)
	for _, it := range items {
		if olderThan > 0 && it.DeletedAt.After(cut) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(v.trashRoot(), it.ID))
		if os.Remove(filepath.Join(v.trashRoot(), it.ID+".json")) == nil {
			n++
		}
	}
	return n, nil
}
