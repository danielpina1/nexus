// Package index is the SQLite cache over the vault: notes, link edges and an
// FTS5 full-text table. Files are the truth; the index can be deleted and
// rebuilt at any time (Rebuild).
//
// Schema (index.db, separate from the hub store):
//
//	notes(rowid, path UNIQUE, id, type, title, name, aliases JSON, tags JSON, hash, updated unix-ms,
//	      fm JSON frontmatter, created unix-ms, size bytes)
//	edges(src path, dst normalised target, relation, raw target, heading, embed, line)
//	tasks(path, line, status, done, text, due, scheduled, start, done_date, priority, tags JSON, raw)
//	attachments(rowid, path UNIQUE, hash, size, mime, modified, taken, text) + attachments_fts(name, text)
//	notes_fts(title, aliases, body, tags)   -- FTS5, rowid = notes.rowid
//	vectors(path, chunk, hash, text, vec)   -- optional embeddings (see vector.go)
//	kv(key, value)                          -- small module state (capture idempotency)
//
// Edges store the link target as written (normalised) rather than a resolved
// note, so links resolve lazily, exactly like Obsidian: creating or renaming a
// note immediately fixes previously dangling links.
package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielpina1/nexus/backend/internal/sqlitex"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

var schema = []string{
	`CREATE TABLE IF NOT EXISTS notes(
		rowid INTEGER PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		id TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		aliases TEXT NOT NULL DEFAULT '[]',
		tags TEXT NOT NULL DEFAULT '[]',
		hash TEXT NOT NULL DEFAULT '',
		updated INTEGER NOT NULL DEFAULT 0,
		fm TEXT NOT NULL DEFAULT '{}',
		created INTEGER NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS notes_id ON notes(id)`,
	`CREATE INDEX IF NOT EXISTS notes_type ON notes(type)`,
	`CREATE INDEX IF NOT EXISTS notes_name ON notes(name)`,
	`CREATE TABLE IF NOT EXISTS edges(
		src TEXT NOT NULL,
		dst TEXT NOT NULL,
		relation TEXT NOT NULL,
		raw TEXT NOT NULL,
		heading TEXT NOT NULL DEFAULT '',
		embed INTEGER NOT NULL DEFAULT 0,
		line INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS edges_src ON edges(src)`,
	`CREATE INDEX IF NOT EXISTS edges_dst ON edges(dst)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(title, aliases, body, tags, tokenize='unicode61 remove_diacritics 2')`,
	`CREATE TABLE IF NOT EXISTS vectors(
		path TEXT NOT NULL,
		chunk INTEGER NOT NULL,
		hash TEXT NOT NULL,
		text TEXT NOT NULL,
		vec BLOB NOT NULL,
		PRIMARY KEY(path, chunk)
	)`,
	`CREATE TABLE IF NOT EXISTS kv(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tasks(
		path TEXT NOT NULL,
		line INTEGER NOT NULL,
		status TEXT NOT NULL,
		done INTEGER NOT NULL,
		text TEXT NOT NULL,
		due TEXT NOT NULL DEFAULT '',
		scheduled TEXT NOT NULL DEFAULT '',
		start TEXT NOT NULL DEFAULT '',
		done_date TEXT NOT NULL DEFAULT '',
		priority TEXT NOT NULL DEFAULT '',
		tags TEXT NOT NULL DEFAULT '[]',
		raw TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS tasks_path ON tasks(path)`,
	`CREATE INDEX IF NOT EXISTS tasks_due ON tasks(due)`,
	`CREATE TABLE IF NOT EXISTS attachments(
		rowid INTEGER PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		hash TEXT NOT NULL,
		size INTEGER NOT NULL,
		mime TEXT NOT NULL DEFAULT '',
		modified INTEGER NOT NULL DEFAULT 0,
		taken INTEGER NOT NULL DEFAULT 0,
		text TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS attachments_hash ON attachments(hash)`,
	`CREATE TABLE IF NOT EXISTS attachment_refs(src TEXT NOT NULL, target TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS attachment_refs_src ON attachment_refs(src)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS attachments_fts USING fts5(name, text, tokenize='unicode61 remove_diacritics 2')`,
}

