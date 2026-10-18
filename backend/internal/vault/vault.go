// Package vault is the Nexus knowledge vault: a folder of Obsidian-compatible
// Markdown notes (optionally a git repo) with a SQLite index for full-text
// search, links and graph queries, plus the agent catalog (agents/*.md).
//
// Subpackages:
//   - note:     Markdown/frontmatter/link parser
//   - index:    SQLite index (notes, edges, FTS5, optional vectors)
//   - agentdef: agent note → *nexusv1.Agent
//
// The "vault" hub module (module.go) wires it into the kernel: it provides
// kernel.SvcVault and kernel.SvcAgents, the VaultService and AgentService
// Connect handlers, and the hub tools memory.search, vault.read, vault.write,
// vault.link, vault.daily_append and facts.propose.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Errors returned by the vault (mapped to Connect codes by the handlers).
var (
	ErrNotFound    = errors.New("vault: note not found")
	ErrInvalidPath = errors.New("vault: invalid path")
	ErrConflict    = errors.New("vault: note changed since base_commit")
	ErrInvalid     = errors.New("vault: invalid input")
	ErrTooLarge    = errors.New("vault: file too large")
)

// Default limits.
const (
	DefaultMaxNoteBytes       = 5 << 20
	DefaultMaxAttachmentBytes = 100 << 20
)

// HashPrefix marks content-hash revisions in Note.commit (see WriteNote).
const HashPrefix = "sha256:"

// Options configure a Vault.
type Options struct {
	// Root is the vault directory (created if missing).
	Root string
	// IndexPath is the SQLite index file, e.g. <data dir>/index.db.
	// Empty = in-memory (tests only).
	IndexPath string
	// Git enables auto-commit when Root is inside a git work tree.
	Git bool
	// CommitDelay batches writes into one commit (0 = commit on every write).
	CommitDelay time.Duration
	Logger      *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time

	// StateDir holds local state outside the vault (the snapshot store used
	// for undo when the vault is not a git repo). Empty = no snapshots.
	StateDir string
	// SnapshotKeep is the number of versions kept per note (default 50).
	SnapshotKeep int
	// NoLock skips the single-writer lock (read-only tools, tests).
	NoLock bool
	// KeepConflicts writes "<note> (conflict YYYY-MM-DD HHMM).md" copies for
	// stale writes that cannot be merged, instead of only failing.
	KeepConflicts bool
	// Encryption enables encryption at rest for some folders.
	Encryption *EncryptionOptions
	// MaxNoteBytes / MaxAttachmentBytes limit writes (0 = defaults).
	MaxNoteBytes       int64
	MaxAttachmentBytes int64
}

// Vault is the knowledge base. It implements kernel.Vault.
type Vault struct {
	root string
	ix   *index.Index
	git  *gitRepo
	log  *slog.Logger
	now  func() time.Time

	wmu sync.Mutex // serialises read-modify-write of files

	lmu       sync.RWMutex
	listeners []func(paths []string)

	// LLM, when set, returns the LLM service (resolved lazily by the module).
	LLM func() (kernel.LLM, bool)

	lock          *writerLock
	snaps         *snapStore
	feed          *feed
	crypt         *crypter
	recent        *recentCache
	keepConflicts bool
	maxNote       int64
	maxAttach     int64
	// journalCrashAfter is a test hook (see runJournal); -1 = off.
	journalCrashAfter int
	templates         string
	periodic          map[string]PeriodicConfig

	nmu        sync.RWMutex
	notices    func(Notice)
	lastReport *Report
	// Fetch, when set, fetches a URL as readable text (web clips).
	Fetch func(ctx context.Context, url string) (*Clip, error)
}

// Notice is an inbox card raised by the vault (conflicts, integrity
// problems, backup failures). The module publishes them on the event bus.
type Notice struct {
	Kind  nexusv1.NoticeKind
	Title string
	Body  string
	Paths []string
	Data  map[string]any
}

// SetNoticeSink sets where vault notices go (nil = log only).
func (v *Vault) SetNoticeSink(fn func(Notice)) {
	v.nmu.Lock()
	v.notices = fn
	v.nmu.Unlock()
}

func (v *Vault) setLastReport(r *Report) {
	v.nmu.Lock()
	v.lastReport = r
	v.nmu.Unlock()
}

