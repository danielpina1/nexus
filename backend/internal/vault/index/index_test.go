package index

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T, files map[string]string) (*Index, string) {
	t.Helper()
	root := t.TempDir()
	for rel, c := range files {
		write(t, root, rel, c)
	}
	ix, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	if _, err := ix.Sync(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return ix, root
}

var corpus = map[string]string{
	"people/João Silva.md":     "---\nid: 01PERSON\ntype: person\naliases: [João, JS]\nworks_at: \"[[ACME]]\"\ntags: [work]\n---\n# João Silva\nMet at the coffee conference.\nPrefers:: [[Delta coffee]]\n",
	"companies/ACME.md":        "---\ntype: company\n---\n# ACME\nA company. See [[people/João Silva|João]].\n",
	"products/Delta coffee.md": "---\ntype: product\n---\nGround coffee from Portugal.\n",
	"topics/Coffee.md":         "---\ntype: topic\n---\n# Coffee\nNotes about coffee brewing. [[Delta coffee]] [[Missing note]] ![[photo.png]]\n",
	"daily/2026-10-11.md":      "Talked to [[JS]] about [[01PERSON]] and #meeting stuff.\n",
	"a/Dup.md":                 "dup in a",
	"b/Dup.md":                 "dup in b, see [[Dup]]",
	"Dup.md":                   "root dup",
	".obsidian/workspace.md":   "[[ignored]]",
	"_attachments/x.txt":       "not a note",
}

func TestSyncIncremental(t *testing.T) {
	ctx := context.Background()
	ix, root := setup(t, corpus)
	all, _ := ix.ListByType(ctx, "")
	if len(all) != 8 {
		t.Fatalf("indexed %d notes", len(all))
	}
	st, err := ix.Sync(ctx, root)
	if err != nil || st.Updated != 0 || st.Removed != 0 || st.Scanned != 8 {
		t.Fatalf("second sync: %+v %v", st, err)
	}
	write(t, root, "topics/Coffee.md", "# Coffee v2\n")
	os.Remove(filepath.Join(root, "Dup.md"))
	st, _ = ix.Sync(ctx, root)
	if st.Updated != 1 || st.Removed != 1 {
		t.Fatalf("third sync: %+v", st)
	}
	if e, _ := ix.Get(ctx, "topics/Coffee.md"); e.Title != "Coffee v2" {
		t.Fatalf("title = %q", e.Title)
	}
	st, _ = ix.Rebuild(ctx, root)
	if st.Updated != 7 {
		t.Fatalf("rebuild: %+v", st)
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	tests := []struct{ target, from, want string }{
		{"ACME", "x.md", "companies/ACME.md"},
		{"acme", "x.md", "companies/ACME.md"},
		{"ACME.md", "x.md", "companies/ACME.md"},
		{"people/João Silva", "x.md", "people/João Silva.md"},
		{"João Silva", "x.md", "people/João Silva.md"},
		{"joão", "x.md", "people/João Silva.md"},     // alias
		{"01PERSON", "x.md", "people/João Silva.md"}, // id
		{"Dup", "x.md", "Dup.md"},                    // shortest path
		{"Dup", "b/Other.md", "b/Dup.md"},            // same folder wins
		{"a/Dup", "b/Other.md", "a/Dup.md"},          // explicit path
		{"../a/Dup", "b/Other.md", "a/Dup.md"},       // relative
		{"Silva", "x.md", ""},                        // no partial match
		{"Missing note", "x.md", ""},
	}
	for _, tt := range tests {
		e, ok := ix.Resolve(ctx, tt.target, tt.from)
		got := ""
		if ok {
			got = e.Path
		}
		if got != tt.want {
			t.Errorf("Resolve(%q from %q) = %q, want %q", tt.target, tt.from, got, tt.want)
		}
	}
}

func TestLinksAndBacklinks(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	out, err := ix.Outlinks(ctx, "people/João Silva.md")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range out {
		got = append(got, e.Relation+">"+e.Dst)
	}
	want := []string{"works_at>companies/ACME.md", "prefers>products/Delta coffee.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outlinks = %v", got)
	}
	back, _ := ix.Backlinks(ctx, "people/João Silva.md")
	var srcs []string
	for _, e := range back {
		srcs = append(srcs, e.Src)
	}
	// daily links twice (alias + id) → two edges.
	want = []string{"companies/ACME.md", "daily/2026-10-11.md", "daily/2026-10-11.md"}
	if !reflect.DeepEqual(srcs, want) {
		t.Fatalf("backlinks = %v", srcs)
	}
	back, _ = ix.Backlinks(ctx, "products/Delta coffee.md")
	if len(back) != 2 {
		t.Fatalf("delta backlinks = %+v", back)
	}
	// Coffee: attachment embed is not an edge; the missing note is dangling.
	out, _ = ix.Outlinks(ctx, "topics/Coffee.md")
	if len(out) != 2 || out[1].Dst != "" || out[1].Target != "Missing note" {
		t.Fatalf("coffee outlinks = %+v", out)
	}
}

func TestDanglingResolvesLater(t *testing.T) {
	ctx := context.Background()
	ix, root := setup(t, corpus)
	write(t, root, "topics/Missing note.md", "now exists")
	if _, err := ix.Sync(ctx, root); err != nil {
		t.Fatal(err)
	}
	out, _ := ix.Outlinks(ctx, "topics/Coffee.md")
	if out[1].Dst != "topics/Missing note.md" {
		t.Fatalf("link not resolved after creation: %+v", out[1])
	}
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	hits, err := ix.Search(ctx, Query{Text: "coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 3 {
		t.Fatalf("hits = %d", len(hits))
	}
	if hits[0].Entry.Path != "topics/Coffee.md" {
		t.Fatalf("top hit = %s (exact title should win)", hits[0].Entry.Path)
	}
	if !strings.Contains(hits[0].Snippet, "**coffee**") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	// Type filter.
	hits, _ = ix.Search(ctx, Query{Text: "coffee", Types: []string{"product"}})
	if len(hits) != 1 || hits[0].Entry.Path != "products/Delta coffee.md" {
		t.Fatalf("typed hits = %+v", hits)
	}
	// Diacritics and prefix matching; aliases.
	hits, _ = ix.Search(ctx, Query{Text: "joao"})
	if len(hits) == 0 || hits[0].Entry.Path != "people/João Silva.md" {
		t.Fatalf("joao hits = %+v", hits)
	}
	hits, _ = ix.Search(ctx, Query{Text: "conf"})
	if len(hits) != 1 {
		t.Fatalf("prefix hits = %+v", hits)
	}
	// AND then OR fallback.
	hits, _ = ix.Search(ctx, Query{Text: "brewing xyzzy"})
	if len(hits) != 1 || hits[0].Entry.Path != "topics/Coffee.md" {
		t.Fatalf("or fallback = %+v", hits)
	}
	// Tags are searchable; FTS syntax is neutralised.
	hits, _ = ix.Search(ctx, Query{Text: `work" OR NEAR(`})
	if len(hits) == 0 {
		t.Fatal("tag search found nothing")
	}
	// Empty query = recent.
	hits, _ = ix.Search(ctx, Query{Limit: 3})
	if len(hits) != 3 {
		t.Fatalf("recent = %d", len(hits))
	}
	// Since filter.
	hits, _ = ix.Search(ctx, Query{Text: "coffee", Since: time.Now().Add(time.Hour)})
	if len(hits) != 0 {
		t.Fatalf("since filter = %d", len(hits))
	}
}

func TestGraph(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	g, err := ix.Graph(ctx, "people/João Silva.md", 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []string
	for _, n := range g.Nodes {
		nodes = append(nodes, n.Path)
	}
	sort.Strings(nodes)
	want := []string{"companies/ACME.md", "daily/2026-10-11.md", "people/João Silva.md", "products/Delta coffee.md"}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("depth1 nodes = %v", nodes)
	}
	// Edges: works_at, prefers, ACME→João, daily→João (deduped).
	if len(g.Edges) != 4 {
		t.Fatalf("edges = %+v", g.Edges)
	}
	g, _ = ix.Graph(ctx, "people/João Silva.md", 2, nil, 0)
	if len(g.Nodes) != 5 { // + topics/Coffee via Delta coffee
		t.Fatalf("depth2 nodes = %d", len(g.Nodes))
	}
	g, _ = ix.Graph(ctx, "people/João Silva.md", 2, []string{"product"}, 0)
	if len(g.Nodes) != 2 {
		t.Fatalf("typed nodes = %+v", g.Nodes)
	}
	g, _ = ix.Graph(ctx, "", 0, nil, 0)
	if len(g.Nodes) != 8 || len(g.Dangling) != 1 {
		t.Fatalf("whole graph: %d nodes, dangling %v", len(g.Nodes), g.Dangling)
	}
	g, _ = ix.Graph(ctx, "", 0, nil, 2)
	if len(g.Nodes) != 2 {
		t.Fatalf("limit: %d", len(g.Nodes))
	}
	if _, err := ix.Graph(ctx, "nope.md", 1, nil, 0); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestLookupAndKV(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	for _, k := range []string{"01PERSON", "people/João Silva.md", "people/João Silva"} {
		if e, ok := ix.Lookup(ctx, k); !ok || e.Key() != "01PERSON" {
			t.Errorf("Lookup(%q) = %v %v", k, e, ok)
		}
	}
	if e, ok := ix.Lookup(ctx, "companies/ACME.md"); !ok || e.Key() != "companies/ACME.md" {
		t.Errorf("key without id = %v", e)
	}
	if err := ix.KVPut(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	ix.KVPut(ctx, "k", "v2")
	if v, ok := ix.KVGet(ctx, "k"); !ok || v != "v2" {
		t.Fatalf("kv = %q", v)
	}
}

type fakeEmb struct{ calls int }

// Embed maps texts onto 2 dims: (mentions coffee, mentions company).
func (f *fakeEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		t = strings.ToLower(t)
		v := []float32{0.01, 0.01}
		if strings.Contains(t, "coffee") || strings.Contains(t, "espresso") {
			v[0] = 1
		}
		if strings.Contains(t, "company") || strings.Contains(t, "firm") {
			v[1] = 1
		}
		out[i] = v
	}
	return out, nil
}

func TestVectors(t *testing.T) {
	ctx := context.Background()
	ix, _ := setup(t, corpus)
	emb := &fakeEmb{}
	v := NewVectors(ix, emb)
	n, err := v.Refresh(ctx)
	if err != nil || n != 8 {
		t.Fatalf("refresh = %d %v", n, err)
	}
	if n, _ = v.Refresh(ctx); n != 0 {
		t.Fatalf("second refresh embedded %d", n)
	}
	ix.SetVectorSearcher(v)
	// "firm" never appears in the text: only the vector side can find ACME.
	hits, err := ix.Search(ctx, Query{Text: "firm"})
	if err != nil || len(hits) == 0 || hits[0].Entry.Path != "companies/ACME.md" {
		t.Fatalf("semantic hits = %+v %v", hits, err)
	}
}

func TestChunk(t *testing.T) {
	text := strings.Repeat("word ", 100) + "\n\n" + "short\n\nalso short"
	cs := Chunk(text, 120)
	for _, c := range cs {
		if len(c) > 120 {
			t.Fatalf("chunk too long: %d", len(c))
		}
	}
	if !strings.HasSuffix(cs[len(cs)-1], "short\n\nalso short") {
		t.Fatalf("chunks = %q", cs)
	}
}