// schemaVersion bumps force a re-index of every note (new derived columns).
const schemaVersion = "2"

// migrations add columns to databases created by older versions.
var migrations = []struct{ table, column, ddl string }{
	{"notes", "fm", `ALTER TABLE notes ADD COLUMN fm TEXT NOT NULL DEFAULT '{}'`},
	{"notes", "created", `ALTER TABLE notes ADD COLUMN created INTEGER NOT NULL DEFAULT 0`},
	{"notes", "size", `ALTER TABLE notes ADD COLUMN size INTEGER NOT NULL DEFAULT 0`},
	{"edges", "line", `ALTER TABLE edges ADD COLUMN line INTEGER NOT NULL DEFAULT 0`},
}

// Entry is an indexed note.
type Entry struct {
	Path    string
	ID      string // frontmatter id; empty when the note has none
	Type    string
	Title   string
	Aliases []string
	Tags    []string
	Hash    string
	Updated time.Time
	// Frontmatter is the parsed YAML (JSON-compatible values).
	Frontmatter map[string]any
	// Created is the frontmatter created/date value, else Updated.
	Created time.Time
	// Size is the content size in bytes.
	Size int64
}

// Key is the stable identifier exposed in APIs: the frontmatter id, or the
// vault path for notes without one.
func (e *Entry) Key() string {
	if e.ID != "" {
		return e.ID
	}
	return e.Path
}

// Index is safe for concurrent use.
type Index struct {
	db  *sql.DB
	wmu sync.Mutex // serialises writers
	gen atomic.Int64

	rmu    sync.Mutex
	res    *resolver
	resGen int64

	vmu sync.RWMutex
	vec VectorSearcher

	hmu     sync.RWMutex
	decode  func(rel string, raw []byte) ([]byte, error)
	private func(rel string) bool
	extract AttachmentExtractor
}

// SetDecoder sets the function that turns on-disk bytes into note text
// (decryption). Files that fail to decode are indexed by name only.
func (ix *Index) SetDecoder(fn func(rel string, raw []byte) ([]byte, error)) {
	ix.hmu.Lock()
	ix.decode = fn
	ix.hmu.Unlock()
}

// SetPrivate marks notes whose body and tasks must not be indexed (only
// title, aliases, tags and links are).
func (ix *Index) SetPrivate(fn func(rel string) bool) {
	ix.hmu.Lock()
	ix.private = fn
	ix.hmu.Unlock()
}

func (ix *Index) isPrivate(rel string) bool {
	ix.hmu.RLock()
	fn := ix.private
	ix.hmu.RUnlock()
	return fn != nil && fn(rel)
}

// Open opens or creates the index database at path.
func Open(dbPath string) (*Index, error) {
	db, err := sqlitex.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return newIndex(db)
}

// OpenMemory opens an in-memory index (tests).
func OpenMemory() (*Index, error) {
	db, err := sqlitex.OpenMemory()
	if err != nil {
		return nil, err
	}
	return newIndex(db)
}

func newIndex(db *sql.DB) (*Index, error) {
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

func migrate(db *sql.DB) error {
	// Old databases: add the missing columns before the CREATE INDEX
	// statements that may reference them.
	for _, m := range migrations {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, m.table).Scan(&n); err != nil {
			return fmt.Errorf("index schema: %w", err)
		}
		if n == 0 {
			continue // created fresh below
		}
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, m.table, m.column).Scan(&n); err != nil {
			return fmt.Errorf("index schema: %w", err)
		}
		if n == 0 {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("index migrate: %w", err)
			}
		}
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("index schema: %w", err)
		}
	}
	var v string
	_ = db.QueryRow(`SELECT value FROM kv WHERE key = 'schema'`).Scan(&v)
	if v != schemaVersion {
		// Derived columns changed: force every note to be re-indexed.
		if _, err := db.Exec(`UPDATE notes SET hash = ''`); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO kv(key, value) VALUES('schema', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, schemaVersion); err != nil {
			return err
		}
	}
	return nil
}

