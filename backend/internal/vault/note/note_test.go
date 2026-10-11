package note

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func linkTargets(ls []Link) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Relation+":"+l.Target)
	}
	return out
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name, src, fm, body string
		line                int
		ok                  bool
	}{
		{"none", "# Hi\n", "", "# Hi\n", 1, false},
		{"basic", "---\na: 1\n---\nbody\n", "a: 1\n", "body\n", 4, true},
		{"crlf", "---\r\na: 1\r\n---\r\nbody", "a: 1\n", "body", 4, true},
		{"bom", "\uFEFF---\na: 1\n---\nx", "a: 1\n", "x", 4, true},
		{"dots", "---\na: 1\n...\nx", "a: 1\n", "x", 4, true},
		{"empty", "---\n---\nx", "", "x", 3, true},
		{"unterminated", "---\na: 1\nbody", "", "---\na: 1\nbody", 1, false},
		{"hr later", "text\n---\nmore", "", "text\n---\nmore", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body, line, ok := SplitFrontmatter([]byte(tt.src))
			if fm != tt.fm || body != tt.body || ok != tt.ok || line != tt.line {
				t.Fatalf("got (%q,%q,%d,%v)", fm, body, line, ok)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	tests := []struct{ name, path, src, want string }{
		{"frontmatter", "a/x.md", "---\ntitle: FM Title\n---\n# H1\n", "FM Title"},
		{"h1", "a/x.md", "intro\n## H2\n# The **H1**\n", "The H1"},
		{"h1 in code ignored", "a/x.md", "```\n# not\n```\n", "x"},
		{"filename", "people/João Silva.md", "hello", "João Silva"},
		{"numeric title", "x.md", "---\ntitle: 2024\n---\n", "2024"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.path, []byte(tt.src)).Title; got != tt.want {
				t.Fatalf("title = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWikilinks(t *testing.T) {
	src := "See [[Note A]], [[Note B|the alias]] and [[Note C#Heading|alias]].\n" +
		"Embed: ![[diagram.png]] and ![[Note D#^block]]\n" +
		"Self: [[#Local heading]] (skipped)\n" +
		"Table | [[Note E\\|alias]] |\n" +
		"Inline `[[Not a link]]` code.\n" +
		"```\n[[Also not]]\n```\n" +
		"~~~md\n[[Nope]]\n~~~\n" +
		"<!-- [[Commented]] -->\n" +
		"Md link [x](other%20note.md#h) and [web](https://example.com/a.md) and ![img](_attachments/a.png)\n"
	n := Parse("x.md", []byte(src))
	want := []Link{
		{Target: "Note A", Relation: RelLinksTo, Line: 1},
		{Target: "Note B", Alias: "the alias", Relation: RelLinksTo, Line: 1},
		{Target: "Note C", Heading: "Heading", Alias: "alias", Relation: RelLinksTo, Line: 1},
		{Target: "diagram.png", Embed: true, Relation: RelLinksTo, Line: 2},
		{Target: "Note D", Heading: "^block", Embed: true, Relation: RelLinksTo, Line: 2},
		{Target: "Note E", Alias: "alias", Relation: RelLinksTo, Line: 4},
		{Target: "other note.md", Heading: "h", Alias: "x", Relation: RelLinksTo, Line: 13},
		{Target: "_attachments/a.png", Alias: "img", Embed: true, Relation: RelLinksTo, Line: 13},
	}
	if !reflect.DeepEqual(n.Links, want) {
		t.Fatalf("links:\n got %+v\nwant %+v", n.Links, want)
	}
}

func TestTags(t *testing.T) {
	src := "---\ntags: [work, \"#home\"]\n---\n" +
		"# Heading is not a tag\n" +
		"Text #project/alpha and #Work (dup) #123 (number) #a-b_c.\n" +
		"URL https://example.com/page#anchor and www.x.com/#frag\n" +
		"Link [[Note#heading]] and [x](#local)\n" +
		"`#code` and\n```\n#fenced\n```\n" +
		"#start-of-line,#after-comma\n"
	n := Parse("x.md", []byte(src))
	want := []string{"work", "home", "project/alpha", "a-b_c", "start-of-line", "after-comma"}
	if !reflect.DeepEqual(n.Tags, want) {
		t.Fatalf("tags = %v, want %v", n.Tags, want)
	}
}

func TestTagsStringForm(t *testing.T) {
	n := Parse("x.md", []byte("---\ntags: a, b c\n---\n"))
	if !reflect.DeepEqual(n.Tags, []string{"a", "b", "c"}) {
		t.Fatalf("tags = %v", n.Tags)
	}
}

func TestInlineFieldsAndRelations(t *testing.T) {
	src := "---\ntype: person\nworks_at: \"[[ACME]]\"\nknows: [\"[[Maria]]\", \"[[Rui|R]]\"]\n" +
		"memory:\n  reads: [\"[[Shopping preferences]]\", \"products/\"]\n" +
		"title: \"[[Not a relation]]\"\n---\n" +
		"Prefers:: [[Delta coffee]]\n" +
		"- Store:: [[Continente]]\n" +
		"- Budget per order:: \n" +
		"**Lives in**:: [[Lisbon]] and [[Portugal]]\n" +
		"Inline [met:: [[Ana]]] here and (uses:: Obsidian), plus [[Plain]].\n" +
		"No field: std::vector or https://x.y\n"
	n := Parse("people/x.md", []byte(src))
	gotLinks := linkTargets(n.Links)
	want := []string{
		"knows:Maria", "knows:Rui",
		"memory.reads:Shopping preferences",
		"works_at:ACME",
		"prefers:Delta coffee",
		"store:Continente",
		"lives_in:Lisbon", "lives_in:Portugal",
		"met:Ana", "links_to:Plain",
	}
	if !reflect.DeepEqual(gotLinks, want) {
		t.Fatalf("links:\n got %v\nwant %v", gotLinks, want)
	}
	wantFields := map[string][]string{
		"prefers":          {"[[Delta coffee]]"},
		"store":            {"[[Continente]]"},
		"budget_per_order": {""},
		"lives_in":         {"[[Lisbon]] and [[Portugal]]"},
		"met":              {"[[Ana]]"},
		"uses":             {"Obsidian"},
	}
	if !reflect.DeepEqual(n.Fields, wantFields) {
		t.Fatalf("fields = %#v", n.Fields)
	}
}

func TestFrontmatterErrorsAndTypes(t *testing.T) {
	n := Parse("x.md", []byte("---\nwhen: {{now}}\n---\n# T\n[[A]]"))
	if n.FrontmatterErr == nil {
		t.Fatal("expected frontmatter error")
	}
	if n.Title != "T" || len(n.Links) != 1 {
		t.Fatalf("body should still parse: %+v", n)
	}
	n = Parse("x.md", []byte("---\nid: 0123\ncreated: 2026-10-11T21:00:00Z\nday: 2026-10-11\nn: 3\naliases: João\n---\n"))
	if n.ID != "0123" {
		t.Fatalf("id = %q", n.ID)
	}
	if n.Frontmatter["created"] != "2026-10-11T21:00:00Z" || n.Frontmatter["day"] != "2026-10-11" {
		t.Fatalf("timestamps not normalised: %#v", n.Frontmatter)
	}
	if !reflect.DeepEqual(n.Aliases, []string{"João"}) {
		t.Fatalf("aliases = %v", n.Aliases)
	}
}

func TestNormalizeKey(t *testing.T) {
	for in, want := range map[string]string{
		"Preferred brands": "preferred_brands", "works-at": "works_at", " Lives  in ": "lives_in",
		"memory.reads": "memory.reads", "Ção": "ção", "a/b": "a_b",
	} {
		if got := NormalizeKey(in); got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureID(t *testing.T) {
	gen := func() string { return "01NEW" }
	tests := []struct{ name, src, want, id string }{
		{"no fm", "# T\n", "---\nid: 01NEW\n---\n# T\n", "01NEW"},
		{"fm", "---\ntype: x\n---\nbody", "---\nid: 01NEW\ntype: x\n---\nbody", "01NEW"},
		{"has id", "---\nid: abc\n---\n", "---\nid: abc\n---\n", "abc"},
		{"crlf", "---\r\ntype: x\r\n---\r\n", "---\r\nid: 01NEW\r\ntype: x\r\n---\r\n", "01NEW"},
		{"bad yaml untouched", "---\na: {{x}}\n---\n", "---\na: {{x}}\n---\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, id, _ := EnsureID([]byte(tt.src), gen)
			if string(out) != tt.want || id != tt.id {
				t.Fatalf("got %q id=%q", out, id)
			}
		})
	}
}

func TestSetFields(t *testing.T) {
	src := "---\n# comment\ntype: fact\nstatus: proposed # inline\nobject: x\n---\nBody [[A]]\n"
	out, err := SetFields([]byte(src), map[string]any{"status": "confirmed", "object": nil, "valid_to": "2026"})
	if err != nil {
		t.Fatal(err)
	}
	n := Parse("x.md", out)
	if n.Frontmatter["status"] != "confirmed" || n.Frontmatter["valid_to"] != "2026" || n.Frontmatter["object"] != nil {
		t.Fatalf("fm = %#v\n%s", n.Frontmatter, out)
	}
	if !strings.HasSuffix(string(out), "---\nBody [[A]]\n") || !strings.Contains(string(out), "# comment") {
		t.Fatalf("formatting lost:\n%s", out)
	}
	r, err := Render([]string{"type", "title"}, map[string]any{"type": "x", "title": "Hello: world"}, "body\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(r) != "---\ntype: x\ntitle: 'Hello: world'\n---\nbody\n" {
		t.Fatalf("render = %q", r)
	}
}

// TestVaultTemplate parses every note of the starter vault.
func TestVaultTemplate(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "vault-template")
	if _, err := os.Stat(root); err != nil {
		t.Skip("vault-template not found")
	}
	count := 0
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || filepath.Ext(p) != ".md" {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		n := Parse(filepath.ToSlash(rel), b)
		count++
		if rel != filepath.Join("templates", "episode.md") && n.FrontmatterErr != nil {
			t.Errorf("%s: %v", rel, n.FrontmatterErr)
		}
		if n.Title == "" {
			t.Errorf("%s: empty title", rel)
		}
		switch filepath.ToSlash(rel) {
		case "agents/grocery-continente.md":
			if n.ID != "grocery-continente" || n.Type != "agent" || n.Title != "Grocery (Continente)" {
				t.Errorf("grocery: %+v", n)
			}
			got := linkTargets(n.Links)
			want := []string{"memory.reads:Shopping preferences", "links_to:Shopping preferences"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("grocery links = %v", got)
			}
		case "templates/Shopping preferences.md":
			if got := linkTargets(n.Links); !reflect.DeepEqual(got, []string{"store:Continente"}) {
				t.Errorf("shopping links = %v", got)
			}
			if len(n.Fields) != 5 {
				t.Errorf("shopping fields = %v", n.Fields)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 10 {
		t.Fatalf("parsed only %d notes", count)
	}
}