// LastReport returns the latest integrity report (nil before the first check).
func (v *Vault) LastReport() *Report {
	v.nmu.RLock()
	defer v.nmu.RUnlock()
	return v.lastReport
}

func (v *Vault) notice(n Notice) {
	v.nmu.RLock()
	fn := v.notices
	v.nmu.RUnlock()
	v.log.Warn("vault: "+n.Title, "detail", n.Body, "paths", n.Paths)
	if fn != nil {
		fn(n)
	}
}

var _ kernel.Vault = (*Vault)(nil)

// Open opens a vault directory and its index. Call Sync to index existing
// files.
func Open(o Options) (*Vault, error) {
	if o.Root == "" {
		return nil, fmt.Errorf("vault: empty root")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	abs, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	crypt, err := newCrypter(o.Encryption)
	if err != nil {
		return nil, err
	}
	var lock *writerLock
	if !o.NoLock {
		if lock, err = acquireLock(abs); err != nil {
			return nil, err
		}
	}
	var ix *index.Index
	if o.IndexPath == "" {
		ix, err = index.OpenMemory()
	} else {
		ix, err = index.Open(o.IndexPath)
	}
	if err != nil {
		lock.release()
		return nil, fmt.Errorf("vault index: %w", err)
	}
	v := &Vault{
		root: abs, ix: ix, log: o.Logger, now: o.Now, lock: lock, crypt: crypt,
		feed: newFeed(o.Now()), recent: newRecentCache(256, 8<<20),
		keepConflicts: o.KeepConflicts, maxNote: o.MaxNoteBytes, maxAttach: o.MaxAttachmentBytes,
		journalCrashAfter: -1,
	}
	if v.maxNote <= 0 {
		v.maxNote = DefaultMaxNoteBytes
	}
	if v.maxAttach <= 0 {
		v.maxAttach = DefaultMaxAttachmentBytes
	}
	ix.SetDecoder(func(_ string, raw []byte) ([]byte, error) { return v.decode(raw) })
	ix.SetPrivate(func(rel string) bool { return v.crypt.covers(rel) && !v.crypt.searchable })
	ix.SetAttachmentExtractor(extractAttachment)
	if o.Git {
		v.git = detectGit(abs, o.CommitDelay, o.Logger)
	}
	if v.git == nil && o.StateDir != "" {
		if v.snaps, err = openSnapStore(filepath.Join(o.StateDir, "snapshots"), o.SnapshotKeep); err != nil {
			v.log.Warn("vault: snapshot store unavailable; undo limited to the trash", "err", err)
		}
	}
	if !o.NoLock {
		touched, err := v.recoverJournal()
		if err != nil {
			v.log.Error("vault: journal recovery incomplete", "err", err)
		}
		if len(touched) > 0 && v.git != nil {
			v.git.schedule(touched, "vault: complete interrupted operation", "")
		}
	}
	return v, nil
}

// Close flushes pending commits, closes the index and releases the lock.
func (v *Vault) Close() error {
	v.Flush()
	err := v.ix.Close()
	v.lock.release()
	return err
}

// Flush commits pending git changes now.
func (v *Vault) Flush() {
	if v.git != nil {
		v.git.flush()
	}
}

// Root returns the vault directory.
func (v *Vault) Root() string { return v.root }

// Index exposes the index (search, graph, vectors).
func (v *Vault) Index() *index.Index { return v.ix }

// GitEnabled reports whether writes are auto-committed.
func (v *Vault) GitEnabled() bool { return v.git != nil }

// Sync incrementally indexes the vault.
func (v *Vault) Sync(ctx context.Context) (index.SyncStats, error) {
	st, err := v.ix.Sync(ctx, v.root)
	if err == nil && st.Updated+st.Removed > 0 {
		v.notify(nil)
	}
	return st, err
}

// OnChange registers a callback for changed vault paths (writes and external
// edits). A nil slice means "anything may have changed" (full sync).
func (v *Vault) OnChange(fn func(paths []string)) {
	v.lmu.Lock()
	v.listeners = append(v.listeners, fn)
	v.lmu.Unlock()
}

func (v *Vault) notify(paths []string) {
	v.lmu.RLock()
	ls := append([]func([]string){}, v.listeners...)
	v.lmu.RUnlock()
	for _, fn := range ls {
		fn(paths)
	}
}

// ---------------------------------------------------------------------------
// Paths

// CleanPath validates a vault-relative path: no absolute paths, no "..",
// no hidden segments (.git, .obsidian), slash-separated.
func CleanPath(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: empty", ErrInvalidPath)
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || (len(rel) > 1 && rel[1] == ':') {
		return "", fmt.Errorf("%w: %q is absolute", ErrInvalidPath, rel)
	}
	c := path.Clean(rel)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: %q escapes the vault", ErrInvalidPath, rel)
	}
	for _, seg := range strings.Split(c, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("%w: %q has a hidden segment", ErrInvalidPath, rel)
		}
	}
	return c, nil
}

