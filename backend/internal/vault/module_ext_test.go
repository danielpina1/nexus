package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// fakeBus records published events.
type fakeBus struct {
	mu  sync.Mutex
	evs []*nexusv1.Event
}

func (b *fakeBus) Publish(_ context.Context, ev *nexusv1.Event) (*nexusv1.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evs = append(b.evs, ev)
	return ev, nil
}
func (b *fakeBus) Subscribe(ctx context.Context, _ int64, _ kernel.EventFilter) (<-chan *nexusv1.Event, error) {
	ch := make(chan *nexusv1.Event)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func (b *fakeBus) LastSeq(context.Context) (int64, error) { return 0, nil }

func (b *fakeBus) notices() []*nexusv1.Notice {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*nexusv1.Notice
	for _, e := range b.evs {
		if n := e.GetNotice(); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func startModuleWith(t *testing.T, root, extra string) (*kerneltest.Host, *Module, *fakeBus) {
	t.Helper()
	h := kerneltest.New(t)
	bus := &fakeBus{}
	h.Provide(kernel.SvcEventBus, kernel.EventBus(bus))
	h.ConfigYAML = "path: " + root + "\ngit: false\nwatch: false\nepisodes: false\n" + extra
	m := &Module{}
	ctx := context.Background()
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	return h, m, bus
}

func TestModuleRobustRPCs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "dup1.md", "---\nid: SAME\n---\n# One\n")
	writeFile(t, root, "dup2.md", "---\nid: SAME\n---\n# Two\n")
	backups := t.TempDir()
	h, m, bus := startModuleWith(t, root, "backup:\n  dir: "+backups+"\nencryption:\n  folders: [private/]\n")
	// Startup quick check raised a notice for the duplicate id.
	deadline := time.Now().Add(3 * time.Second)
	for len(bus.notices()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ns := bus.notices(); len(ns) == 0 || ns[0].Kind != nexusv1.NoticeKind_NOTICE_KIND_ALERT || !strings.Contains(ns[0].Body, "duplicate id") {
		t.Fatalf("startup notices = %v", ns)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "vault.key")); err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	srv := httptest.NewServer(h.Mux)
	defer srv.Close()
	vc := nexusv1connect.NewVaultServiceClient(srv.Client(), srv.URL)

	// Change feed stream.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := vc.WatchVault(sctx, connect.NewRequest(&nexusv1.WatchVaultRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan *nexusv1.VaultChange, 20)
	go func() {
		for stream.Receive() {
			changes <- stream.Msg().Change
		}
		close(changes)
	}()
	time.Sleep(100 * time.Millisecond)

	w, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Ana.md", Markdown: "# Ana\n- [ ] call [[Bo]] 📅 2026-10-20\n"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-changes:
		if c.Kind != nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED || c.Path != "people/Ana.md" || c.Origin != "api" {
			t.Fatalf("change = %v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change streamed")
	}
	// Merge flag on a clean three-way merge.
	vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Ana.md", Markdown: "# Ana\n- [ ] call [[Bo]] 📅 2026-10-20\nnew line\n", BaseCommit: w.Msg.Note.Commit}))
	mw, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Ana.md", Markdown: "# Ana!\n- [ ] call [[Bo]] 📅 2026-10-20\n", BaseCommit: w.Msg.Note.Commit}))
	if err != nil || !mw.Msg.Merged || !strings.Contains(mw.Msg.Note.Body, "# Ana!") || !strings.Contains(mw.Msg.Note.Body, "new line") {
		t.Fatalf("merged write = %v %v", mw, err)
	}

	// Tasks.
	lt, err := vc.ListNoteTasks(ctx, connect.NewRequest(&nexusv1.ListNoteTasksRequest{}))
	if err != nil || len(lt.Msg.Tasks) != 1 || lt.Msg.Tasks[0].Due != "2026-10-20" || lt.Msg.Tasks[0].Note.Path != "people/Ana.md" {
		t.Fatalf("tasks = %v %v", lt, err)
	}
	tk := lt.Msg.Tasks[0]
	tt, err := vc.ToggleTask(ctx, connect.NewRequest(&nexusv1.ToggleTaskRequest{Path: "people/Ana.md", Line: tk.Line, Raw: tk.Raw}))
	if err != nil || !tt.Msg.Task.Done {
		t.Fatalf("toggle = %v %v", tt, err)
	}
	if _, err := vc.ToggleTask(ctx, connect.NewRequest(&nexusv1.ToggleTaskRequest{Path: "people/Ana.md", Line: tk.Line, Raw: tk.Raw})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale toggle = %v", err)
	}

	// Query, rename, backlinks, related, stats.
	vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Bo.md", Markdown: "---\ntype: person\n---\n# Bo\n"}))
	q, err := vc.Query(ctx, connect.NewRequest(&nexusv1.QueryRequest{Query: "type:person"}))
	if err != nil || q.Msg.Total != 1 || q.Msg.Hits[0].Note.Path != "people/Bo.md" || q.Msg.Hits[0].Frontmatter.AsMap()["type"] != "person" {
		t.Fatalf("query = %v %v", q, err)
	}
	rn, err := vc.RenameNote(ctx, connect.NewRequest(&nexusv1.RenameNoteRequest{From: "people/Bo.md", To: "people/Bo Silva"}))
	if err != nil || rn.Msg.Note.Ref.Path != "people/Bo Silva.md" || len(rn.Msg.UpdatedPaths) != 1 {
		t.Fatalf("rename = %v %v", rn, err)
	}
	bl, err := vc.Backlinks(ctx, connect.NewRequest(&nexusv1.BacklinksRequest{Path: "people/Bo Silva.md"}))
	if err != nil || len(bl.Msg.Backlinks) != 1 || !strings.Contains(bl.Msg.Backlinks[0].Context, "[[Bo Silva]]") {
		t.Fatalf("backlinks = %v %v", bl, err)
	}
	if rl, err := vc.Related(ctx, connect.NewRequest(&nexusv1.RelatedRequest{Path: "people/Ana.md"})); err != nil || len(rl.Msg.Notes) == 0 {
		t.Fatalf("related = %v %v", rl, err)
	}
	if gs, err := vc.GraphStats(ctx, connect.NewRequest(&nexusv1.GraphStatsRequest{})); err != nil || gs.Msg.Notes < 3 {
		t.Fatalf("stats = %v %v", gs, err)
	}

	// History (snapshot store in the data dir) and restore.
	hist, err := vc.History(ctx, connect.NewRequest(&nexusv1.HistoryRequest{Path: "people/Ana.md", IncludeDiff: true}))
	if err != nil || len(hist.Msg.Versions) < 3 {
		t.Fatalf("history = %v %v", hist, err)
	}
	first := hist.Msg.Versions[len(hist.Msg.Versions)-1]
	gv, err := vc.GetVersion(ctx, connect.NewRequest(&nexusv1.GetVersionRequest{Path: "people/Ana.md", Commit: first.Commit}))
	if err != nil || strings.Contains(gv.Msg.Markdown, "new line") {
		t.Fatalf("get version = %v %v", gv, err)
	}
	if _, err := vc.RestoreVersion(ctx, connect.NewRequest(&nexusv1.RestoreVersionRequest{Path: "people/Ana.md", Commit: first.Commit})); err != nil {
		t.Fatal(err)
	}

	// Trash round trip.
	del, err := vc.DeleteNote(ctx, connect.NewRequest(&nexusv1.DeleteNoteRequest{Path: "people/Ana.md"}))
	if err != nil {
		t.Fatal(err)
	}
	lt2, _ := vc.ListTrash(ctx, connect.NewRequest(&nexusv1.ListTrashRequest{}))
	if len(lt2.Msg.Items) != 1 || lt2.Msg.Items[0].Id != del.Msg.Item.Id {
		t.Fatalf("trash = %v", lt2)
	}
	if rt, err := vc.RestoreFromTrash(ctx, connect.NewRequest(&nexusv1.RestoreFromTrashRequest{Id: del.Msg.Item.Id})); err != nil || rt.Msg.Path != "people/Ana.md" || rt.Msg.Note == nil {
		t.Fatalf("restore from trash = %v %v", rt, err)
	}

	// Encrypted folder through the API, but not through agent tools.
	if _, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "private/pin.md", Markdown: "# PIN\n1234\n"})); err != nil {
		t.Fatal(err)
	}
	gn, err := vc.GetNote(ctx, connect.NewRequest(&nexusv1.GetNoteRequest{Path: "private/pin.md"}))
	if err != nil || !gn.Msg.Note.Encrypted || !strings.Contains(gn.Msg.Note.Body, "1234") {
		t.Fatalf("private note = %v %v", gn, err)
	}
	tool, _ := h.Tools().Get("vault.read")
	if _, err := tool.Invoke(ctx, kernel.ToolCall{Input: json.RawMessage(`{"path":"private/pin.md"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent read of private note err = %v", err)
	}

	// Templates.
	vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "templates/idea.md", Markdown: "# {{title}}\nWhy: {{prompt:Why}}\n"}))
	tl, _ := vc.ListTemplates(ctx, connect.NewRequest(&nexusv1.ListTemplatesRequest{}))
	if len(tl.Msg.Templates) != 1 || tl.Msg.Templates[0].Prompts[0] != "Why" {
		t.Fatalf("templates = %v", tl)
	}
	nt, err := vc.NewFromTemplate(ctx, connect.NewRequest(&nexusv1.NewFromTemplateRequest{Template: "idea", Title: "Rocket"}))
	if err != nil || nt.Msg.Note != nil || nt.Msg.MissingPrompts[0] != "Why" {
		t.Fatalf("missing prompts = %v %v", nt, err)
	}
	nt, err = vc.NewFromTemplate(ctx, connect.NewRequest(&nexusv1.NewFromTemplateRequest{Template: "idea", Title: "Rocket", Variables: map[string]string{"Why": "fun"}}))
	if err != nil || !strings.Contains(nt.Msg.Note.Body, "Why: fun") {
		t.Fatalf("new from template = %v %v", nt, err)
	}
	pn, err := vc.PeriodicNote(ctx, connect.NewRequest(&nexusv1.PeriodicNoteRequest{Period: "daily", Date: "2026-10-18", Create: true}))
	if err != nil || !pn.Msg.Created || pn.Msg.Path != "daily/2026-10-18.md" {
		t.Fatalf("periodic = %v %v", pn, err)
	}

	// Check / Repair.
	ck, err := vc.Check(ctx, connect.NewRequest(&nexusv1.CheckRequest{Full: true}))
	if err != nil || ck.Msg.Report.Counts[IssueDuplicateID] != 1 {
		t.Fatalf("check = %v %v", ck, err)
	}
	rp, err := vc.Repair(ctx, connect.NewRequest(&nexusv1.RepairRequest{}))
	if err != nil || rp.Msg.Report.Counts[IssueDuplicateID] != 0 || len(rp.Msg.Actions) == 0 {
		t.Fatalf("repair = %v %v", rp, err)
	}

	// Backups.
	bk, err := vc.Backup(ctx, connect.NewRequest(&nexusv1.BackupRequest{}))
	if err != nil || len(bk.Msg.Created) != 1 {
		t.Fatalf("backup = %v %v", bk, err)
	}
	lb, _ := vc.ListBackups(ctx, connect.NewRequest(&nexusv1.ListBackupsRequest{}))
	if len(lb.Msg.Backups) != 1 || lb.Msg.Dir != backups {
		t.Fatalf("list backups = %v", lb)
	}
	vb, err := vc.VerifyBackup(ctx, connect.NewRequest(&nexusv1.VerifyBackupRequest{Name: lb.Msg.Backups[0].Name}))
	if err != nil || !vb.Msg.Ok {
		t.Fatalf("verify = %v %v", vb, err)
	}
	if _, err := vc.VerifyBackup(ctx, connect.NewRequest(&nexusv1.VerifyBackupRequest{Name: "../../etc/passwd"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("path traversal verify = %v", err)
	}

	// Attachments and import.
	if _, err := vc.Capture(ctx, connect.NewRequest(&nexusv1.CaptureRequest{Text: "pic", Attachments: []*nexusv1.Attachment{{Name: "a.png", Data: []byte("png")}}})); err != nil {
		t.Fatal(err)
	}
	la, err := vc.ListAttachments(ctx, connect.NewRequest(&nexusv1.ListAttachmentsRequest{}))
	if err != nil || len(la.Msg.Attachments) != 1 || len(la.Msg.Attachments[0].ReferencedBy) != 1 {
		t.Fatalf("attachments = %v %v", la, err)
	}
	src := t.TempDir()
	writeFile(t, src, "x.md", "# X\n")
	im, err := vc.ImportNotes(ctx, connect.NewRequest(&nexusv1.ImportNotesRequest{SourceDir: src, DestFolder: "imp"}))
	if err != nil || im.Msg.Notes != 1 {
		t.Fatalf("import = %v %v", im, err)
	}
	if _, err := vc.ImportNotes(ctx, connect.NewRequest(&nexusv1.ImportNotesRequest{SourceDir: "relative"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative import = %v", err)
	}
	// Graph filters over the API.
	g, err := vc.Graph(ctx, connect.NewRequest(&nexusv1.GraphRequest{PathPrefix: "people/", HideDangling: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Msg.Nodes {
		if !strings.HasPrefix(n.Path, "people/") {
			t.Fatalf("graph node outside prefix: %v", n)
		}
	}
	_ = m
}

func TestBackupsNotConfigured(t *testing.T) {
	h, _, _ := startModuleWith(t, t.TempDir(), "check:\n  on_start: false\n")
	srv := httptest.NewServer(h.Mux)
	defer srv.Close()
	vc := nexusv1connect.NewVaultServiceClient(srv.Client(), srv.URL)
	if _, err := vc.Backup(context.Background(), connect.NewRequest(&nexusv1.BackupRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("backup without dir = %v", err)
	}
}

func TestExtendedAgentTools(t *testing.T) {
	ctx := context.Background()
	h, m, _ := startModuleWith(t, t.TempDir(), "check:\n  on_start: false\n")
	call := func(name string, in any) (map[string]any, error) {
		tool, ok := h.Tools().Get(name)
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		b, _ := json.Marshal(in)
		res, err := tool.Invoke(ctx, kernel.ToolCall{RunID: "run_9", AgentID: "librarian", Input: b})
		if err != nil {
			return nil, err
		}
		var out map[string]any
		json.Unmarshal(res.Output, &out)
		return out, nil
	}
	for name, class := range map[string]nexusv1.ActionClass{
		"vault.query": nexusv1.ActionClass_ACTION_CLASS_READ, "vault.history": nexusv1.ActionClass_ACTION_CLASS_READ,
		"vault.restore": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, "vault.rename": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		"vault.tasks": nexusv1.ActionClass_ACTION_CLASS_READ, "vault.related": nexusv1.ActionClass_ACTION_CLASS_READ,
		"vault.complete_task": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
	} {
		tool, _ := h.Tools().Get(name)
		if tool.Spec().ActionClass != class {
			t.Errorf("%s class = %v", name, tool.Spec().ActionClass)
		}
		// restore/rename need one-tap approval: an explicit minimum level,
		// not an accident of an unspecified class.
		want := nexusv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED
		if name == "vault.restore" || name == "vault.rename" {
			want = nexusv1.TrustLevel_TRUST_LEVEL_APPROVE
		}
		if tool.Spec().MinLevel != want {
			t.Errorf("%s min level = %v", name, tool.Spec().MinLevel)
		}
		if got := approvalgate.WithMin(approvalgate.Level(nil, tool.Spec().ActionClass, nil), tool.Spec().MinLevel); want != 0 && got != want {
			t.Errorf("%s gate level = %v, want %v", name, got, want)
		}
	}
	m.V.Write(ctx, "projects/Apollo.md", "---\ntype: project\nstatus: active\n---\n# Apollo\n- [ ] launch 📅 2026-11-01\n", "")
	m.V.Write(ctx, "people/Jo.md", "Works on [[Apollo]].\n", "")
	q, err := call("vault.query", map[string]any{"query": "type:project status:active"})
	if err != nil || q["total"].(float64) != 1 {
		t.Fatalf("vault.query = %v %v", q, err)
	}
	ts, _ := call("vault.tasks", map[string]any{"due_before": "2026-12-01"})
	tasks := ts["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("vault.tasks = %v", ts)
	}
	tk := tasks[0].(map[string]any)
	if out, err := call("vault.complete_task", map[string]any{"path": tk["path"], "line": tk["line"], "raw": tk["raw"]}); err != nil || out["done"] != true {
		t.Fatalf("complete = %v %v", out, err)
	}
	rel, _ := call("vault.related", map[string]any{"path": "projects/Apollo.md"})
	if len(rel["notes"].([]any)) != 1 {
		t.Fatalf("related = %v", rel)
	}
	rn, err := call("vault.rename", map[string]any{"from": "projects/Apollo.md", "to": "projects/Apollo 11.md"})
	if err != nil || len(rn["updated_notes"].([]any)) != 1 {
		t.Fatalf("rename = %v %v", rn, err)
	}
	for _, bad := range []map[string]any{{"from": "projects/Apollo 11.md", "to": "agents/evil.md"}, {"from": "people/Jo.md", "to": "skills/x.md"}} {
		if _, err := call("vault.rename", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("rename into protected dir %v err = %v", bad, err)
		}
	}
	if _, err := call("vault.write", map[string]any{"path": "teams/x.md", "content": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("write to teams/ err = %v", err)
	}
	hist, err := call("vault.history", map[string]any{"path": "projects/Apollo 11.md", "diff": true})
	if err != nil || len(hist["versions"].([]any)) < 2 {
		t.Fatalf("history = %v %v", hist, err)
	}
	vs := hist["versions"].([]any)
	oldest := vs[len(vs)-1].(map[string]any)["commit"].(string)
	if out, err := call("vault.restore", map[string]any{"path": "projects/Apollo 11.md", "commit": oldest}); err != nil || out["path"] != "projects/Apollo 11.md" {
		t.Fatalf("restore = %v %v", out, err)
	}
	if b := rawOf(t, m.V, "projects/Apollo 11.md"); !strings.Contains(b, "- [ ] launch") {
		t.Fatalf("restored content = %s", b)
	}
}

func TestWatcherFeedsChangesAndSnapshots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	v := newVault(t, Options{Root: root, StateDir: t.TempDir()})
	stop, err := v.Watch(ctx, 50*time.Millisecond)
	if err != nil {
		t.Skip("watcher unavailable:", err)
	}
	defer stop()
	got := make(chan Change, 10)
	go v.WatchChanges(ctx, 0, "", func(c Change) error { got <- c; return nil })
	time.Sleep(50 * time.Millisecond)
	writeFile(t, root, "ext.md", "# Edited in Obsidian\n")
	select {
	case c := <-got:
		if c.Kind != nexusv1.VaultChangeKind_VAULT_CHANGE_KIND_CREATED || c.Path != "ext.md" || c.Origin != "external" {
			t.Fatalf("change = %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external edit not streamed")
	}
	hist, err := v.History(ctx, "ext.md", 0, false)
	if err != nil || len(hist) != 1 || hist[0].Message != "edited outside Nexus" {
		t.Fatalf("external edit history = %+v %v", hist, err)
	}
	// Our own writes are not reported twice by the watcher.
	v.Write(ctx, "own.md", "x", "")
	n := 0
	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case c := <-got:
			if c.Path == "own.md" {
				n++
			}
		case <-timeout:
			break loop
		}
	}
	if n != 1 {
		t.Fatalf("own write reported %d times", n)
	}
}