// DB exposes the database (vector store, module state).
func (ix *Index) DB() *sql.DB { return ix.db }

// Close closes the database.
func (ix *Index) Close() error { return ix.db.Close() }

// Generation increases on every change; useful for caches.
func (ix *Index) Generation() int64 { return ix.gen.Load() }

// Hash returns the content hash used for change detection.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// IsNotePath reports whether a vault-relative path is an indexable note
// (.md, not inside a hidden directory such as .git, .obsidian or .trash).
func IsNotePath(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !strings.EqualFold(path.Ext(rel), ".md") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// WalkNotes calls fn for every note under root with its slash path.
func WalkNotes(root string, fn func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entry: skip
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !IsNotePath(rel) {
			return nil
		}
		return fn(rel, d)
	})
}

// Upsert indexes a note if its content changed. It returns whether the
// index was modified.
func (ix *Index) Upsert(ctx context.Context, rel string, src []byte, updated time.Time) (bool, error) {
	h := Hash(src)
	var old string
	err := ix.db.QueryRowContext(ctx, `SELECT hash FROM notes WHERE path = ?`, rel).Scan(&old)
	if err == nil && old == h {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	n := note.Parse(rel, src)
	aliases, _ := json.Marshal(nonNil(n.Aliases))
	tags, _ := json.Marshal(nonNil(n.Tags))
	fm, err := json.Marshal(n.Frontmatter)
	if err != nil {
		fm = []byte("{}")
	}
	created := frontmatterTime(n.Frontmatter, "created", "date")
	if created.IsZero() {
		created = updated
	}
	private := ix.isPrivate(rel)
	body := n.Body
	if private {
		body = ""
	}

	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := deleteTx(ctx, tx, rel); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO notes(path, id, type, title, name, aliases, tags, hash, updated, fm, created, size) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		rel, n.ID, n.Type, n.Title, baseName(rel), string(aliases), string(tags), h, updated.UnixMilli(), string(fm), created.UnixMilli(), len(src))
	if err != nil {
		return false, err
	}
	rowid, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO notes_fts(rowid, title, aliases, body, tags) VALUES(?,?,?,?,?)`,
		rowid, n.Title, strings.Join(n.Aliases, " "), body, strings.Join(n.Tags, " ")); err != nil {
		return false, err
	}
	if !private {
		for _, t := range n.Tasks {
			done := 0
			if t.Done {
				done = 1
			}
			ttags, _ := json.Marshal(nonNil(t.Tags))
			if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(path, line, status, done, text, due, scheduled, start, done_date, priority, tags, raw) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				rel, t.Line, t.Status, done, t.Text, t.Due, t.Scheduled, t.Start, t.DoneDate, t.Priority, string(ttags), t.Raw); err != nil {
				return false, err
			}
		}
	}
	seen := map[string]bool{}
	for _, l := range n.Links {
		if isAttachment(l.Target) {
			t := strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(l.Target, "%20", " "), "./"))
			if _, err := tx.ExecContext(ctx, `INSERT INTO attachment_refs(src, target) VALUES(?,?)`, rel, t); err != nil {
				return false, err
			}
			continue
		}
		dst := NormalizeTarget(l.Target)
		k := dst + "\x00" + l.Relation
		if dst == "" || seen[k] {
			continue
		}
		seen[k] = true
		embed := 0
		if l.Embed {
			embed = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src, dst, relation, raw, heading, embed, line) VALUES(?,?,?,?,?,?,?)`,
			rel, dst, l.Relation, l.Target, l.Heading, embed, l.Line); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	ix.gen.Add(1)
	return true, nil
}

