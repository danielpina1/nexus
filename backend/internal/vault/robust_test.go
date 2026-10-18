package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestWriterLock(t *testing.T) {
	root := t.TempDir()
	v := newVault(t, Options{Root: root})
	if _, err := Open(Options{Root: root, Logger: quiet}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open err = %v, want ErrLocked", err)
	}
	// NoLock (read-only tools) still works.
	ro, err := Open(Options{Root: root, Logger: quiet, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	ro.Close()
	v.Close()
	v2, err := Open(Options{Root: root, Logger: quiet})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	v2.Close()
	if b, _ := os.ReadFile(filepath.Join(root, ".nexus", ".gitignore")); !strings.Contains(string(b), "*") {
		t.Fatalf(".nexus/.gitignore = %q", b)
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	if _, err := v.Write(ctx, "a/b.md", "x", ""); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(v.Root(), "a"))
	if len(ents) != 1 {
		t.Fatalf("dir = %v", ents)
	}
	if _, err := v.Write(ctx, "big.md", strings.Repeat("x", DefaultMaxNoteBytes+1), ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestJournalRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := newVault(t, Options{Root: root})
	v.Write(ctx, "people/Ana.md", "# Ana\n", "")
	v.Write(ctx, "notes/a.md", "Met [[Ana]] and [[Ana|her]].\n", "")
	v.Write(ctx, "notes/b.md", "See [[people/Ana]].\n", "")
	// Crash after the first op of the rename journal.
	v.journalCrashAfter = 1
	_, err := v.Rename(ctx, "people/Ana.md", "people/Ana Silva.md", false, "")
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("rename err = %v", err)
	}
	if len(v.pendingJournals()) != 1 {
		t.Fatal("journal not left behind")
	}
	v.Close()
	// Reopen: the journal is rolled forward.
	v2 := newVault(t, Options{Root: root})
	if len(v2.pendingJournals()) != 0 {
		t.Fatal("journal not cleared")
	}
	if _, err := os.Stat(filepath.Join(root, "people/Ana.md")); !os.IsNotExist(err) {
		t.Fatal("old file still present")
	}
	a, _ := os.ReadFile(filepath.Join(root, "notes/a.md"))
	b, _ := os.ReadFile(filepath.Join(root, "notes/b.md"))
	if !strings.Contains(string(a), "[[Ana Silva]] and [[Ana Silva|her]]") || !strings.Contains(string(b), "[[people/Ana Silva]]") {
		t.Fatalf("links not rewritten:\n%s\n%s", a, b)
	}
	// An uncommitted journal (no manifest) is discarded.
	os.MkdirAll(filepath.Join(root, ".nexus/journal/x"), 0o755)
	os.WriteFile(filepath.Join(root, ".nexus/journal/x/0.data"), []byte("junk"), 0o644)
	v2.Close()
	v3 := newVault(t, Options{Root: root})
	if len(v3.pendingJournals()) != 0 {
		t.Fatal("uncommitted journal kept")
	}
}

func TestHistoryAndRestoreGit(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	n1, _ := v.Write(ctx, "a.md", "---\nid: A1\n---\none\n", "first")
	v.Write(ctx, "a.md", "---\nid: A1\n---\ntwo\n", "second")
	if _, err := v.Rename(ctx, "a.md", "folder/b.md", false, ""); err != nil {
		t.Fatal(err)
	}
	v.Write(ctx, "folder/b.md", "---\nid: A1\n---\nthree\n", "third")
	hist, err := v.History(ctx, "A1", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Fatalf("history = %+v", hist)
	}
	if hist[0].Message != "third" || hist[3].Message != "first" || hist[3].Path != "a.md" {
		t.Fatalf("history order/paths = %+v", hist)
	}
	if !strings.Contains(hist[0].Diff, "-two\n+three\n") || !strings.Contains(hist[3].Diff, "+one\n") {
		t.Fatalf("diffs:\n%s\n---\n%s", hist[0].Diff, hist[3].Diff)
	}
	_ = n1
	// Restore the first version (renamed since).
	n, err := v.RestoreVersion(ctx, "A1", hist[3].Commit, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(n.Body) != "one" || n.Ref.Path != "folder/b.md" {
		t.Fatalf("restored = %v", n)
	}
	hist2, _ := v.History(ctx, "folder/b.md", 1, false)
	if !strings.HasPrefix(hist2[0].Message, "restore folder/b.md") {
		t.Fatalf("restore commit = %+v", hist2)
	}
}

func TestHistorySnapshotsWithoutGit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "pre.md", "original\n")
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	v := newVault(t, Options{Root: root, StateDir: t.TempDir(), Now: now})
	if _, err := v.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	v.Write(ctx, "pre.md", "edited\n", "edit")
	v.Write(ctx, "pre.md", "edited twice\n", "edit 2")
	hist, err := v.History(ctx, "pre.md", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 || hist[2].Message != "(before first Nexus change)" || !strings.HasPrefix(hist[0].Commit, SnapshotPrefix) {
		t.Fatalf("snap history = %+v", hist)
	}
	if !strings.Contains(hist[0].Diff, "+edited twice") {
		t.Fatalf("diff = %s", hist[0].Diff)
	}
	b, err := v.GetVersion(ctx, "pre.md", hist[2].Commit)
	if err != nil || string(b) != "original\n" {
		t.Fatalf("version = %q %v", b, err)
	}
	n, err := v.RestoreVersion(ctx, "pre.md", hist[2].Commit, "")
	if err != nil || !strings.Contains(n.Body, "original") {
		t.Fatalf("restore = %v %v", n, err)
	}
	// Delete and history still has it; restore brings it back.
	if _, err := v.Delete(ctx, "pre.md", "", ""); err != nil {
		t.Fatal(err)
	}
	hist, _ = v.History(ctx, "pre.md", 0, false)
	if !hist[0].Deleted {
		t.Fatalf("delete not in history: %+v", hist[0])
	}
	if _, err := v.RestoreVersion(ctx, "pre.md", hist[1].Commit, ""); err != nil {
		t.Fatal(err)
	}
	// No git, no snapshots: history is unavailable.
	plain := newVault(t, Options{})
	plain.Write(ctx, "x.md", "x", "")
	if _, err := plain.History(ctx, "x.md", 0, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("history without store err = %v", err)
	}
}

func TestTrash(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	n, _ := v.Write(ctx, "people/Bo.md", "# Bo\n", "")
	if _, err := v.Delete(ctx, "people/Bo.md", "sha256:stale", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete err = %v", err)
	}
	it, err := v.Delete(ctx, n.Ref.Id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Read(ctx, "people/Bo.md"); !errors.Is(err, ErrNotFound) {
		t.Fatal("note still readable")
	}
	if hits, _ := v.Search(ctx, "Bo", nil, 5); len(hits) != 0 {
		t.Fatal("trashed note still indexed")
	}
	if st := gitOut(t, root, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("git status after delete = %q", st)
	}
	items, _ := v.ListTrash(ctx)
	if len(items) != 1 || items[0].Path != "people/Bo.md" || items[0].Title != "Bo" {
		t.Fatalf("trash = %+v", items)
	}
	// Restore when the original path is taken → free name.
	v.Write(ctx, "people/Bo.md", "# Other Bo\n", "")
	p, err := v.RestoreFromTrash(ctx, it.ID, "")
	if err != nil || p != "people/Bo-2.md" {
		t.Fatalf("restore = %q %v", p, err)
	}
	if got, err := v.Read(ctx, n.Ref.Id); err != nil || got.Ref.Path != "people/Bo-2.md" {
		t.Fatalf("restored note = %v %v", got, err)
	}
	if items, _ := v.ListTrash(ctx); len(items) != 0 {
		t.Fatalf("trash after restore = %+v", items)
	}
	// Purge by age.
	v.Delete(ctx, "people/Bo.md", "", "")
	if n, _ := v.PurgeTrash(ctx, "", time.Hour); n != 0 {
		t.Fatal("purged a fresh item")
	}
	if n, _ := v.PurgeTrash(ctx, "", 0); n != 1 {
		t.Fatal("purge all failed")
	}
	if _, err := v.RestoreFromTrash(ctx, "../x", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id err = %v", err)
	}
}

func TestConflictMergeAndCopy(t *testing.T) {
	ctx := context.Background()
	var notices []Notice
	v := newVault(t, Options{KeepConflicts: true})
	v.SetNoticeSink(func(n Notice) { notices = append(notices, n) })
	base, _ := v.Write(ctx, "n.md", "---\nid: N1\n---\nline 1\nline 2\nline 3\n", "")
	// Someone else edits line 1.
	if _, err := v.WriteNote(ctx, "n.md", []byte("---\nid: N1\n---\nline ONE\nline 2\nline 3\n"), "", base.Commit); err != nil {
		t.Fatal(err)
	}
	// A stale writer edits line 3: merged cleanly.
	r, err := v.WriteNoteEx(ctx, "n.md", []byte("---\nid: N1\n---\nline 1\nline 2\nline THREE\n"), "", base.Commit)
	if err != nil || !r.Merged || strings.TrimSpace(r.Note.Body) != "line ONE\nline 2\nline THREE" {
		t.Fatalf("merge = %+v %v", r, err)
	}
	// Overlapping edit: conflict copy + notice.
	_, err = v.WriteNoteEx(ctx, "n.md", []byte("---\nid: N1\n---\nline uno\nline 2\nline 3\n"), "", base.Commit)
	var ce *ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConflict) || !strings.HasPrefix(ce.CopyPath, "n (conflict ") {
		t.Fatalf("conflict err = %v", err)
	}
	cp, err := v.Read(ctx, ce.CopyPath)
	if err != nil || cp.Ref.Id == "N1" || !strings.Contains(cp.Body, "line uno") {
		t.Fatalf("copy = %v %v", cp, err)
	}
	if len(notices) != 1 || notices[0].Kind != nexusv1.NoticeKind_NOTICE_KIND_ALERT {
		t.Fatalf("notices = %+v", notices)
	}
	cur, _ := v.Read(ctx, "N1")
	if !strings.Contains(cur.Body, "line ONE") {
		t.Fatal("original changed by a conflicting write")
	}
}

func TestConflictMergeGitBase(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	v.Write(ctx, "g.md", "a\nb\nc\n", "")
	base, _ := v.Read(ctx, "g.md")
	if strings.HasPrefix(base.Commit, HashPrefix) {
		t.Fatalf("expected git revision, got %s", base.Commit)
	}
	v.WriteNote(ctx, "g.md", append([]byte(nil), []byte(strings.Replace(rawOf(t, v, "g.md"), "a\n", "A\n", 1))...), "", "")
	r, err := v.WriteNoteEx(ctx, "g.md", []byte(strings.Replace(rawOf(t, v, "g.md"), "A\n", "a\n", 1)+"d\n"), "", base.Commit)
	if err != nil || !r.Merged {
		t.Fatalf("git-base merge = %+v %v", r, err)
	}
	if body := rawOf(t, v, "g.md"); !strings.Contains(body, "A\nb\nc\nd\n") {
		t.Fatalf("merged = %q", body)
	}
}

func rawOf(t *testing.T, v *Vault, rel string) string {
	t.Helper()
	b, _, err := v.ReadRaw(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEncryption(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	root := t.TempDir()
	v := newVault(t, Options{Root: root, Encryption: &EncryptionOptions{Folders: []string{"private/"}, Key: key}})
	n, err := v.Write(ctx, "private/diary.md", "---\ntags: [secret]\n---\n# Diary\nmy secret thoughts about [[Ana]]\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if !n.Encrypted {
		t.Fatal("note not flagged encrypted")
	}
	disk, _ := os.ReadFile(filepath.Join(root, "private/diary.md"))
	if !IsEncrypted(disk) || strings.Contains(string(disk), "secret") {
		t.Fatalf("file not encrypted: %q", disk)
	}
	got, err := v.Read(ctx, "private/diary.md")
	if err != nil || !strings.Contains(got.Body, "secret thoughts") {
		t.Fatalf("read = %v %v", got, err)
	}
	// Not searchable by body; still in the graph by title/tags/links.
	if hits, _ := v.Search(ctx, "thoughts", nil, 5); len(hits) != 0 {
		t.Fatalf("encrypted body searchable: %v", hits)
	}
	if hits, _ := v.Search(ctx, "diary", nil, 5); len(hits) != 1 {
		t.Fatalf("title not searchable: %v", hits)
	}
	if v.AgentMayRead("private/diary.md") || !v.AgentMayRead("public.md") {
		t.Fatal("agent access rules")
	}
	// A rebuild (index from disk) decrypts too.
	v.Index().Rebuild(ctx, root)
	if e, ok := v.Index().Get(ctx, "private/diary.md"); !ok || e.Title != "Diary" {
		t.Fatalf("rebuilt entry = %+v", e)
	}
	// Tampering is detected.
	tampered := []byte(strings.Replace(string(disk), "A", "B", 1))
	if string(tampered) != string(disk) {
		os.WriteFile(filepath.Join(root, "private/diary.md"), tampered, 0o644)
		if _, err := v.Read(ctx, "private/diary.md"); err == nil {
			t.Fatal("tampered file decrypted")
		}
	}
	// A vault without the key cannot read it.
	v.Close()
	nokey := newVault(t, Options{Root: root})
	if _, _, err := nokey.ReadRaw(ctx, "private/diary.md"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no-key err = %v", err)
	}
}

func TestKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k", "vault.key")
	k1, err := LoadOrCreateKeyFile(p)
	if err != nil || len(k1) != 32 {
		t.Fatal(err)
	}
	k2, _ := LoadOrCreateKeyFile(p)
	if string(k1) != string(k2) {
		t.Fatal("key changed")
	}
	os.Chmod(p, 0o644)
	if _, err := LoadOrCreateKeyFile(p); err == nil {
		t.Fatal("world-readable key accepted")
	}
}

func TestChangeFeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := newVault(t, Options{})
	start := v.FeedHead()
	got := make(chan Change, 10)
	done := make(chan error, 1)
	go func() {
		done <- v.WatchChanges(ctx, 0, "notes/", func(c Change) error { got <- c; return nil })
	}()
	time.Sleep(50 * time.Millisecond)
	v.Write(ctx, "notes/a.md", "a", "")
	v.Write(ctx, "other/b.md", "b", "")
	v.Write(ctx, "notes/a.md", "a2", "")
	v.Rename(ctx, "notes/a.md", "notes/c.md", false, "")
	v.Delete(ctx, "notes/c.md", "", "")
	want := []nexusv1.VaultChangeKind{
		nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_UPDATED,
		nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RENAMED, nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_DELETED,
	}
	var last int64
	for i, w := range want {
		select {
		case c := <-got:
			if c.Kind != w || c.Rev <= last {
				t.Fatalf("change %d = %+v, want %v", i, c, w)
			}
			last = c.Rev
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for change %d", i)
		}
	}
	// Resume from a revision: backlog only.
	var replay []Change
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	_ = v.WatchChanges(ctx2, start, "", func(c Change) error { replay = append(replay, c); return nil })
	if len(replay) != 5 {
		t.Fatalf("replay = %d", len(replay))
	}
	// Too old: RESYNC first.
	var first Change
	ctx3, cancel3 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel3()
	_ = v.WatchChanges(ctx3, 1, "", func(c Change) error {
		if first.Kind == 0 {
			first = c
		}
		return nil
	})
	if first.Kind != nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_RESYNC {
		t.Fatalf("first = %+v", first)
	}
	cancel()
	<-done
}
