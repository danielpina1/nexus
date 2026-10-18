package vault

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestRenameUpdatesLinks(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	v.Write(ctx, "companies/ACME.md", "---\naliases: [Acme Corp]\n---\n# ACME\nSee [rel](../people/x.md)\n", "")
	v.Write(ctx, "people/x.md", "# X\n", "")
	v.Write(ctx, "people/João.md", "---\nworks_at: \"[[ACME]]\"\n---\nAt [[ACME#History|the firm]], ![[ACME]], [[Acme Corp]], [[companies/ACME]].\n"+
		"Table | [[ACME\\|alias]] |\n```\n[[ACME]] in code\n```\n[md](../companies/ACME.md#h)\n", "")
	res, err := v.Rename(ctx, "companies/ACME.md", "orgs/ACME Inc.md", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Note.Ref.Path != "orgs/ACME Inc.md" || len(res.Updated) != 1 {
		t.Fatalf("rename = %+v", res)
	}
	got := rawOf(t, v, "people/João.md")
	for _, want := range []string{
		`works_at: "[[ACME Inc]]"`, "[[ACME Inc#History|the firm]]", "![[ACME Inc]]", "[[Acme Corp]]",
		"[[orgs/ACME Inc]]", `[[ACME Inc\|alias]]`, "[[ACME]] in code", "[md](../orgs/ACME%20Inc.md#h)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "../companies/ACME.md") {
		t.Errorf("md link not updated:\n%s", got)
	}
	// The moved note's relative link was adjusted (same depth here).
	if moved := rawOf(t, v, "orgs/ACME Inc.md"); !strings.Contains(moved, "](../people/x.md)") {
		t.Errorf("moved note links:\n%s", moved)
	}
	n, _ := v.Read(ctx, "orgs/ACME Inc.md")
	if len(n.Backlinks) != 1 {
		t.Fatalf("backlinks after rename = %v", n.Backlinks)
	}
	if _, err := v.Rename(ctx, "orgs/ACME Inc.md", "people/x.md", false, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rename onto existing err = %v", err)
	}
	// Ambiguous new name → path-style link.
	v.Write(ctx, "a/Topic.md", "# T1\n", "")
	v.Write(ctx, "b/Other.md", "# T2\n", "")
	v.Write(ctx, "c/ref.md", "[[Other]]\n", "")
	if _, err := v.Rename(ctx, "b/Other.md", "b/Topic.md", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := rawOf(t, v, "c/ref.md"); !strings.Contains(got, "[[b/Topic]]") {
		t.Fatalf("ambiguous rename link = %s", got)
	}
}

func TestMergeAndSplit(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	v.Write(ctx, "Coffee.md", "---\ntags: [drinks]\n---\n# Coffee\nGood.\n", "")
	v.Write(ctx, "Espresso.md", "---\naliases: [Shot]\ntags: [italian]\n---\n# Espresso\nStrong [[Coffee]].\n", "")
	v.Write(ctx, "log.md", "Had an [[Espresso]] and a [[Shot]].\n", "")
	res, err := v.Merge(ctx, "Espresso.md", "Coffee.md", "")
	if err != nil {
		t.Fatal(err)
	}
	body := rawOf(t, v, "Coffee.md")
	for _, want := range []string{"## Espresso", "Strong [[Coffee]].", "- Espresso", "- Shot", "- drinks", "- italian"} {
		if !strings.Contains(body, want) {
			t.Errorf("merged missing %q:\n%s", want, body)
		}
	}
	if got := rawOf(t, v, "log.md"); !strings.Contains(got, "[[Coffee]] and a [[Shot]]") {
		t.Fatalf("links after merge = %s", got)
	}
	if _, err := v.Read(ctx, "Espresso.md"); !errors.Is(err, ErrNotFound) {
		t.Fatal("source not trashed")
	}
	if items, _ := v.ListTrash(ctx); len(items) != 1 || items[0].Path != "Espresso.md" {
		t.Fatalf("trash = %+v", items)
	}
	_ = res
	// Split.
	v.Write(ctx, "Guide.md", "# Guide\nIntro.\n\n## Setup\nInstall it.\n### Detail\nMore.\n\n## Usage\nRun it.\n", "")
	v.Write(ctx, "ref.md", "See [[Guide#Usage]] and [[Guide#Intro]] and [[Guide]].\n", "")
	sr, err := v.Split(ctx, "Guide.md", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Created) != 2 || sr.Created[0].Path != "Setup.md" || sr.Created[1].Path != "Usage.md" {
		t.Fatalf("created = %v", sr.Created)
	}
	g := rawOf(t, v, "Guide.md")
	if !strings.Contains(g, "Intro.") || !strings.Contains(g, "![[Setup]]") || !strings.Contains(g, "![[Usage]]") || strings.Contains(g, "Install it") {
		t.Fatalf("guide after split:\n%s", g)
	}
	setup := rawOf(t, v, "Setup.md")
	if !strings.Contains(setup, "# Setup\nInstall it.\n### Detail\nMore.") || !strings.Contains(setup, "split_from: '[[Guide]]'") {
		t.Fatalf("setup:\n%s", setup)
	}
	if r := rawOf(t, v, "ref.md"); !strings.Contains(r, "[[Usage]] and [[Guide#Intro]] and [[Guide]]") {
		t.Fatalf("ref after split = %s", r)
	}
	if _, err := v.Split(ctx, "Setup.md", 2, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("split without headings err = %v", err)
	}
}

func TestTemplatesAndPeriodic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 9, 30, 0, 0, time.Local)
	v := newVault(t, Options{Now: func() time.Time { return now }})
	v.Write(ctx, "templates/meeting.md", "---\ntype: template\ntags: [meeting]\n---\n# {{title}}\nDate: {{date:dddd D MMMM YYYY}} {{time}}\nWith: {{prompt:Who}}\nTopic: {{topic}}\n", "")
	v.Write(ctx, "templates/daily.md", "---\ntype: daily\n---\n# {{date:YYYY-MM-DD}}\n[[{{yesterday}}]] | [[{{tomorrow}}]]\n", "")
	if raw := rawOf(t, v, "templates/meeting.md"); strings.Contains(raw, "id:") {
		t.Fatal("template got an id")
	}
	ts, err := v.ListTemplates(ctx)
	if err != nil || len(ts) != 2 {
		t.Fatalf("templates = %+v %v", ts, err)
	}
	var m Template
	for _, x := range ts {
		if x.Name == "meeting" {
			m = x
		}
	}
	if strings.Join(m.Prompts, ",") != "Who" || !strings.Contains(strings.Join(m.Variables, ","), "topic") {
		t.Fatalf("meeting template = %+v", m)
	}
	_, err = v.NewFromTemplate(ctx, "meeting", "", "Sync", nil, "")
	var mp *MissingPromptsError
	if !errors.As(err, &mp) || mp.Prompts[0] != "Who" {
		t.Fatalf("missing prompt err = %v", err)
	}
	n, err := v.NewFromTemplate(ctx, "meeting", "", "Sync", map[string]string{"Who": "Ana", "topic": "Q4"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ref.Path != "inbox/Sync.md" || n.Type != "" || !strings.Contains(n.Body, "Date: Sunday 18 October 2026 09:30") ||
		!strings.Contains(n.Body, "With: Ana") || !strings.Contains(n.Body, "Topic: Q4") || n.Ref.Id == "" {
		t.Fatalf("note = %v", n)
	}
	if _, err := v.NewFromTemplate(ctx, "meeting", "", "Sync", map[string]string{"Who": "x"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("existing dest err = %v", err)
	}
	// Periodic notes.
	d, rel, created, err := v.PeriodicNote(ctx, "daily", time.Time{}, true)
	if err != nil || !created || rel != "daily/2026-10-18.md" || !strings.Contains(d.Body, "[[2026-10-17]] | [[2026-10-19]]") {
		t.Fatalf("daily = %v %q %v %v", d, rel, created, err)
	}
	if _, _, created, _ := v.PeriodicNote(ctx, "daily", time.Time{}, true); created {
		t.Fatal("daily created twice")
	}
	w, rel, _, err := v.PeriodicNote(ctx, "weekly", now, true)
	if err != nil || rel != "weekly/2026-W42.md" || w.Ref.Title != "2026-W42" {
		t.Fatalf("weekly = %v %q %v", w, rel, err)
	}
	if _, rel, _, _ := v.PeriodicNote(ctx, "monthly", now, false); rel != "monthly/2026-10.md" {
		t.Fatalf("monthly path = %q", rel)
	}
	// DailyAppend uses the daily template for new days.
	v.DailyAppend(ctx, "hello", now.AddDate(0, 0, 1))
	if got := rawOf(t, v, "daily/2026-10-19.md"); !strings.Contains(got, "[[2026-10-18]]") || !strings.Contains(got, "hello") {
		t.Fatalf("daily append with template:\n%s", got)
	}
	if FormatMoment(now, "Do [of] MMM YY, h:mm A") != "18th of Oct 26, 9:30 AM" {
		t.Fatalf("moment = %q", FormatMoment(now, "Do [of] MMM YY, h:mm A"))
	}
}

func TestQueryLanguage(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	v := newVault(t, Options{})
	v.Write(ctx, "companies/ACME.md", "---\ntype: company\n---\n# ACME\n", "")
	v.Write(ctx, "people/Ana.md", "---\ntype: person\ntags: [work/acme]\ncompany: \"[[ACME]]\"\nrating: 5\ncreated: 2026-01-10\n---\n# Ana\nLoves espresso. Works with [[ACME]].\n", "")
	v.Write(ctx, "people/Bo.md", "---\ntype: person\ntags: [family]\nrating: 3\ncreated: 2025-06-01\n---\n# Bo\nLikes tea.\n- [ ] call Bo\n", "")
	v.Write(ctx, "people/Cy.md", "---\ntype: person\ntags: [work]\nstatus: active\n---\n# Cy\n", "")
	cases := []struct {
		q    string
		want string
	}{
		{"type:person tag:work sort:path", "people/Ana.md,people/Cy.md"},
		{"type:person -tag:work", "people/Bo.md"},
		{"links:[[ACME]]", "people/Ana.md"},
		{"company:[[ACME]]", "people/Ana.md"},
		{"rating:>=4", "people/Ana.md"},
		{"rating:<4 sort:title", "people/Bo.md"},
		{"type:person sort:-rating limit:2", "people/Ana.md,people/Bo.md"},
		{"created:<2026-01-01", "people/Bo.md"},
		{"created:2026-01", "people/Ana.md"},
		{"has:status", "people/Cy.md"},
		{"has:open-tasks", "people/Bo.md"},
		{"path:people/ espresso", "people/Ana.md"},
		{"type:company,person sort:path limit:1", "companies/ACME.md"},
		{"from:[[people/Ana]]", "companies/ACME.md"},
		{"orphan:true sort:path", "people/Bo.md,people/Cy.md"},
		{"updated:>-1d type:company", "companies/ACME.md"},
	}
	for _, c := range cases {
		res, err := v.Query(ctx, c.q, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		var got []string
		for _, h := range res.Hits {
			got = append(got, h.Entry.Path)
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("Query(%q) = %v (warnings %v), want %s", c.q, got, res.Warnings, c.want)
		}
	}
	res, _ := v.Query(ctx, "espresso", 0)
	if len(res.Hits) != 1 || !strings.Contains(res.Hits[0].Snippet, "**espresso**") {
		t.Fatalf("snippet = %+v", res.Hits)
	}
	if res, _ := v.Query(ctx, "updated:>banana", 0); len(res.Warnings) == 0 {
		t.Fatal("no warning for a bad date")
	}
	// Search routes filter syntax to the query engine.
	hits, _ := v.Search(ctx, "tag:family", nil, 10)
	if len(hits) != 1 || hits[0].Note.Path != "people/Bo.md" {
		t.Fatalf("search with filters = %v", hits)
	}
	_ = now
}

func TestAnalytics(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	v.Write(ctx, "Coffee.md", "---\naliases: [Java]\ntags: [drink]\n---\n# Coffee\nsee [[Beans]]\n", "")
	v.Write(ctx, "Beans.md", "# Beans\n", "")
	v.Write(ctx, "a.md", "# A\nI drink [[Coffee]] daily.\n", "")
	v.Write(ctx, "b.md", "# B\nMore coffee please. Java too.\n[[Beans]]\n", "")
	v.Write(ctx, "lonely.md", "# Lonely\n", "")
	links, mentions, err := v.Backlinks(ctx, "Coffee.md", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Line numbers count the frontmatter (an id was added on write).
	if len(links) != 1 || links[0].Source.Path != "a.md" || links[0].Line != 5 || links[0].Context != "I drink [[Coffee]] daily." {
		t.Fatalf("backlinks = %+v", links)
	}
	if len(mentions) != 1 || mentions[0].Source.Path != "b.md" || mentions[0].Line != 5 {
		t.Fatalf("mentions = %+v", mentions)
	}
	rel, err := v.Related(ctx, "Coffee.md", 5)
	if err != nil || len(rel) < 2 {
		t.Fatalf("related = %+v %v", rel, err)
	}
	paths := map[string]bool{}
	for _, r := range rel {
		paths[r.Entry.Path] = true
	}
	if !paths["Beans.md"] || !paths["a.md"] || !paths["b.md"] {
		t.Fatalf("related = %+v", rel)
	}
	st, err := v.Stats(ctx, 5)
	if err != nil || st.Notes != 5 || len(st.Orphans) != 1 || st.Orphans[0].Path != "lonely.md" || st.Hubs[0].Entry.Path != "Beans.md" {
		t.Fatalf("stats = %+v %v", st, err)
	}
	// Graph filters.
	g, err := v.Graph(ctx, &nexusv1.GraphRequest{HideOrphans: true, Relations: []string{"links_to"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.Path == "lonely.md" {
			t.Fatal("orphan not hidden")
		}
	}
	g, _ = v.Graph(ctx, &nexusv1.GraphRequest{Tags: []string{"drink"}})
	if len(g.Nodes) != 1 || g.Nodes[0].Path != "Coffee.md" {
		t.Fatalf("tag-filtered graph = %v", g.Nodes)
	}
}

func TestNoteTasks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)
	v := newVault(t, Options{Now: func() time.Time { return now }})
	v.Write(ctx, "work/p.md", "# P\n- [ ] Ship it 📅 2026-10-20 #work\n- [x] Done thing ✅ 2026-10-01\n- [ ] Someday #idea\n", "")
	v.Write(ctx, "home.md", "- [ ] Fix sink 📅 2026-10-19 ⏫\n", "")
	open, err := v.ListTasks(ctx, TaskFilter{})
	if err != nil || len(open) != 3 || open[0].Text != "Fix sink" || open[0].Priority != "high" {
		t.Fatalf("open = %+v %v", open, err)
	}
	due, _ := v.ListTasks(ctx, TaskFilter{DueBefore: "2026-10-19"})
	if len(due) != 1 {
		t.Fatalf("due = %+v", due)
	}
	tagged, _ := v.ListTasks(ctx, TaskFilter{Tags: []string{"work"}, Status: "all"})
	if len(tagged) != 1 || tagged[0].Path != "work/p.md" {
		t.Fatalf("tagged = %+v", tagged)
	}
	done, _ := v.ListTasks(ctx, TaskFilter{Status: "done"})
	if len(done) != 1 {
		t.Fatalf("done = %+v", done)
	}
	line := 0
	for _, o := range open {
		if o.Text == "Ship it #work" {
			line = o.Line
		}
	}
	tk, n, err := v.ToggleTask(ctx, "work/p.md", line, "- [ ] Ship it 📅 2026-10-20 #work", nil)
	if err != nil || !tk.Done || tk.DoneDate != "2026-10-18" || !strings.Contains(n.Body, "- [x] Ship it 📅 2026-10-20 #work ✅ 2026-10-18") {
		t.Fatalf("toggle = %+v %v", tk, err)
	}
	if _, _, err := v.ToggleTask(ctx, "work/p.md", line, "- [ ] Ship it 📅 2026-10-20 #work", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale toggle err = %v", err)
	}
	if _, _, err := v.ToggleTask(ctx, "work/p.md", 1, "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-task toggle err = %v", err)
	}
	if open, _ := v.ListTasks(ctx, TaskFilter{}); len(open) != 2 {
		t.Fatalf("open after toggle = %d", len(open))
	}
}

func TestCheckAndRepair(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := newVault(t, Options{Root: root})
	v.Write(ctx, "a.md", "# A\n[[Missing]]\n![[_attachments/used.png]]\n", "")
	writeFile(t, root, "b.md", "---\nid: DUP\n---\n# B\n")
	writeFile(t, root, "c.md", "---\nid: DUP\n---\n# C\n")
	writeFile(t, root, "noid.md", "# No id\n")
	writeFile(t, root, "bad.md", "---\ntitle: [unclosed\n---\nx\n")
	writeFile(t, root, "_attachments/used.png", "png")
	writeFile(t, root, "_attachments/orphan.png", "png")
	writeFile(t, root, "x/"+tempPrefix+"123", "partial")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "x", tempPrefix+"123"), old, old)
	if _, err := v.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "late.md", "# Late\n") // not indexed yet
	quick, err := v.Check(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	qc := quick.Counts()
	if qc[IssueDuplicateID] != 1 || qc[IssueTempFile] != 1 || qc[IssueIndexMissing] != 1 || qc[IssueMissingID] != 0 {
		t.Fatalf("quick = %v", qc)
	}
	full, _ := v.Check(ctx, true)
	fc := full.Counts()
	for _, k := range []string{IssueBrokenLink, IssueOrphanAttachment, IssueInvalidFM, IssueMissingID} {
		if fc[k] == 0 {
			t.Errorf("full check missing %s: %v", k, fc)
		}
	}
	if !strings.Contains(full.Summary(), "duplicate id") {
		t.Fatalf("summary = %s", full.Summary())
	}
	actions, after, err := v.Repair(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	ac := after.Counts()
	if ac[IssueDuplicateID] != 0 || ac[IssueTempFile] != 0 || ac[IssueIndexMissing] != 0 || ac[IssueMissingID] != 0 {
		t.Fatalf("after repair = %v (actions %v)", ac, actions)
	}
	if ac[IssueInvalidFM] != 1 {
		t.Fatalf("invalid frontmatter should be reported, not touched: %v", ac)
	}
	b, c := rawOf(t, v, "b.md"), rawOf(t, v, "c.md")
	if strings.Contains(b, "\nid: DUP") == strings.Contains(c, "\nid: DUP") || !strings.Contains(b+c, "previous_id: DUP") {
		t.Fatalf("duplicate fix:\n%s\n%s", b, c)
	}
}

func TestBackupVerifyRestoreRetention(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	v.Write(ctx, "a.md", "# A\n", "")
	writeFile(t, root, "_attachments/p.png", "png-bytes")
	dir := t.TempDir()
	res, err := v.Backup(ctx, BackupOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 2 || res.Created[0].Kind != "tar.gz" || res.Created[1].Kind != "bundle" {
		t.Fatalf("created = %+v", res.Created)
	}
	files, problems, err := VerifyBackup(ctx, res.Created[0].Path)
	if err != nil || len(problems) != 0 || files < 2 {
		t.Fatalf("verify = %d %v %v", files, problems, err)
	}
	if _, problems, _ := VerifyBackup(ctx, res.Created[1].Path); len(problems) != 0 {
		t.Fatalf("bundle verify = %v", problems)
	}
	// Corruption is detected.
	b, _ := os.ReadFile(res.Created[0].Path)
	bad := filepath.Join(t.TempDir(), "nexus-vault-20200101-000000.tar.gz")
	os.WriteFile(bad, b[:len(b)/2], 0o600)
	if _, problems, _ := VerifyBackup(ctx, bad); len(problems) == 0 {
		t.Fatal("truncated archive verified")
	}
	// Restore into an empty dir; refuse a non-empty one without force.
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, res.Created[0].Path, target, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "_attachments/p.png")); string(got) != "png-bytes" {
		t.Fatalf("restored attachment = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal(".git included in tar backup")
	}
	if _, err := RestoreBackup(ctx, res.Created[0].Path, target, false); err == nil {
		t.Fatal("restore over non-empty dir without force")
	}
	aside, err := RestoreBackup(ctx, res.Created[0].Path, target, true)
	if err != nil || aside == "" {
		t.Fatalf("forced restore = %q %v", aside, err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if _, err := RestoreBackup(ctx, res.Created[1].Path, clone, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, "a.md")); err != nil {
		t.Fatal("bundle restore missing a.md")
	}
	// Retention: 40 daily backups → 7 daily + extra weekly/monthly.
	rdir := t.TempDir()
	start := time.Date(2026, 1, 1, 3, 0, 0, 0, time.Local)
	for i := 0; i < 90; i++ {
		name := "nexus-vault-" + start.AddDate(0, 0, i).Format("20060102-150405") + ".tar.gz"
		os.WriteFile(filepath.Join(rdir, name), []byte("x"), 0o600)
	}
	pruned, err := PruneBackups(rdir, DefaultRetention, start.AddDate(0, 0, 90))
	if err != nil {
		t.Fatal(err)
	}
	left, _ := ListBackups(rdir)
	if len(left)+len(pruned) != 90 || len(left) < 7 || len(left) > 7+4+12 {
		t.Fatalf("left %d pruned %d", len(left), len(pruned))
	}
	if !left[0].Created.Equal(start.AddDate(0, 0, 89)) {
		t.Fatal("newest backup pruned")
	}
	if _, err := CreateBackup(ctx, root, BackupOptions{Dir: filepath.Join(root, "bk")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("backup inside vault err = %v", err)
	}
}

func TestBackupPushRemote(t *testing.T) {
	ctx := context.Background()
	root := gitInit(t)
	v := newVault(t, Options{Root: root, Git: true})
	v.Write(ctx, "a.md", "# A\n", "")
	remote := t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare")
	res, err := v.Backup(ctx, BackupOptions{Dir: t.TempDir(), Remote: remote, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.PushResult, "refs/heads/main") {
		t.Fatalf("push = %q", res.PushResult)
	}
	// Remote moved ahead: never forced.
	other := t.TempDir()
	gitOut(t, other, "clone", "-q", "-b", "main", remote, ".")
	gitOut(t, other, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-q", "--allow-empty", "-m", "remote change")
	gitOut(t, other, "push", "-q", "origin", "HEAD:main")
	v.Write(ctx, "b.md", "# B\n", "")
	if _, err := v.Backup(ctx, BackupOptions{Dir: t.TempDir(), Remote: remote, Branch: "main"}); err == nil {
		t.Fatal("diverged push succeeded (force?)")
	}
}

func TestAttachmentsDedupAndExif(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	jpg := fakeJPEG("2025:07:14 18:30:00")
	ref1, err := v.Capture(ctx, &nexusv1.CaptureRequest{Text: "photo", Attachments: []*nexusv1.Attachment{{Name: "a.jpg", Data: jpg}}})
	if err != nil {
		t.Fatal(err)
	}
	ref2, _ := v.Capture(ctx, &nexusv1.CaptureRequest{Text: "same photo", Attachments: []*nexusv1.Attachment{{Name: "b.jpg", Data: jpg}}})
	atts, err := v.ListAttachments(ctx, AttachmentFilter{})
	if err != nil || len(atts) != 1 {
		t.Fatalf("attachments = %+v %v", atts, err)
	}
	if len(atts[0].ReferencedBy) != 2 || atts[0].ReferencedBy[0] != ref1.Path && atts[0].ReferencedBy[1] != ref2.Path {
		t.Fatalf("refs = %v", atts[0].ReferencedBy)
	}
	if atts[0].Taken.Format("2006-01-02 15:04") != "2025-07-14 18:30" || atts[0].MIME != "image/jpeg" {
		t.Fatalf("exif/mime = %v %s", atts[0].Taken, atts[0].MIME)
	}
	writeFile(t, v.Root(), "docs/notes.txt", "quarterly budget figures")
	v.Sync(ctx)
	found, _ := v.ListAttachments(ctx, AttachmentFilter{Query: "budget"})
	if len(found) != 1 || found[0].Path != "docs/notes.txt" {
		t.Fatalf("text search = %+v", found)
	}
	orph, _ := v.ListAttachments(ctx, AttachmentFilter{OrphanedOnly: true})
	if len(orph) != 1 || orph[0].Path != "docs/notes.txt" {
		t.Fatalf("orphans = %+v", orph)
	}
	small := newVault(t, Options{MaxAttachmentBytes: 3})
	if _, err := small.Capture(ctx, &nexusv1.CaptureRequest{Attachments: []*nexusv1.Attachment{{Name: "big.bin", Data: []byte("1234")}}}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size limit err = %v", err)
	}
}

// fakeJPEG builds a minimal JPEG with an EXIF DateTimeOriginal.
func fakeJPEG(date string) []byte {
	var tiff bytes.Buffer
	bo := binary.LittleEndian
	tiff.WriteString("II")
	binary.Write(&tiff, bo, uint16(42))
	binary.Write(&tiff, bo, uint32(8)) // IFD0 at 8
	// IFD0: 1 entry: ExifIFD pointer → 26
	binary.Write(&tiff, bo, uint16(1))
	binary.Write(&tiff, bo, uint16(0x8769))
	binary.Write(&tiff, bo, uint16(4))
	binary.Write(&tiff, bo, uint32(1))
	binary.Write(&tiff, bo, uint32(26))
	binary.Write(&tiff, bo, uint32(0))
	// Exif IFD at 26: DateTimeOriginal ASCII(20) at 44
	binary.Write(&tiff, bo, uint16(1))
	binary.Write(&tiff, bo, uint16(0x9003))
	binary.Write(&tiff, bo, uint16(2))
	binary.Write(&tiff, bo, uint32(20))
	binary.Write(&tiff, bo, uint32(44))
	binary.Write(&tiff, bo, uint32(0))
	tiff.WriteString(date + "\x00")
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(&out, binary.BigEndian, uint16(len(app1)+2))
	out.Write(app1)
	out.Write([]byte{0xFF, 0xD9})
	return out.Bytes()
}

func TestImportNotes(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	writeFile(t, src, ".obsidian/app.json", "{}")
	writeFile(t, src, "Notes/One.md", "---\nid: SAME\n---\n# One\n[[Two]]\r\n")
	writeFile(t, src, "Notes/Two.md", "# Two\n")
	writeFile(t, src, "Notes/Clash.md", "---\nid: EXISTING\n---\n# Clash\n")
	writeFile(t, src, "img/pic.png", "png")
	writeFile(t, src, "junk.exe", "x")
	v := newVault(t, Options{})
	v.Write(ctx, "existing.md", "---\nid: EXISTING\n---\n# E\n", "")
	dry, err := v.ImportNotes(ctx, ImportOptions{Source: src, DryRun: true})
	if err != nil || dry.Notes != 3 {
		t.Fatalf("dry run = %+v %v", dry, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "imported")); err == nil {
		t.Fatal("dry run wrote files")
	}
	res, err := v.ImportNotes(ctx, ImportOptions{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	base := "imported/" + filepath.Base(src)
	if res.Notes != 3 || res.Attachments != 1 || res.IDsAssigned != 1 || res.IDsReplaced != 1 || len(res.Skipped) != 1 {
		t.Fatalf("import = %+v", res)
	}
	one, err := v.Read(ctx, base+"/Notes/One.md")
	if err != nil || one.Ref.Id != "SAME" || one.Links[0].Path != base+"/Notes/Two.md" {
		t.Fatalf("one = %v %v", one, err)
	}
	if strings.Contains(rawOf(t, v, base+"/Notes/One.md"), "\r") {
		t.Fatal("CRLF not normalised")
	}
	clash, _ := v.Read(ctx, base+"/Notes/Clash.md")
	if clash.Ref.Id == "EXISTING" || clash.Frontmatter.AsMap()["imported_id"] != "EXISTING" {
		t.Fatalf("clash = %v", clash)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), base, ".obsidian")); err == nil {
		t.Fatal(".obsidian imported")
	}
	// Re-import never overwrites.
	again, _ := v.ImportNotes(ctx, ImportOptions{Source: src})
	if again.Notes != 0 || len(again.Skipped) < 4 {
		t.Fatalf("re-import = %+v", again)
	}
}

func TestWebClipCapture(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{})
	v.Fetch = func(_ context.Context, url string) (*Clip, error) {
		return &Clip{URL: url, Title: "Great Article", Byline: "Jane", Text: "Body of the article."}, nil
	}
	ref, err := v.Capture(ctx, &nexusv1.CaptureRequest{Url: "https://example.com/a", Clip: true})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := v.Read(ctx, ref.Path)
	if n.Type != "resource" || n.Ref.Title != "Great Article" || !strings.Contains(n.Body, "Body of the article.") ||
		!strings.Contains(n.Body, "Source: https://example.com/a") || n.Frontmatter.AsMap()["author"] != "Jane" {
		t.Fatalf("clip = %v", n)
	}
	v.Fetch = nil
	ref, _ = v.Capture(ctx, &nexusv1.CaptureRequest{Url: "https://example.com/b", Clip: true})
	if n, _ := v.Read(ctx, ref.Path); n.Type != "capture" {
		t.Fatalf("fallback = %v", n)
	}
}

// An agent and a workflow may share an id: they are looked up per kind.
func TestCheckIDsArePerConfigFolder(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := newVault(t, Options{Root: root})
	writeFile(t, root, "agents/shop.md", "---\nid: shop\n---\n# Shop agent\n")
	writeFile(t, root, "workflows/shop.md", "---\nid: shop\n---\n# Shop workflow\n")
	writeFile(t, root, "agents/shop-2.md", "---\nid: shop\n---\n# Another shop agent\n")
	if _, err := v.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := v.Check(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := rep.Counts()[IssueDuplicateID]; n != 1 {
		t.Fatalf("duplicate ids = %d, want 1 (the two agents only): %s", n, rep.Summary())
	}
	for _, is := range rep.Issues {
		if is.Kind == IssueDuplicateID && (strings.HasPrefix(is.Path, "workflows/") || slices.Contains(is.Related, "workflows/shop.md")) {
			t.Fatalf("workflow flagged as a duplicate: %+v", is)
		}
	}
}
