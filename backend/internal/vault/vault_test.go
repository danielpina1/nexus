package vault

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newVault(t *testing.T, o Options) *Vault {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.IndexPath == "" {
		o.IndexPath = filepath.Join(t.TempDir(), "index.db")
	}
	o.Logger = quiet
	v, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"people/João.md", "people/João.md", true},
		{"a/./b/../c.md", "a/c.md", true},
		{`win\style.md`, "win/style.md", true},
		{"../etc/passwd", "", false},
		{"a/../../x.md", "", false},
		{"/etc/passwd", "", false},
		{"C:/x.md", "", false},
		{".git/config", "", false},
		{"a/.obsidian/x.md", "", false},
		{"", "", false},
		{"..", "", false},
		{"a\x00b", "", false},
	}
	for _, tt := range tests {
		got, err := CleanPath(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("CleanPath(%q) = %q, %v", tt.in, got, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanPath(%q) error %v is not ErrInvalidPath", tt.in, err)
		}
	}
}

func TestWriteRules(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	for _, p := range []string{"../x.md", "x.txt", "a/b.json", ".hidden.md", "/abs.md"} {
		if _, err := v.Write(ctx, p, "x", ""); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Write(%q) err = %v", p, err)
		}
	}
	// Symlink escaping the root is refused.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(v.Root(), "link")); err == nil {
		if _, err := v.Write(ctx, "link/evil.md", "x", ""); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("symlink escape err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "evil.md")); err == nil {
			t.Error("file written outside the vault")
		}
	}
	// Extension-less paths become notes.
	n, err := v.Write(ctx, "topics/Coffee", "# Coffee\n", "")
	if err != nil || n.Ref.Path != "topics/Coffee.md" {
		t.Fatalf("write = %v %v", n, err)
	}
}