// notePath validates a note path; ".md" is appended when there is no extension.
func notePath(rel string) (string, error) {
	c, err := CleanPath(rel)
	if err != nil {
		return "", err
	}
	switch ext := strings.ToLower(path.Ext(c)); ext {
	case ".md":
		return c, nil
	case "":
		return c + ".md", nil
	default:
		return "", fmt.Errorf("%w: notes must be .md files (got %q)", ErrInvalidPath, ext)
	}
}

// abs maps a clean relative path to disk, refusing symlinks that leave the root.
func (v *Vault) abs(rel string) (string, error) {
	p := filepath.Join(v.root, filepath.FromSlash(rel))
	// Check the deepest existing ancestor (or the file itself) after
	// resolving symlinks.
	probe := p
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	if real != v.root && !strings.HasPrefix(real, v.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves outside the vault", ErrInvalidPath, rel)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Run ids

type runKey struct{}

// WithRunID tags writes made with ctx with a task run id; it is added to the
// git commit message as a "Nexus-Run:" trailer.
func WithRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, runKey{}, runID)
}

func runID(ctx context.Context) string {
	s, _ := ctx.Value(runKey{}).(string)
	return s
}

// NewID returns a new note id (ULID).
func NewID() string { return ulid.Make().String() }

// ---------------------------------------------------------------------------
// Read / write

// ReadRaw returns the Markdown of a note by path.
func (v *Vault) ReadRaw(ctx context.Context, rel string) ([]byte, string, error) {
	c, err := notePath(rel)
	if err != nil {
		return nil, "", err
	}
	p, err := v.abs(c)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, c, fmt.Errorf("%w: %s", ErrNotFound, c)
	}
	if err != nil {
		return nil, c, err
	}
	b, err = v.decode(b)
	return b, c, err
}

// locate turns an id or path into a vault path.
func (v *Vault) locate(ctx context.Context, idOrPath string) (string, error) {
	if e, ok := v.ix.Lookup(ctx, idOrPath); ok {
		return e.Path, nil
	}
	c, err := notePath(idOrPath)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, idOrPath)
	}
	return c, nil
}

// Read returns a note by path or id.
func (v *Vault) Read(ctx context.Context, idOrPath string) (*nexusv1.Note, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	b, rel, err := v.ReadRaw(ctx, rel)
	if err != nil {
		return nil, err
	}
	return v.toNote(ctx, rel, b)
}

// Write writes a note (kernel.Vault). See WriteNote.
func (v *Vault) Write(ctx context.Context, rel, markdown, commitMessage string) (*nexusv1.Note, error) {
	return v.WriteNote(ctx, rel, []byte(markdown), commitMessage, "")
}

// WriteNote writes a note atomically, adds an `id` (ULID) to its
// frontmatter when missing, reindexes it and schedules a git commit.
//
// Optimistic concurrency: when baseCommit is non-empty the write fails with
// ErrConflict unless the file is unchanged since that revision. A revision
// is the Note.commit value returned by reads/writes:
//   - a git commit hash (git vaults, file clean): the file must still have
//     the same content as in that commit;
//   - "sha256:<hex>" (no git, or uncommitted changes): the file's current
//     content hash must match.
//
// A missing file matches a revision in which it did not exist.
func (v *Vault) WriteNote(ctx context.Context, rel string, markdown []byte, commitMessage, baseCommit string) (*nexusv1.Note, error) {
	r, err := v.WriteNoteEx(ctx, rel, markdown, commitMessage, baseCommit)
	return r.Note, err
}

// WriteResult is the outcome of WriteNoteEx.
type WriteResult struct {
	Note *nexusv1.Note
	// Merged is true when a stale base was three-way merged cleanly.
	Merged bool
}