// UpsertFile reads root/rel and indexes it; a missing file is removed.
func (ix *Index) UpsertFile(ctx context.Context, root, rel string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return true, ix.Remove(ctx, rel)
	}
	if err != nil {
		return false, err
	}
	if fi.IsDir() {
		return false, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	ix.hmu.RLock()
	dec := ix.decode
	ix.hmu.RUnlock()
	if dec != nil {
		plain, err := dec(rel, b)
		if err != nil {
			plain = nil // undecodable (no key): indexed by name only
		}
		b = plain
	}
	return ix.Upsert(ctx, rel, b, fi.ModTime())
}

// Remove deletes a note from the index.
func (ix *Index) Remove(ctx context.Context, rel string) error {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteTx(ctx, tx, rel); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vectors WHERE path = ?`, rel); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ix.gen.Add(1)
	return nil
}

func deleteTx(ctx context.Context, tx *sql.Tx, rel string) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, `SELECT rowid FROM notes WHERE path = ?`, rel).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM notes_fts WHERE rowid = ?`, `DELETE FROM notes WHERE rowid = ?`} {
		if _, err := tx.ExecContext(ctx, q, rowid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE path = ?`, rel); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachment_refs WHERE src = ?`, rel); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM edges WHERE src = ?`, rel)
	return err
}

// SyncStats reports what Sync did.
type SyncStats struct {
	Scanned, Updated, Removed int
	// Attachments is the number of attachment files seen.
	Attachments int
}

// Sync brings the index up to date with the files under root (incremental:
// unchanged files are skipped by hash, deleted files are removed).
func (ix *Index) Sync(ctx context.Context, root string) (SyncStats, error) {
	var st SyncStats
	present := map[string]bool{}
	err := WalkNotes(root, func(rel string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		st.Scanned++
		present[rel] = true
		changed, err := ix.UpsertFile(ctx, root, rel)
		if err != nil {
			return fmt.Errorf("index %s: %w", rel, err)
		}
		if changed {
			st.Updated++
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	paths, err := ix.paths(ctx)
	if err != nil {
		return st, err
	}
	for _, p := range paths {
		if !present[p] {
			if err := ix.Remove(ctx, p); err != nil {
				return st, err
			}
			st.Removed++
		}
	}
	if err := ix.syncAttachments(ctx, root, &st); err != nil {
		return st, err
	}
	return st, nil
}

// Rebuild drops every indexed note and re-indexes root from scratch.
func (ix *Index) Rebuild(ctx context.Context, root string) (SyncStats, error) {
	ix.wmu.Lock()
	for _, q := range []string{`DELETE FROM notes_fts`, `DELETE FROM notes`, `DELETE FROM edges`, `DELETE FROM vectors`, `DELETE FROM tasks`, `DELETE FROM attachments`, `DELETE FROM attachments_fts`, `DELETE FROM attachment_refs`} {
		if _, err := ix.db.ExecContext(ctx, q); err != nil {
			ix.wmu.Unlock()
			return SyncStats{}, err
		}
	}
	ix.wmu.Unlock()
	ix.gen.Add(1)
	return ix.Sync(ctx, root)
}

func (ix *Index) paths(ctx context.Context) ([]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path FROM notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const entryCols = `path, id, type, title, aliases, tags, hash, updated, fm, created, size`

// entryColsN is entryCols qualified with the "n" table alias.
const entryColsN = `n.path, n.id, n.type, n.title, n.aliases, n.tags, n.hash, n.updated, n.fm, n.created, n.size`

func scanEntry(sc interface{ Scan(...any) error }, extra ...any) (*Entry, error) {
	var e Entry
	var aliases, tags, fm string
	var updated, created int64
	dst := append([]any{&e.Path, &e.ID, &e.Type, &e.Title, &aliases, &tags, &e.Hash, &updated, &fm, &created, &e.Size}, extra...)
	if err := sc.Scan(dst...); err != nil {
		return nil, err
	}
	fill(&e, aliases, tags, updated)
	_ = json.Unmarshal([]byte(fm), &e.Frontmatter)
	if e.Frontmatter == nil {
		e.Frontmatter = map[string]any{}
	}
	e.Created = time.UnixMilli(created)
	if created == 0 {
		e.Created = e.Updated
	}
	return &e, nil
}

// frontmatterTime parses the first date-like frontmatter value of keys.
func frontmatterTime(fm map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		if t, ok := ParseTime(fm[k]); ok {
			return t
		}
	}
	return time.Time{}
}

// ParseTime parses a frontmatter date (RFC 3339, "YYYY-MM-DD",
// "YYYY-MM-DD HH:MM", "YYYY-MM-DDTHH:MM").
func ParseTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (ix *Index) queryEntries(ctx context.Context, q string, args ...any) ([]*Entry, error) {
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns the entry at a vault path.
func (ix *Index) Get(ctx context.Context, rel string) (*Entry, bool) {
	e, err := scanEntry(ix.db.QueryRowContext(ctx, `SELECT `+entryCols+` FROM notes WHERE path = ?`, rel))
	return e, err == nil
}

// Lookup finds a note by frontmatter id, then by path (with or without .md).
func (ix *Index) Lookup(ctx context.Context, idOrPath string) (*Entry, bool) {
	if idOrPath == "" {
		return nil, false
	}
	if e, err := scanEntry(ix.db.QueryRowContext(ctx, `SELECT `+entryCols+` FROM notes WHERE id = ? ORDER BY path LIMIT 1`, idOrPath)); err == nil {
		return e, true
	}
	p := strings.TrimPrefix(filepath.ToSlash(idOrPath), "/")
	if e, ok := ix.Get(ctx, p); ok {
		return e, true
	}
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		return ix.Get(ctx, p+".md")
	}
	return nil, false
}

// ListByType returns notes of a type ordered by path. Empty type = all notes.
func (ix *Index) ListByType(ctx context.Context, typ string) ([]*Entry, error) {
	if typ == "" {
		return ix.queryEntries(ctx, `SELECT `+entryCols+` FROM notes ORDER BY path`)
	}
	return ix.queryEntries(ctx, `SELECT `+entryCols+` FROM notes WHERE type = ? ORDER BY path`, typ)
}

// ListPrefix returns notes whose path starts with prefix, ordered by path.
func (ix *Index) ListPrefix(ctx context.Context, prefix string) ([]*Entry, error) {
	return ix.queryEntries(ctx, `SELECT `+entryCols+` FROM notes WHERE substr(path, 1, ?) = ? ORDER BY path`, len(prefix), prefix)
}

// KVGet / KVPut store small module state (e.g. capture idempotency keys).
// Note: this state is lost by deleting index.db, which is acceptable for
// short-lived deduplication only.
func (ix *Index) KVGet(ctx context.Context, key string) (string, bool) {
	var v string
	err := ix.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	return v, err == nil
}

func (ix *Index) KVPut(ctx context.Context, key, value string) error {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	_, err := ix.db.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func baseName(rel string) string {
	b := path.Base(rel)
	return strings.ToLower(strings.TrimSuffix(b, path.Ext(b)))
}

// attachmentExts are link targets that are files, not notes.
var attachmentExts = note.FileExts

func isAttachment(target string) bool {
	return attachmentExts[strings.ToLower(path.Ext(target))]
}

// NormalizeTarget normalises a link target for storage and lookup:
// slashes, no leading "./" or "/", no .md extension, lower case.
func NormalizeTarget(t string) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "\\", "/"))
	t = strings.TrimPrefix(t, "./")
	t = strings.TrimPrefix(t, "/")
	if strings.HasSuffix(strings.ToLower(t), ".md") {
		t = t[:len(t)-3]
	}
	return strings.ToLower(t)
}

// sortPaths orders candidate paths: shortest first, then alphabetical
// (Obsidian's "shortest path" preference for ambiguous names).
func sortPaths(ps []string) {
	sort.Slice(ps, func(i, j int) bool {
		di, dj := strings.Count(ps[i], "/"), strings.Count(ps[j], "/")
		if di != dj {
			return di < dj
		}
		return ps[i] < ps[j]
	})
}
