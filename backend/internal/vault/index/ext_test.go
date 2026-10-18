package index

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/sqlitex"
)

func TestMigrateOldSchema(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "index.db")
	db, err := sqlitex.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// The v1 schema (before fm/created/size/line, tasks and attachments).
	for _, s := range []string{
		`CREATE TABLE notes(rowid INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, id TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', aliases TEXT NOT NULL DEFAULT '[]', tags TEXT NOT NULL DEFAULT '[]', hash TEXT NOT NULL DEFAULT '', updated INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE edges(src TEXT NOT NULL, dst TEXT NOT NULL, relation TEXT NOT NULL, raw TEXT NOT NULL, heading TEXT NOT NULL DEFAULT '', embed INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO notes(path, hash) VALUES('a.md', 'oldhash')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	ix, err := Open(p)
	if err != nil {
		t.Fatalf("open old schema: %v", err)
	}
	defer ix.Close()
	e, ok := ix.Get(ctx, "a.md")
	if !ok || e.Hash != "" {
		t.Fatalf("old rows not marked for re-index: %+v", e)
	}
	root := t.TempDir()
	write(t, root, "a.md", "---\ncreated: 2026-01-02\nstatus: x\n---\n- [ ] task\n[[b]]\n")
	if _, err := ix.Sync(ctx, root); err != nil {
		t.Fatal(err)
	}
	e, _ = ix.Get(ctx, "a.md")
	if e.Frontmatter["status"] != "x" || e.Created.Format("2006-01-02") != "2026-01-02" || e.Size == 0 {
		t.Fatalf("entry after migration = %+v", e)
	}
	out, _ := ix.Outlinks(ctx, "a.md")
	if len(out) != 1 || out[0].Line != 6 {
		t.Fatalf("edge lines = %+v", out)
	}
	ts, _ := ix.Tasks(ctx, TaskFilter{})
	if len(ts) != 1 || ts[0].Line != 5 {
		t.Fatalf("tasks = %+v", ts)
	}
}

func TestPrivateNotesAndDecoder(t *testing.T) {
	ctx := context.Background()
	ix, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ix.SetPrivate(func(rel string) bool { return strings.HasPrefix(rel, "private/") })
	ix.SetDecoder(func(_ string, raw []byte) ([]byte, error) { return []byte(strings.ToUpper(string(raw))), nil })
	root := t.TempDir()
	write(t, root, "private/secret.md", "# diary\nhidden words\n- [ ] secret task\n")
	write(t, root, "public.md", "# pub\nvisible words\n")
	if _, err := ix.Sync(ctx, root); err != nil {
		t.Fatal(err)
	}
	if hits, _ := ix.Search(ctx, Query{Text: "hidden"}); len(hits) != 0 {
		t.Fatalf("private body indexed: %+v", hits)
	}
	if hits, _ := ix.Search(ctx, Query{Text: "visible"}); len(hits) != 1 {
		t.Fatalf("public body not indexed: %+v", hits)
	}
	if e, _ := ix.Get(ctx, "private/secret.md"); e.Title != "DIARY" {
		t.Fatalf("decoder not used: %+v", e)
	}
	if ts, _ := ix.Tasks(ctx, TaskFilter{}); len(ts) != 0 {
		t.Fatalf("private tasks indexed: %+v", ts)
	}
}

func TestAttachmentIndex(t *testing.T) {
	ctx := context.Background()
	ix, root := setup(t, map[string]string{"n.md": "![[_attachments/a.png]] [x](docs/r.pdf)\n"})
	ix.SetAttachmentExtractor(func(rel string, data []byte) (string, time.Time) {
		if strings.HasSuffix(rel, ".txt") {
			return string(data), time.Time{}
		}
		return "", time.Time{}
	})
	write(t, root, "_attachments/a.png", "png")
	write(t, root, "docs/r.pdf", "%PDF")
	write(t, root, "docs/t.txt", "invoice total")
	write(t, root, ".hidden/x.png", "no")
	st, err := ix.Sync(ctx, root)
	if err != nil || st.Attachments != 3 {
		t.Fatalf("sync = %+v %v", st, err)
	}
	all, _ := ix.ListAttachments(ctx, AttachmentFilter{})
	if len(all) != 3 || all[0].MIME != "image/png" {
		t.Fatalf("attachments = %+v", all)
	}
	if f, _ := ix.ListAttachments(ctx, AttachmentFilter{Query: "invoice"}); len(f) != 1 || f[0].Path != "docs/t.txt" {
		t.Fatalf("fts = %+v", f)
	}
	refs, _ := ix.AttachmentLinks(ctx)
	if len(refs["_attachments/a.png"]) != 1 || len(refs["docs/r.pdf"]) != 1 {
		t.Fatalf("refs = %v", refs)
	}
	write(t, root, "docs/t.txt", "changed")
	if changed, _ := ix.UpsertAttachmentFile(ctx, root, "docs/t.txt"); !changed {
		t.Fatal("changed attachment not reindexed")
	}
	if changed, _ := ix.UpsertAttachmentFile(ctx, root, "docs/t.txt"); changed {
		t.Fatal("unchanged attachment reindexed")
	}
}

func TestRRFKeepsTextHitsFirst(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	emb := &fakeEmb{}
	v := NewVectors(ix, emb)
	if _, err := v.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	ix.SetVectorSearcher(v)
	hits, err := ix.Search(ctx, Query{Text: "brewing"})
	if err != nil || len(hits) == 0 || hits[0].Entry.Path != "topics/Coffee.md" {
		t.Fatalf("hits = %+v %v", hits, err)
	}
	for _, h := range hits {
		if h.Score < 0 || h.Score > 2.5 {
			t.Fatalf("score out of range: %+v", h)
		}
	}
	sim, err := ix.SimilarNotes(ctx, "companies/ACME.md", 3)
	if err != nil || len(sim) == 0 {
		t.Fatalf("similar = %+v %v", sim, err)
	}
}