// WriteNoteEx is WriteNote with conflict handling: when baseCommit is stale
// the change is three-way merged with the current version (line based);
// if that fails the write is rejected with a *ConflictError (ErrConflict)
// and, with KeepConflicts, the incoming content is saved as a conflict copy
// next to the note and a Notice is raised.
func (v *Vault) WriteNoteEx(ctx context.Context, rel string, markdown []byte, commitMessage, baseCommit string) (WriteResult, error) {
	c, err := notePath(rel)
	if err != nil {
		return WriteResult{}, err
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	if baseCommit != "" {
		if err := v.checkBase(ctx, c, baseCommit); err != nil {
			if errors.Is(err, ErrConflict) {
				return v.resolveConflictLocked(ctx, c, markdown, commitMessage, baseCommit, err)
			}
			return WriteResult{}, err
		}
	}
	n, err := v.writeLocked(ctx, c, markdown, commitMessage)
	return WriteResult{Note: n}, err
}

// Update applies fn to the current content of a note (nil when missing)
// under the write lock and writes the result.
func (v *Vault) Update(ctx context.Context, rel string, commitMessage string, fn func(old []byte) ([]byte, error)) (*nexusv1.Note, error) {
	c, err := notePath(rel)
	if err != nil {
		return nil, err
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	old, _, err := v.ReadRaw(ctx, c)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	out, err := fn(old)
	if err != nil {
		return nil, err
	}
	return v.writeLocked(ctx, c, out, commitMessage)
}

func (v *Vault) writeLocked(ctx context.Context, rel string, markdown []byte, msg string) (*nexusv1.Note, error) {
	p, err := v.abs(rel)
	if err != nil {
		return nil, err
	}
	if int64(len(markdown)) > v.maxNote {
		return nil, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, rel, len(markdown), v.maxNote)
	}
	if !strings.HasPrefix(rel, v.templatesDir()+"/") {
		// Templates get no id: notes created from them would share it.
		markdown, _, _ = note.EnsureID(markdown, NewID)
	}
	if msg == "" {
		msg = "vault: update " + rel
	}
	old, rerr := os.ReadFile(p)
	existed := rerr == nil
	if v.snaps != nil && existed && !v.snaps.has(rel) {
		// First Nexus change to a file that existed before: keep the original.
		_ = v.snaps.record(snapEntry{Path: rel, At: v.now().Add(-time.Millisecond).UnixMilli(), Message: "(before first Nexus change)", PH: v.plainHash(old)}, old)
	}
	raw, err := v.encode(rel, markdown)
	if err != nil {
		return nil, fmt.Errorf("vault: encrypt %s: %w", rel, err)
	}
	if err := writeAtomic(p, raw); err != nil {
		return nil, fmt.Errorf("vault: write %s: %w", rel, err)
	}
	if fi, err := os.Stat(p); err == nil {
		if _, err := v.ix.Upsert(ctx, rel, markdown, fi.ModTime()); err != nil {
			v.log.Warn("vault: index update failed", "path", rel, "err", err)
		}
	}
	v.recent.put(index.Hash(markdown), markdown)
	if v.snaps != nil {
		if err := v.snaps.record(snapEntry{Path: rel, At: v.now().UnixMilli(), Message: msg, Run: runID(ctx), PH: index.Hash(markdown)}, raw); err != nil {
			v.log.Warn("vault: snapshot failed", "path", rel, "err", err)
		}
	}
	if v.git != nil {
		v.git.schedule([]string{rel}, msg, runID(ctx))
	}
	v.notify([]string{rel})
	kind := nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_UPDATED
	if !existed {
		kind = nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED
	}
	n, err := v.toNote(ctx, rel, markdown)
	if err == nil {
		v.emit(ctx, kind, rel, "", n.GetRef().GetId())
	}
	return n, err
}

// plainHash hashes the decrypted content of on-disk bytes.
func (v *Vault) plainHash(raw []byte) string {
	if p, err := v.decode(raw); err == nil {
		return index.Hash(p)
	}
	return ""
}

func timestampOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// writeFile writes a non-note file (attachments) inside the vault.
func (v *Vault) writeFile(ctx context.Context, rel string, data []byte, msg string) error {
	c, err := CleanPath(rel)
	if err != nil {
		return err
	}
	p, err := v.abs(c)
	if err != nil {
		return err
	}
	if int64(len(data)) > v.maxAttach {
		return fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, c, len(data), v.maxAttach)
	}
	existed := fileExists(p)
	if err := writeAtomic(p, data); err != nil {
		return err
	}
	if index.IsAttachmentPath(c) {
		if _, err := v.ix.UpsertAttachmentFile(ctx, v.root, c); err != nil {
			v.log.Warn("vault: attachment index", "path", c, "err", err)
		}
	}
	if v.git != nil {
		v.git.schedule([]string{c}, msg, runID(ctx))
	}
	kind := nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_UPDATED
	if !existed {
		kind = nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED
	}
	v.emit(ctx, kind, c, "", "")
	return nil
}