func TestWriteReadLinks(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	n, err := v.Write(ctx, "people/João Silva.md", "---\ntype: person\nworks_at: \"[[ACME]]\"\n---\n# João Silva\nLikes [[Coffee]] #friend\n", "add joão")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ref.Id == "" || len(n.Ref.Id) != 26 {
		t.Fatalf("id not generated: %q", n.Ref.Id)
	}
	raw, _, _ := v.ReadRaw(ctx, "people/João Silva.md")
	if !strings.HasPrefix(string(raw), "---\nid: "+n.Ref.Id+"\ntype: person\n") {
		t.Fatalf("id not inserted in frontmatter:\n%s", raw)
	}
	if n.Type != "person" || n.Ref.Title != "João Silva" || n.Tags[0] != "friend" {
		t.Fatalf("note = %v", n)
	}
	if n.Frontmatter.AsMap()["works_at"] != "[[ACME]]" {
		t.Fatalf("frontmatter = %v", n.Frontmatter.AsMap())
	}
	// Links: both dangling for now.
	if len(n.Links) != 2 || n.Links[0].Id != "" || n.Links[0].Title != "ACME" {
		t.Fatalf("links = %v", n.Links)
	}
	if !strings.HasPrefix(n.Commit, HashPrefix) {
		t.Fatalf("commit = %q", n.Commit)
	}
	// Rewrite keeps the id.
	n2, _ := v.Write(ctx, "people/João Silva.md", string(raw)+"more\n", "")
	if n2.Ref.Id != n.Ref.Id {
		t.Fatal("id changed on rewrite")
	}
	acme, _ := v.Write(ctx, "companies/ACME.md", "# ACME\n", "")
	got, err := v.Read(ctx, acme.Ref.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Backlinks) != 1 || got.Backlinks[0].Id != n.Ref.Id {
		t.Fatalf("backlinks = %v", got.Backlinks)
	}
	// Read by id, path and path without extension.
	for _, k := range []string{n.Ref.Id, "people/João Silva.md", "people/João Silva"} {
		if r, err := v.Read(ctx, k); err != nil || r.Ref.Id != n.Ref.Id || r.Links[0].Path != "companies/ACME.md" {
			t.Errorf("Read(%q) = %v %v", k, r, err)
		}
	}
	if _, err := v.Read(ctx, "nope.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	// Search sees written notes immediately.
	hits, _ := v.Search(ctx, "coffee", nil, 5)
	if len(hits) != 1 || hits[0].Note.Path != "people/João Silva.md" {
		t.Fatalf("hits = %v", hits)
	}
	g, err := v.Graph(ctx, &nexusv1.GraphRequest{CenterId: n.Ref.Id, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	// João, ACME and the dangling Coffee.
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("graph = %v", g)
	}
	foundDangling := false
	for _, e := range g.Edges {
		if e.TargetId == "unresolved:coffee" && e.Relation == "links_to" && e.SourceId == n.Ref.Id {
			foundDangling = true
		}
	}
	if !foundDangling {
		t.Fatalf("edges = %v", g.Edges)
	}
}

func TestOptimisticConcurrencyHash(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	n, _ := v.Write(ctx, "a.md", "one", "")
	if _, err := v.WriteNote(ctx, "a.md", []byte("two"), "", n.Commit); err != nil {
		t.Fatalf("matching base rejected: %v", err)
	}
	if _, err := v.WriteNote(ctx, "a.md", []byte("three"), "", n.Commit); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base accepted: %v", err)
	}
	if _, err := v.WriteNote(ctx, "a.md", []byte("x"), "", "deadbeef"); !errors.Is(err, ErrConflict) {
		t.Fatalf("git-style base without git: %v", err)
	}
	if _, err := v.WriteNote(ctx, "new.md", []byte("x"), "", HashPrefix+"abc"); !errors.Is(err, ErrConflict) {
		t.Fatalf("base for missing file: %v", err)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "t@example.com"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitCommits(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	if !v.GitEnabled() {
		t.Fatal("git not detected")
	}
	// An unrelated staged file must not be committed by Nexus.
	writeFile(t, root, "user.md", "user's own work")
	gitOut(t, root, "add", "user.md")

	n, err := v.Write(WithRunID(ctx, "run_01"), "people/Ana.md", "# Ana\n", "add Ana")
	if err != nil {
		t.Fatal(err)
	}
	log := gitOut(t, root, "log", "-1", "--format=%an <%ae>%n%B")
	if !strings.HasPrefix(log, "Nexus <nexus@localhost>\nadd Ana") || !strings.Contains(log, "Nexus-Run: run_01") {
		t.Fatalf("commit = %q", log)
	}
	if st := gitOut(t, root, "status", "--porcelain"); st != "A  user.md" {
		t.Fatalf("status = %q", st)
	}
	head := gitOut(t, root, "rev-parse", "HEAD")
	if n.Commit != head {
		t.Fatalf("note commit %q, head %q", n.Commit, head)
	}
	// base_commit = git commit works; then conflicts after another write.
	n2, err := v.WriteNote(ctx, "people/Ana.md", []byte("# Ana\nv2\n"), "", n.Commit)
	if err != nil {
		t.Fatalf("matching git base rejected: %v", err)
	}
	if _, err := v.WriteNote(ctx, "people/Ana.md", []byte("# Ana\nv3\n"), "", n.Commit); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale git base accepted: %v", err)
	}
	// A commit that only touched other files keeps the base valid.
	v.Write(ctx, "other.md", "x", "")
	if _, err := v.WriteNote(ctx, "people/Ana.md", []byte("# Ana\nv3\n"), "", n2.Commit); err != nil {
		t.Fatalf("base invalidated by unrelated commit: %v", err)
	}
	if _, err := v.WriteNote(ctx, "people/Ana.md", []byte("x"), "", "0000000000000000000000000000000000000000"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown commit: %v", err)
	}
}

func TestGitBatching(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true, CommitDelay: time.Hour})
	v.Write(WithRunID(ctx, "run_a"), "a.md", "a", "write a")
	v.Write(WithRunID(ctx, "run_b"), "b.md", "b", "write b")
	if err := exec.Command("git", "-C", root, "rev-parse", "-q", "--verify", "HEAD").Run(); err == nil {
		t.Fatal("committed before flush")
	}
	n, _ := v.Read(ctx, "a.md")
	if !strings.HasPrefix(n.Commit, HashPrefix) {
		t.Fatalf("pending note should report a content hash, got %q", n.Commit)
	}
	v.Flush()
	if c := gitOut(t, root, "rev-list", "--count", "HEAD"); c != "1" {
		t.Fatalf("commits = %s", c)
	}
	msg := gitOut(t, root, "log", "-1", "--format=%B")
	for _, s := range []string{"vault: 2 changes", "- write a", "- write b", "Nexus-Run: run_a", "Nexus-Run: run_b"} {
		if !strings.Contains(msg, s) {
			t.Errorf("message missing %q:\n%s", s, msg)
		}
	}
}

