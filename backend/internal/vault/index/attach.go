package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Attachments are the non-note files of the vault (images, PDFs, audio…).
// They are indexed by content hash (deduplication), size, MIME type, EXIF
// date and extracted text (searchable through attachments_fts).

// AttachmentExtractor returns searchable text and a capture date for a
// file (both optional). data holds at most MaxExtractBytes.
type AttachmentExtractor func(rel string, data []byte) (text string, taken time.Time)

// MaxExtractBytes caps the bytes read for text extraction.
const MaxExtractBytes = 32 << 20

// Attachment is an indexed attachment.
type Attachment struct {
	Path     string
	Hash     string
	Size     int64
	MIME     string
	Modified time.Time
	Taken    time.Time
	Text     string
}

// SetAttachmentExtractor sets the text/EXIF extractor used when indexing
// attachments.
func (ix *Index) SetAttachmentExtractor(fn AttachmentExtractor) {
	ix.hmu.Lock()
	ix.extract = fn
	ix.hmu.Unlock()
}

// IsAttachmentPath reports whether rel is an indexable attachment.
func IsAttachmentPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return isAttachment(rel)
}

// IsAttachmentTarget reports whether a link target names a file (by
// extension) rather than a note.
func IsAttachmentTarget(t string) bool { return isAttachment(t) }

// WalkAttachments calls fn for every attachment under root.
func WalkAttachments(root string, fn func(rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
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
		if !IsAttachmentPath(rel) {
			return nil
		}
		return fn(rel, d)
	})
}

// MimeType guesses a MIME type from the extension.
func MimeType(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	switch ext {
	case ".heic":
		return "image/heic"
	case ".m4a":
		return "audio/mp4"
	case ".canvas", ".excalidraw":
		return "application/json"
	case ".md":
		return "text/markdown"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return strings.Split(t, ";")[0]
	}
	return "application/octet-stream"
}

// UpsertAttachmentFile (re)indexes root/rel when its size or mtime changed.
// A missing file is removed. It returns whether the index changed.
func (ix *Index) UpsertAttachmentFile(ctx context.Context, root, rel string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return true, ix.RemoveAttachment(ctx, rel)
	}
	if err != nil || fi.IsDir() {
		return false, err
	}
	var size, mod int64
	err = ix.db.QueryRowContext(ctx, `SELECT size, modified FROM attachments WHERE path = ?`, rel).Scan(&size, &mod)
	if err == nil && size == fi.Size() && mod == fi.ModTime().UnixMilli() {
		return false, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	head := &limitedBuffer{max: MaxExtractBytes}
	if _, err := io.Copy(io.MultiWriter(h, head), f); err != nil {
		return false, err
	}
	a := Attachment{Path: rel, Hash: hex.EncodeToString(h.Sum(nil)), Size: fi.Size(), MIME: MimeType(rel), Modified: fi.ModTime()}
	ix.hmu.RLock()
	ex := ix.extract
	ix.hmu.RUnlock()
	if ex != nil {
		a.Text, a.Taken = ex(rel, head.b)
	}
	return true, ix.PutAttachment(ctx, a)
}

type limitedBuffer struct {
	b   []byte
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// PutAttachment stores an attachment row.
func (ix *Index) PutAttachment(ctx context.Context, a Attachment) error {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteAttachmentTx(ctx, tx, a.Path); err != nil {
		return err
	}
	var taken int64
	if !a.Taken.IsZero() {
		taken = a.Taken.UnixMilli()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO attachments(path, hash, size, mime, modified, taken, text) VALUES(?,?,?,?,?,?,?)`,
		a.Path, a.Hash, a.Size, a.MIME, a.Modified.UnixMilli(), taken, a.Text)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO attachments_fts(rowid, name, text) VALUES(?,?,?)`, id, path.Base(a.Path), a.Text); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ix.gen.Add(1)
	return nil
}

func deleteAttachmentTx(ctx context.Context, tx *sql.Tx, rel string) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT rowid FROM attachments WHERE path = ?`, rel).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM attachments_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM attachments WHERE rowid = ?`, id)
	return err
}

// RemoveAttachment drops an attachment from the index.
func (ix *Index) RemoveAttachment(ctx context.Context, rel string) error {
	ix.wmu.Lock()
	defer ix.wmu.Unlock()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteAttachmentTx(ctx, tx, rel); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ix.gen.Add(1)
	return nil
}

// AttachmentFilter narrows ListAttachments.
type AttachmentFilter struct {
	Prefix string
	Query  string // FTS over name and extracted text
	Hash   string
	Limit  int
}

const attCols = `a.path, a.hash, a.size, a.mime, a.modified, a.taken, a.text`

func scanAttachment(sc interface{ Scan(...any) error }) (*Attachment, error) {
	var a Attachment
	var mod, taken int64
	if err := sc.Scan(&a.Path, &a.Hash, &a.Size, &a.MIME, &mod, &taken, &a.Text); err != nil {
		return nil, err
	}
	a.Modified = time.UnixMilli(mod)
	if taken != 0 {
		a.Taken = time.UnixMilli(taken)
	}
	return &a, nil
}

// ListAttachments returns indexed attachments ordered by path.
func (ix *Index) ListAttachments(ctx context.Context, f AttachmentFilter) ([]*Attachment, error) {
	q := `SELECT ` + attCols + ` FROM attachments a WHERE 1=1`
	var args []any
	if f.Query != "" {
		terms := tokenize(f.Query)
		if len(terms) == 0 {
			return nil, nil
		}
		q = `SELECT ` + attCols + ` FROM attachments_fts JOIN attachments a ON a.rowid = attachments_fts.rowid WHERE attachments_fts MATCH ?`
		args = append(args, matchExpr(terms, " "))
	}
	if f.Prefix != "" {
		q += ` AND substr(a.path, 1, ?) = ?`
		args = append(args, len(f.Prefix), f.Prefix)
	}
	if f.Hash != "" {
		q += ` AND a.hash = ?`
		args = append(args, f.Hash)
	}
	q += ` ORDER BY a.path`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := ix.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttachmentLinks returns, for every link target that names a file, the
// notes linking to it: normalised target → source paths. Targets are
// lower-case paths or file names as written.
func (ix *Index) AttachmentLinks(ctx context.Context) (map[string][]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT DISTINCT target, src FROM attachment_refs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t, s string
		if err := rows.Scan(&t, &s); err != nil {
			return nil, err
		}
		out[t] = append(out[t], s)
	}
	return out, rows.Err()
}

func (ix *Index) syncAttachments(ctx context.Context, root string, st *SyncStats) error {
	present := map[string]bool{}
	err := WalkAttachments(root, func(rel string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		present[rel] = true
		st.Attachments++
		if _, err := ix.UpsertAttachmentFile(ctx, root, rel); err != nil {
			return nil // unreadable attachment: skip
		}
		return nil
	})
	if err != nil {
		return err
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT path FROM attachments`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && !present[p] {
			stale = append(stale, p)
		}
	}
	rows.Close()
	for _, p := range stale {
		if err := ix.RemoveAttachment(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