func (v *Vault) checkBase(ctx context.Context, rel, base string) error {
	cur, _, err := v.ReadRaw(ctx, rel)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if strings.HasPrefix(base, HashPrefix) {
		if !exists || index.Hash(cur) != strings.TrimPrefix(base, HashPrefix) {
			return fmt.Errorf("%w: %s", ErrConflict, rel)
		}
		return nil
	}
	if v.git == nil {
		return fmt.Errorf("%w: base_commit %q is not a content hash and the vault has no git", ErrConflict, base)
	}
	want, err := v.git.blobAt(ctx, base, rel)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	have := ""
	if exists {
		have = v.git.hashFile(ctx, rel)
	}
	if want != have {
		return fmt.Errorf("%w: %s", ErrConflict, rel)
	}
	return nil
}

// revision returns the Note.commit value for a file (see WriteNote).
func (v *Vault) revision(ctx context.Context, rel string, content []byte) string {
	if v.git != nil && v.git.clean(ctx, rel) {
		if c := v.git.lastCommit(ctx, rel); c != "" {
			return c
		}
	}
	return HashPrefix + index.Hash(content)
}

// toNote builds the API representation of a note.
func (v *Vault) toNote(ctx context.Context, rel string, content []byte) (*nexusv1.Note, error) {
	n := note.Parse(rel, content)
	out := &nexusv1.Note{
		Ref:    &nexusv1.NoteRef{Id: noteKey(n.ID, rel), Path: rel, Title: n.Title},
		Type:   n.Type,
		Tags:   n.Tags,
		Body:      n.Body,
		Commit:    v.revision(ctx, rel, content),
		Encrypted: v.crypt.covers(rel),
	}
	if fm, err := toStruct(n.Frontmatter); err == nil {
		out.Frontmatter = fm
	}
	if p, err := v.abs(rel); err == nil {
		if fi, err := os.Stat(p); err == nil {
			out.UpdatedAt = timestamppb.New(fi.ModTime())
		}
	}
	seen := map[string]bool{}
	for _, l := range n.Links {
		ref := v.refFor(ctx, l.Target, rel)
		k := ref.Id + "\x00" + ref.Title
		if seen[k] {
			continue
		}
		seen[k] = true
		out.Links = append(out.Links, ref)
	}
	if back, err := v.ix.Backlinks(ctx, rel); err == nil {
		seen = map[string]bool{}
		for _, e := range back {
			if seen[e.Src] {
				continue
			}
			seen[e.Src] = true
			if src, ok := v.ix.Get(ctx, e.Src); ok {
				out.Backlinks = append(out.Backlinks, entryRef(src))
			}
		}
	}
	return out, nil
}

// refFor resolves a link target; dangling links get an empty id and path and
// the target as title.
func (v *Vault) refFor(ctx context.Context, target, from string) *nexusv1.NoteRef {
	if e, ok := v.ix.Resolve(ctx, target, from); ok {
		return entryRef(e)
	}
	return &nexusv1.NoteRef{Title: target}
}

func entryRef(e *index.Entry) *nexusv1.NoteRef {
	return &nexusv1.NoteRef{Id: e.Key(), Path: e.Path, Title: e.Title}
}

// noteKey is the API id of a note: its frontmatter id, else its path.
func noteKey(id, rel string) string {
	if id != "" {
		return id
	}
	return rel
}

func toStruct(m map[string]any) (*structpb.Struct, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	s := &structpb.Struct{}
	return s, protojson.Unmarshal(b, s)
}

// ---------------------------------------------------------------------------
// Search and graph