func TestGitFailureDoesNotFailWrite(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	// Break the repository after detection.
	os.RemoveAll(filepath.Join(root, ".git"))
	if _, err := v.Write(ctx, "a.md", "a", ""); err != nil {
		t.Fatalf("write failed because of git: %v", err)
	}
}

func TestNoGitRepo(t *testing.T) {
	v := newVault(t, Options{Git: true})
	if v.GitEnabled() {
		t.Fatal("git enabled outside a repo")
	}
}

func TestCapture(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 21, 30, 5, 0, time.UTC)
	v := newVault(t, Options{Now: func() time.Time { return now }})
	ref, err := v.Capture(ctx, &nexusv1.CaptureRequest{
		Text:   "## Café com o João\nfalar sobre o projeto",
		Url:    "https://example.com/x",
		Source: "ios-share",
		Attachments: []*nexusv1.Attachment{
			{Name: "../photo 1.jpg", MimeType: "image/jpeg", Data: []byte{0xff, 0xd8}},
			{MimeType: "application/pdf", Data: []byte("%PDF")},
			{Name: "remote.png", Url: "https://hub/blob/1"},
		},
		IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != "inbox/20261011-213005-cafe-com-o-joao.md" || ref.Title != "Café com o João" {
		t.Fatalf("ref = %v", ref)
	}
	raw, _, _ := v.ReadRaw(ctx, ref.Path)
	n := note.Parse(ref.Path, raw)
	if n.Type != "capture" || n.Frontmatter["source"] != "ios-share" || n.Frontmatter["created"] != "2026-10-11T21:30:05Z" || n.Frontmatter["url"] != "https://example.com/x" {
		t.Fatalf("frontmatter = %v", n.Frontmatter)
	}
	for _, s := range []string{"![[_attachments/20261011-213005-photo 1.jpg]]", "![[_attachments/20261011-213005-attachment-2.pdf]]", "- [remote.png](https://hub/blob/1)", "https://example.com/x"} {
		if !strings.Contains(n.Body, s) {
			t.Errorf("body missing %q:\n%s", s, n.Body)
		}
	}
	if b, err := os.ReadFile(filepath.Join(v.Root(), "_attachments", "20261011-213005-photo 1.jpg")); err != nil || len(b) != 2 {
		t.Fatalf("attachment: %v", err)
	}
	// Idempotent.
	ref2, err := v.Capture(ctx, &nexusv1.CaptureRequest{Text: "different", IdempotencyKey: "k1"})
	if err != nil || ref2.Path != ref.Path {
		t.Fatalf("idempotency: %v %v", ref2, err)
	}
	// Same second, same title → unique name.
	ref3, _ := v.Capture(ctx, &nexusv1.CaptureRequest{Text: "Café com o João"})
	if ref3.Path != "inbox/20261011-213005-cafe-com-o-joao-2.md" {
		t.Fatalf("ref3 = %v", ref3.Path)
	}
	if _, err := v.Capture(ctx, &nexusv1.CaptureRequest{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty capture: %v", err)
	}
}

func TestDailyAppendAndLink(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 9, 5, 0, 0, time.Local)
	v := newVault(t, Options{Now: func() time.Time { return now }})
	if _, err := v.DailyAppend(ctx, "first", time.Time{}); err != nil {
		t.Fatal(err)
	}
	n, err := v.DailyAppend(ctx, "line one\nline two", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if n.Ref.Path != "daily/2026-10-11.md" || n.Type != "daily" {
		t.Fatalf("daily = %v", n.Ref)
	}
	if !strings.Contains(n.Body, "# 2026-10-11\n\n- 09:05 first\n\n### 09:05\n\nline one\nline two\n") {
		t.Fatalf("body = %q", n.Body)
	}

	v.Write(ctx, "people/Ana.md", "# Ana\n", "")
	v.Write(ctx, "companies/ACME.md", "---\nid: acme\n---\n# ACME\n", "")
	n, err = v.Link(ctx, "people/Ana.md", "acme", "works at")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Body, "works at:: [[ACME]]\n") {
		t.Fatalf("body = %q", n.Body)
	}
	out, _ := v.Index().Outlinks(ctx, "people/Ana.md")
	if len(out) != 1 || out[0].Relation != "works_at" || out[0].Dst != "companies/ACME.md" {
		t.Fatalf("edges = %+v", out)
	}
	// Idempotent and plain links.
	v.Link(ctx, "people/Ana.md", "acme", "works at")
	n, _ = v.Link(ctx, "people/Ana.md", "Someone New", "")
	if strings.Count(n.Body, "works at::") != 1 || !strings.HasSuffix(n.Body, "[[Someone New]]\n") {
		t.Fatalf("body = %q", n.Body)
	}
	if _, err := v.Link(ctx, "missing.md", "acme", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link from missing: %v", err)
	}
}

func TestFacts(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	v.Write(ctx, "people/João Silva.md", "---\nid: joao\n---\n# João Silva\n", "")
	f, err := v.ProposeFact(ctx, FactInput{Subject: "joao", Predicate: "works_at", Object: "[[ACME]]", Confidence: 0.7, Source: "agent:librarian"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != nexusv1.FactStatus_FACT_STATUS_PROPOSED || f.Subject.GetId() != "joao" || f.Object != "[[ACME]]" || f.Confidence < 0.69 {
		t.Fatalf("fact = %v", f)
	}
	if !strings.HasPrefix(f.Source.Path, "facts/joao-silva-works-at-acme") {
		t.Fatalf("path = %q", f.Source.Path)
	}
	v.ProposeFact(ctx, FactInput{Subject: "Maria", Predicate: "lives_in", Object: "Lisbon", Confidence: 0.9})
	list, next, err := v.ListFacts(ctx, nexusv1.FactStatus_FACT_STATUS_PROPOSED, &nexusv1.PageRequest{PageSize: 1})
	if err != nil || len(list) != 1 || next != "1" {
		t.Fatalf("list = %v %q %v", list, next, err)
	}
	list, next, _ = v.ListFacts(ctx, nexusv1.FactStatus_FACT_STATUS_PROPOSED, &nexusv1.PageRequest{PageSize: 1, PageToken: next})
	if len(list) != 1 || next != "" {
		t.Fatalf("page 2 = %v %q", list, next)
	}
	r, err := v.ReviewFact(ctx, f.Id, nexusv1.FactStatus_FACT_STATUS_CONFIRMED, "[[ACME Corp]]")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != nexusv1.FactStatus_FACT_STATUS_CONFIRMED || r.Object != "[[ACME Corp]]" || r.ValidFrom == nil {
		t.Fatalf("reviewed = %v", r)
	}
	list, _, _ = v.ListFacts(ctx, nexusv1.FactStatus_FACT_STATUS_PROPOSED, nil)
	if len(list) != 1 {
		t.Fatalf("proposed after review = %d", len(list))
	}
	if _, err := v.ProposeFact(ctx, FactInput{Subject: "x", Confidence: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid fact: %v", err)
	}
	if _, err := v.ReviewFact(ctx, "people/João Silva.md", nexusv1.FactStatus_FACT_STATUS_REJECTED, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("review of non-fact: %v", err)
	}
}

func TestEpisodeWriter(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	now := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	task := &nexusv1.Task{
		Id: "tsk_1", Title: "Buy the usual", Prompt: "buy the usual groceries", AgentId: "grocery-continente",
		State: nexusv1.TaskState_TASK_STATE_SUCCEEDED, CurrentRunId: "run_9", Source: "ios-share",
		Cost: &nexusv1.Cost{Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 42}},
	}
	w := EpisodeWriter{Vault: v, Now: func() time.Time { return now }}
	ref, err := w.WriteEpisode(ctx, task, "Ordered 12 items, total 54.20 EUR.")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != "episodes/2026/10/2026-10-11-buy-the-usual.md" || ref.Title != "Buy the usual" {
		t.Fatalf("ref = %v", ref)
	}
	raw, _, _ := v.ReadRaw(ctx, ref.Path)
	n := note.Parse(ref.Path, raw)
	fm := n.Frontmatter
	if n.Type != "episode" || fm["task_id"] != "tsk_1" || fm["agent"] != "[[grocery-continente]]" || fm["outcome"] != "succeeded" || fm["cost"] != "0.42 EUR" || fm["when"] != "2026-10-11T21:00:00Z" {
		t.Fatalf("frontmatter = %v", fm)
	}
	if !strings.Contains(n.Body, "## Request\nbuy the usual groceries\n\n## What happened\nOrdered 12 items") {
		t.Fatalf("body = %q", n.Body)
	}
	if msg := gitOut(t, root, "log", "-1", "--format=%B"); !strings.Contains(msg, "episode: Buy the usual") || !strings.Contains(msg, "Nexus-Run: run_9") {
		t.Fatalf("commit = %q", msg)
	}
	ref2, _ := w.WriteEpisode(ctx, task, "again")
	if ref2.Path != "episodes/2026/10/2026-10-11-buy-the-usual-2.md" {
		t.Fatalf("ref2 = %v", ref2.Path)
	}
}

func TestSlugifyAndSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Café com o João!": "cafe-com-o-joao", "  --Hello   World-- ": "hello-world", "日本": "note", "Ação 2026/10": "acao-2026-10",
	} {
		if got := Slugify(in, 50); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Slugify(strings.Repeat("a", 100), 10); got != "aaaaaaaaaa" {
		t.Errorf("max = %q", got)
	}
	for in, want := range map[string]string{"../../etc/passwd": "passwd", "a:b?.png": "a_b_.png", ".hidden": "hidden", "..": ""} {
		if got := SafeFileName(in); got != want {
			t.Errorf("SafeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWatcherPicksUpExternalEdits(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	changed := make(chan []string, 10)
	v.OnChange(func(p []string) { changed <- p })
	stop, err := v.Watch(ctx, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	writeFile(t, v.Root(), "people/Obsidian edit.md", "# From Obsidian\nzanzibar\n")
	deadline := time.After(5 * time.Second)
	for {
		hits, _ := v.Search(ctx, "zanzibar", nil, 5)
		if len(hits) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("external edit not indexed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// New directories are watched too.
	writeFile(t, v.Root(), "new/dir/note.md", "quokka")
	for {
		hits, _ := v.Search(ctx, "quokka", nil, 5)
		if len(hits) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("note in new directory not indexed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	os.Remove(filepath.Join(v.Root(), "people", "Obsidian edit.md"))
	for {
		hits, _ := v.Search(ctx, "zanzibar", nil, 5)
		if len(hits) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("deleted note still indexed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(changed) == 0 {
		t.Fatal("listeners not notified")
	}
}

func TestAgentCannotWriteConfiguration(t *testing.T) {
	for _, p := range []string{"agents/x.md", "/shops/evil.md", "policies/default.md", "Workflows/w.md", "agents/../agents/y.md"} {
		if err := checkAgentWritable(p); err == nil {
			t.Fatalf("%s: agent write allowed", p)
		}
	}
	for _, p := range []string{"research/x.md", "episodes/2026/a.md", "inbox/agents-note.md"} {
		if err := checkAgentWritable(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}
