package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// ConflictError is returned when a write's base revision is stale and the
// change could not be merged. CopyPath is the conflict copy holding the
// rejected content (KeepConflicts), empty otherwise.
type ConflictError struct {
	Path     string
	CopyPath string
}

func (e *ConflictError) Error() string {
	if e.CopyPath != "" {
		return fmt.Sprintf("vault: %s changed since base_commit and the edits overlap; your version was saved as %s", e.Path, e.CopyPath)
	}
	return fmt.Sprintf("vault: %s changed since base_commit and the edits overlap", e.Path)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// resolveConflictLocked handles a stale base (wmu held): three-way merge
// when the base content is known, else conflict copy / error.
func (v *Vault) resolveConflictLocked(ctx context.Context, rel string, incoming []byte, msg, base string, cause error) (WriteResult, error) {
	cur, _, err := v.ReadRaw(ctx, rel)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return WriteResult{}, err
	}
	if exists {
		if baseContent, ok := v.baseContent(ctx, rel, base); ok {
			// Keep the current id: the incoming text may lack it.
			incomingWithID := incoming
			if n := note.Parse(rel, incoming); n.ID == "" {
				if cn := note.Parse(rel, cur); cn.ID != "" {
					incomingWithID, _, _ = note.EnsureID(incoming, func() string { return cn.ID })
				}
			}
			merged, clean := Merge3(string(baseContent), string(cur), string(incomingWithID))
			if clean {
				if msg == "" {
					msg = "vault: update " + rel
				}
				n, err := v.writeLocked(ctx, rel, []byte(merged), msg+" (merged)")
				return WriteResult{Note: n, Merged: true}, err
			}
		}
	}
	ce := &ConflictError{Path: rel}
	if !v.keepConflicts {
		return WriteResult{}, fmt.Errorf("%w (%v)", ce, cause)
	}
	cp, err := v.writeConflictCopyLocked(ctx, rel, incoming)
	if err != nil {
		v.log.Warn("vault: could not save conflict copy", "path", rel, "err", err)
		return WriteResult{}, ce
	}
	ce.CopyPath = cp
	v.notice(Notice{
		Kind:  nexusv1.NoticeKind_NOTICE_KIND_ALERT,
		Title: "Edit conflict in " + rel,
		Body:  fmt.Sprintf("%s was changed by someone else while it was being edited. Both versions were kept: the other edit is in %s — merge them and delete the copy.", rel, cp),
		Paths: []string{rel, cp},
		Data:  map[string]any{"kind": "vault.conflict", "path": rel, "copy": cp},
	})
	return WriteResult{}, ce
}

// conflictName returns "<dir>/<name> (conflict YYYY-MM-DD HHMM).md".
func (v *Vault) conflictName(rel string) string {
	ext := path.Ext(rel)
	return fmt.Sprintf("%s (conflict %s)%s", strings.TrimSuffix(rel, ext), v.now().Format("2006-01-02 1504"), ext)
}

func (v *Vault) writeConflictCopyLocked(ctx context.Context, rel string, content []byte) (string, error) {
	cp, err := v.uniquePath(v.conflictName(rel))
	if err != nil {
		return "", err
	}
	// The copy must not share the original's id.
	// A plain path (not a [[link]]) so the copy does not show up as a backlink.
	fields := map[string]any{"id": NewID(), "conflict_of": rel}
	out, err := note.SetFields(content, fields, "id", "conflict_of")
	if err != nil {
		out = content // invalid frontmatter: keep verbatim (EnsureID leaves it)
	}
	if _, err := v.writeLocked(WithOrigin(ctx, "system"), cp, out, "conflict copy of "+rel); err != nil {
		return "", err
	}
	return cp, nil
}

// baseContent returns the (decrypted) content of rel at a base revision.
func (v *Vault) baseContent(ctx context.Context, rel, base string) ([]byte, bool) {
	if strings.HasPrefix(base, HashPrefix) {
		h := strings.TrimPrefix(base, HashPrefix)
		if b, ok := v.recent.get(h); ok {
			return b, true
		}
		if v.snaps != nil {
			if raw, ok := v.snaps.byPlainHash(h); ok {
				if b, err := v.decode(raw); err == nil && index.Hash(b) == h {
					return b, true
				}
			}
		}
		if v.git != nil {
			if raw, err := v.git.show(ctx, "HEAD", rel); err == nil {
				if b, err := v.decode(raw); err == nil && index.Hash(b) == h {
					return b, true
				}
			}
		}
		return nil, false
	}
	if v.git == nil {
		return nil, false
	}
	raw, err := v.git.show(ctx, base, rel)
	if err != nil {
		return nil, false
	}
	b, err := v.decode(raw)
	return b, err == nil
}

// conflictCopies lists files named like conflict copies (Check).
func isConflictCopy(rel string) bool {
	return strings.Contains(path.Base(rel), " (conflict ")
}

var _ = os.ErrNotExist
