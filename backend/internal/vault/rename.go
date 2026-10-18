package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Refactoring: rename/move with link updates, merge, split. Every operation
// is one write-ahead journal entry (all files change or none, even across a
// crash) and one git commit.

// fileChange is a planned note write for a refactoring.
type fileChange struct {
	path string
	text []byte // plaintext
}

// commitChanges journals and applies note writes, deletions (delete) and
// moves to the trash (trash), then updates the index, snapshots, git and
// the change feed. wmu must be held.
func (v *Vault) commitChanges(ctx context.Context, msg string, writes []fileChange, deletes []string, trash []*TrashItem, renamedFrom map[string]string, preMoves ...[2]string) error {
	var ops []journalOp
	for _, m := range preMoves {
		ops = append(ops, journalOp{Kind: "move", From: m[0], Path: m[1]})
	}
	for _, w := range writes {
		raw, err := v.encode(w.path, w.text)
		if err != nil {
			return err
		}
		if int64(len(w.text)) > v.maxNote {
			return fmt.Errorf("%w: %s", ErrTooLarge, w.path)
		}
		ops = append(ops, journalOp{Kind: "write", Path: w.path, Data: raw})
	}
	for _, d := range deletes {
		ops = append(ops, journalOp{Kind: "delete", Path: d})
	}
	if len(trash) > 0 {
		if err := v.ensureTrash(); err != nil {
			return err
		}
	}
	for _, it := range trash {
		meta, _ := json.MarshalIndent(it, "", "  ")
		ops = append(ops,
			journalOp{Kind: "write", Path: trashDir + "/" + it.ID + ".json", Data: meta},
			journalOp{Kind: "move", From: it.Path, Path: trashDir + "/" + it.ID + "/" + it.Path})
	}
	// Existing state, for change kinds and first snapshots.
	existed := map[string][]byte{}
	for _, w := range writes {
		if b, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(w.path))); err == nil {
			existed[w.path] = b
		}
	}
	if err := v.runJournal(msg, ops); err != nil {
		return err
	}
	var paths []string
	now := v.now()
	for _, d := range deletes {
		_ = v.ix.Remove(ctx, d)
		paths = append(paths, d)
	}
	for _, it := range trash {
		_ = v.ix.Remove(ctx, it.Path)
		paths = append(paths, it.Path)
		if v.snaps != nil {
			_ = v.snaps.record(snapEntry{Path: it.Path, At: now.UnixMilli(), Message: msg + " (trashed)", Run: runID(ctx)}, nil)
		}
		v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_DELETED, it.Path, "", "")
	}
	for _, w := range writes {
		p := filepath.Join(v.root, filepath.FromSlash(w.path))
		if fi, err := os.Stat(p); err == nil {
			if _, err := v.ix.Upsert(ctx, w.path, w.text, fi.ModTime()); err != nil {
				v.log.Warn("vault: index", "path", w.path, "err", err)
			}
		}
		v.recent.put(indexHash(w.text), w.text)
		if v.snaps != nil {
			if old, ok := existed[w.path]; ok && !v.snaps.has(w.path) {
				_ = v.snaps.record(snapEntry{Path: w.path, At: now.Add(-time.Millisecond).UnixMilli(), Message: "(before first Nexus change)", PH: v.plainHash(old)}, old)
			}
			raw, _ := os.ReadFile(p)
			_ = v.snaps.record(snapEntry{Path: w.path, At: now.UnixMilli(), Message: msg, Run: runID(ctx), PH: indexHash(w.text), OldPath: renamedFrom[w.path]}, raw)
		}
		paths = append(paths, w.path)
		id := note.Parse(w.path, w.text).ID
		switch {
		case renamedFrom[w.path] != "":
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RENAMED, w.path, renamedFrom[w.path], id)
		case existed[w.path] != nil:
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_UPDATED, w.path, "", id)
		default:
			v.emit(ctx, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED, w.path, "", id)
		}
	}
	if v.git != nil {
		v.git.schedule(paths, msg, runID(ctx))
	}
	v.notify(paths)
	return nil
}