// Search implements kernel.Vault. Text with query filters (type:, tag:,
// links:, updated:…, see query.go) runs through the query engine.
func (v *Vault) Search(ctx context.Context, query string, types []string, limit int) ([]*nexusv1.SearchHit, error) {
	if LooksLikeQuery(query) {
		if len(types) > 0 {
			query += " type:" + strings.Join(types, ",")
		}
		res, err := v.Query(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		out := make([]*nexusv1.SearchHit, 0, len(res.Hits))
		for _, h := range res.Hits {
			out = append(out, &nexusv1.SearchHit{Note: entryRef(h.Entry), Snippet: h.Snippet, Score: float32(h.Score)})
		}
		return out, nil
	}
	hits, err := v.ix.Search(ctx, index.Query{Text: query, Types: types, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]*nexusv1.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, &nexusv1.SearchHit{Note: entryRef(h.Entry), Snippet: h.Snippet, Score: float32(h.Score)})
	}
	return out, nil
}

// Graph implements kernel.Vault. Node ids are note ids (frontmatter id, else
// path); dangling link targets appear as "unresolved:<target>" nodes.
func (v *Vault) Graph(ctx context.Context, req *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error) {
	center := ""
	if req.GetCenterId() != "" {
		e, ok := v.ix.Lookup(ctx, req.GetCenterId())
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, req.GetCenterId())
		}
		center = e.Path
	}
	depth := int(req.GetDepth())
	if depth > 5 {
		depth = 5
	}
	g, err := v.ix.Graph(ctx, center, depth, req.GetTypes(), int(req.GetLimit()))
	if err != nil {
		if errors.Is(err, index.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, req.GetCenterId())
		}
		return nil, err
	}
	filterGraph(g, center, req)
	resp := &nexusv1.GraphResponse{}
	keys := map[string]string{}
	for _, e := range g.Nodes {
		keys[e.Path] = e.Key()
		resp.Nodes = append(resp.Nodes, entryRef(e))
	}
	for _, t := range g.Dangling {
		resp.Nodes = append(resp.Nodes, &nexusv1.NoteRef{Id: index.UnresolvedPrefix + index.NormalizeTarget(t), Title: t})
	}
	for _, e := range g.Edges {
		dst := keys[e.Dst]
		if e.Dst == "" {
			dst = index.UnresolvedPrefix + index.NormalizeTarget(e.Target)
		}
		resp.Edges = append(resp.Edges, &nexusv1.GraphEdge{SourceId: keys[e.Src], TargetId: dst, Relation: e.Relation})
	}
	return resp, nil
}

// filterGraph applies the GraphRequest filters (relations, tags,
// path_prefix, hide_dangling, hide_orphans). The center is always kept.
func filterGraph(g *index.GraphResult, center string, req *nexusv1.GraphRequest) {
	keep := map[string]bool{}
	var nodes []*index.Entry
	for _, e := range g.Nodes {
		ok := e.Path == center
		if !ok {
			ok = (len(req.GetTags()) == 0 || index.HasAnyTag(e.Tags, req.GetTags())) &&
				(req.GetPathPrefix() == "" || strings.HasPrefix(e.Path, strings.TrimPrefix(req.GetPathPrefix(), "/")))
		}
		if ok {
			keep[e.Path] = true
			nodes = append(nodes, e)
		}
	}
	rels := map[string]bool{}
	for _, r := range req.GetRelations() {
		rels[strings.ToLower(r)] = true
	}
	var edges []index.Edge
	usedDangling := map[string]bool{}
	degree := map[string]int{}
	for _, e := range g.Edges {
		if len(rels) > 0 && !rels[strings.ToLower(e.Relation)] {
			continue
		}
		if !keep[e.Src] {
			continue
		}
		if e.Dst == "" {
			if req.GetHideDangling() {
				continue
			}
			usedDangling[index.NormalizeTarget(e.Target)] = true
		} else if !keep[e.Dst] {
			continue
		}
		degree[e.Src]++
		degree[e.Dst]++
		edges = append(edges, e)
	}
	if req.GetHideOrphans() {
		var kept []*index.Entry
		for _, n := range nodes {
			if degree[n.Path] > 0 || n.Path == center {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	var dangling []string
	for _, t := range g.Dangling {
		if usedDangling[index.NormalizeTarget(t)] {
			dangling = append(dangling, t)
		}
	}
	g.Nodes, g.Edges, g.Dangling = nodes, edges, dangling
}