// backlinkSources returns the notes linking to rel (excluding skip).
func (v *Vault) backlinkSources(ctx context.Context, rel string, skip ...string) ([]string, error) {
	back, err := v.ix.Backlinks(ctx, rel)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range skip {
		seen[s] = true
	}
	var out []string
	for _, e := range back {
		if !seen[e.Src] {
			seen[e.Src] = true
			out = append(out, e.Src)
		}
	}
	sort.Strings(out)
	return out, nil
}

// rewriteSources rewrites links in srcs, returning the changed files.
func (v *Vault) rewriteSources(ctx context.Context, rw *rewriter, srcs []string) ([]fileChange, error) {
	var out []fileChange
	for _, s := range srcs {
		b, _, err := v.ReadRaw(ctx, s)
		if err != nil {
			v.log.Warn("vault: skip link update", "path", s, "err", err)
			continue
		}
		if nt, changed := rw.rewrite(string(b), s, s); changed {
			out = append(out, fileChange{path: s, text: []byte(nt)})
		}
	}
	return out, nil
}

// RenameResult reports a rename/merge.
type RenameResult struct {
	Note    *nexusv1.Note
	Updated []string // other notes whose links were rewritten
}

// Rename moves a note to a new path and (unless skipLinks) rewrites every
// link to it: [[name]], [[folder/name]], [[name#heading|alias]], embeds,
// Markdown links and frontmatter link fields. Links by alias or id keep
// working unchanged. Relative Markdown links inside the moved note are
// adjusted when it changes folder.
func (v *Vault) Rename(ctx context.Context, fromIDOrPath, to string, skipLinks bool, msg string) (*RenameResult, error) {
	old, err := v.locate(ctx, fromIDOrPath)
	if err != nil {
		return nil, err
	}
	newp, err := notePath(to)
	if err != nil {
		return nil, err
	}
	if old == newp {
		n, err := v.Read(ctx, old)
		return &RenameResult{Note: n}, err
	}
	np, err := v.abs(newp)
	if err != nil {
		return nil, err
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	text, _, err := v.ReadRaw(ctx, old)
	if err != nil {
		return nil, err
	}
	caseOnly := strings.EqualFold(old, newp)
	if fileExists(np) && !caseOnly {
		return nil, fmt.Errorf("%w: %s already exists", ErrInvalid, newp)
	}
	rw, err := newRewriter(ctx, v.ix, map[string]linkTarget{old: {path: newp}})
	if err != nil {
		return nil, err
	}
	rw.keepIDs = true
	var writes []fileChange
	if !skipLinks {
		srcs, err := v.backlinkSources(ctx, old, old)
		if err != nil {
			return nil, err
		}
		if writes, err = v.rewriteSources(ctx, rw, srcs); err != nil {
			return nil, err
		}
		if nt, changed := rw.rewrite(string(text), old, newp); changed {
			text = []byte(nt)
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("rename %s → %s", old, newp)
	}
	updated := make([]string, 0, len(writes))
	for _, w := range writes {
		updated = append(updated, w.path)
	}
	deletes := []string{old}
	var pre [][2]string
	if caseOnly {
		// Case-insensitive file systems: move aside first (inside the
		// journal) so writing the new name does not overwrite, and then
		// delete, the same file.
		tmp := old + "~nexus-rename"
		pre = append(pre, [2]string{old, tmp})
		deletes = []string{tmp}
	}
	writes = append(writes, fileChange{path: newp, text: text})
	if err := v.commitChanges(ctx, msg, writes, deletes, nil, map[string]string{newp: old}, pre...); err != nil {
		return nil, err
	}
	if caseOnly {
		_ = v.ix.Remove(ctx, old)
		if v.git != nil {
			v.git.schedule([]string{old}, msg, runID(ctx))
		}
	}
	n, err := v.toNote(ctx, newp, text)
	return &RenameResult{Note: n, Updated: updated}, err
}

// newTrashItem describes a file about to be trashed.
func (v *Vault) newTrashItem(ctx context.Context, rel, title string) *TrashItem {
	it := &TrashItem{ID: v.now().UTC().Format("20060102-150405") + "-" + strings.ToLower(NewID()[20:]), Path: rel, Title: title, DeletedAt: v.now().UTC(), RunID: runID(ctx)}
	if fi, err := os.Stat(filepath.Join(v.root, filepath.FromSlash(rel))); err == nil {
		it.Size = fi.Size()
	}
	return it
}

// Merge appends source into target (under a "## <source title>" heading),
// adds the source's title and aliases to the target's aliases and its tags
// to the target's tags, redirects every link to source, and moves source to
// the trash.
func (v *Vault) Merge(ctx context.Context, source, target, msg string) (*RenameResult, error) {
	s, err := v.locate(ctx, source)
	if err != nil {
		return nil, err
	}
	t, err := v.locate(ctx, target)
	if err != nil {
		return nil, err
	}
	if s == t {
		return nil, fmt.Errorf("%w: cannot merge a note into itself", ErrInvalid)
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	sText, _, err := v.ReadRaw(ctx, s)
	if err != nil {
		return nil, err
	}
	tText, _, err := v.ReadRaw(ctx, t)
	if err != nil {
		return nil, err
	}
	sn, tn := note.Parse(s, sText), note.Parse(t, tText)
	if tn.FrontmatterErr != nil {
		return nil, fmt.Errorf("%w: %s has invalid frontmatter", ErrInvalid, t)
	}
	rw, err := newRewriter(ctx, v.ix, map[string]linkTarget{s: {path: t}})
	if err != nil {
		return nil, err
	}
	// Source body, links fixed for its new home, H1 dropped.
	body, _ := rw.rewrite(sn.Body, s, t)
	body = dropH1(body, sn.Title)
	merged := strings.TrimRight(string(tText), "\n") + "\n\n## " + sn.Title + "\n\n" + strings.TrimSpace(body) + "\n"
	merged, _ = rw.rewrite(merged, t, t)
	aliases := uniqueStrings(append(append(append([]string{}, tn.Aliases...), sn.Title), sn.Aliases...), tn.Title)
	fmTags := uniqueStrings(append(note.StringList(tn.Frontmatter["tags"]), note.StringList(sn.Frontmatter["tags"])...), "")
	fields := map[string]any{"aliases": aliases}
	if len(fmTags) > 0 {
		fields["tags"] = fmTags
	}
	out, err := note.SetFields([]byte(merged), fields, "aliases", "tags")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	srcs, err := v.backlinkSources(ctx, s, s, t)
	if err != nil {
		return nil, err
	}
	writes, err := v.rewriteSources(ctx, rw, srcs)
	if err != nil {
		return nil, err
	}
	updated := make([]string, 0, len(writes))
	for _, w := range writes {
		updated = append(updated, w.path)
	}
	writes = append(writes, fileChange{path: t, text: out})
	if msg == "" {
		msg = fmt.Sprintf("merge %s into %s", s, t)
	}
	if err := v.commitChanges(ctx, msg, writes, nil, []*TrashItem{v.newTrashItem(ctx, s, sn.Title)}, nil); err != nil {
		return nil, err
	}
	n, err := v.toNote(ctx, t, out)
	return &RenameResult{Note: n, Updated: updated}, err
}

func dropH1(body, title string) string {
	lines := strings.SplitAfter(body, "\n")
	for i, l := range lines {
		tl := strings.TrimSpace(l)
		if tl == "" {
			continue
		}
		if strings.HasPrefix(tl, "# ") && strings.EqualFold(strings.TrimSpace(tl[2:]), title) {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), "")
		}
		break
	}
	return body
}

func uniqueStrings(in []string, exclude string) []string {
	seen := map[string]bool{strings.ToLower(exclude): exclude != ""}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// SplitResult reports a split.
type SplitResult struct {
	Note    *nexusv1.Note
	Created []*nexusv1.NoteRef
	Updated []string
}

// Split moves every section at heading level (default 2) of a note into its
// own note (named after the heading, in folder or the note's folder). The
// original keeps its other content and embeds each new note (![[New]]);
// links to the moved headings ([[Note#Heading]]) are redirected.
func (v *Vault) Split(ctx context.Context, idOrPath string, level int, folder string) (*SplitResult, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	if level <= 0 {
		level = 2
	}
	if level > 6 {
		return nil, fmt.Errorf("%w: heading level must be 1-6", ErrInvalid)
	}
	if folder == "" {
		folder = path.Dir(rel)
	}
	if folder != "." {
		if folder, err = CleanPath(folder); err != nil {
			return nil, err
		}
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	text, _, err := v.ReadRaw(ctx, rel)
	if err != nil {
		return nil, err
	}
	n := note.Parse(rel, text)
	lines := strings.SplitAfter(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n")
	type section struct {
		start, end int // 0-based line indexes [start, end)
		heading    string
		dest       string
	}
	var secs []section
	for i, h := range n.HeadingLines {
		if h.Level != level {
			continue
		}
		end := len(lines)
		for _, h2 := range n.HeadingLines[i+1:] {
			if h2.Level <= level {
				end = h2.Line - 1
				break
			}
		}
		secs = append(secs, section{start: h.Line - 1, end: end, heading: h.Text})
	}
	if len(secs) == 0 {
		return nil, fmt.Errorf("%w: %s has no level-%d headings", ErrInvalid, rel, level)
	}
	taken := map[string]bool{}
	rw, err := newRewriter(ctx, v.ix, map[string]linkTarget{rel: {path: rel}})
	if err != nil {
		return nil, err
	}
	anchors := map[string]linkTarget{}
	for i := range secs {
		name := SafeFileName(stripHeadingMarkup(secs[i].heading))
		if name == "" {
			name = fmt.Sprintf("Section %d", i+1)
		}
		base := name + ".md"
		if folder != "." {
			base = folder + "/" + base
		}
		dest, err := v.uniquePath(base)
		if err != nil {
			return nil, err
		}
		for k := 2; taken[strings.ToLower(dest)]; k++ {
			if dest, err = v.uniquePath(fmt.Sprintf("%s-%d.md", strings.TrimSuffix(base, ".md"), k)); err != nil {
				return nil, err
			}
		}
		taken[strings.ToLower(dest)] = true
		secs[i].dest = dest
		anchors[strings.ToLower(secs[i].heading)] = linkTarget{path: dest}
		rw.names[baseNoExt(dest)]++
	}
	rw.anchors[rel] = anchors
	var writes []fileChange
	var created []string
	parentLink := "[[" + rw.linkText(rel, false) + "]]"
	var orig strings.Builder
	next := 0
	for _, sct := range secs {
		for _, l := range lines[next:sct.start] {
			orig.WriteString(l)
		}
		next = sct.end
		bodyLines := strings.Join(lines[sct.start+1:sct.end], "")
		bodyLines, _ = rw.rewrite(bodyLines, rel, sct.dest)
		md, err := note.Render([]string{"id", "split_from"}, map[string]any{"id": NewID(), "split_from": parentLink},
			"# "+sct.heading+"\n"+strings.TrimRight(bodyLines, "\n")+"\n")
		if err != nil {
			return nil, err
		}
		writes = append(writes, fileChange{path: sct.dest, text: md})
		created = append(created, sct.dest)
		orig.WriteString("![[" + rw.linkText(sct.dest, false) + "]]\n\n")
	}
	for _, l := range lines[next:] {
		orig.WriteString(l)
	}
	origText, _ := rw.rewrite(strings.TrimRight(orig.String(), "\n")+"\n", rel, rel)
	srcs, err := v.backlinkSources(ctx, rel, rel)
	if err != nil {
		return nil, err
	}
	others, err := v.rewriteSources(ctx, rw, srcs)
	if err != nil {
		return nil, err
	}
	var updated []string
	for _, w := range others {
		updated = append(updated, w.path)
	}
	writes = append(writes, others...)
	writes = append(writes, fileChange{path: rel, text: []byte(origText)})
	if err := v.commitChanges(ctx, fmt.Sprintf("split %s into %d notes", rel, len(secs)), writes, nil, nil, nil); err != nil {
		return nil, err
	}
	res := &SplitResult{Updated: updated}
	if res.Note, err = v.toNote(ctx, rel, []byte(origText)); err != nil {
		return nil, err
	}
	for _, c := range created {
		if e, ok := v.ix.Get(ctx, c); ok {
			res.Created = append(res.Created, entryRef(e))
		}
	}
	return res, nil
}

func stripHeadingMarkup(s string) string {
	s = reWikiFull.ReplaceAllStringFunc(s, func(m string) string {
		sub := reWikiFull.FindStringSubmatch(m)
		l, _ := note.ParseLinkText(sub[2])
		if l.Alias != "" {
			return l.Alias
		}
		return l.Target
	})
	return strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "_", " ").Replace(s))
}
