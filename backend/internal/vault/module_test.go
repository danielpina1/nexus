package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

// copyTemplate copies ../../../vault-template into a temp dir.
func copyTemplate(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "vault-template")
	if _, err := os.Stat(src); err != nil {
		t.Skip("vault-template not found")
	}
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestCatalog(t *testing.T) {
	ctx := context.Background()
	root := copyTemplate(t)
	writeFile(t, root, "agents/broken.md", "---\nid: broken\nruntime: shell\ntier: galactic\n---\n# Broken\n")
	writeFile(t, root, "agents/dupe.md", "---\nid: assistant\ndescription: d\nruntime: shell\n---\n")
	writeFile(t, root, "agents/off.md", "---\nid: off\ndescription: d\nruntime: shell\nenabled: false\n---\n")
	v := newVault(t, Options{Root: root})
	c, err := NewCatalog(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	enabled, _ := c.ListAgents(ctx, false)
	var ids []string
	for _, a := range enabled {
		ids = append(ids, a.Id)
	}
	if strings.Join(ids, ",") != "assistant,code-fixer,grocery-continente,librarian,ops" {
		t.Fatalf("enabled = %v", ids)
	}
	all, _ := c.ListAgents(ctx, true)
	if len(all) != 8 {
		t.Fatalf("all = %d", len(all))
	}
	for _, a := range all {
		switch a.Note.Path {
		case "agents/broken.md":
			if a.Enabled || len(a.Errors) != 2 {
				t.Errorf("broken = %v", a)
			}
		case "agents/dupe.md":
			if a.Enabled || !strings.Contains(strings.Join(a.Errors, ";"), "duplicate id") {
				t.Errorf("dupe = %v", a)
			}
		}
	}
	a, instr, md, err := c.GetAgent(ctx, "grocery-continente")
	if err != nil || a.Limits.MaxSpend.MinorUnits != 15000 || !strings.HasPrefix(instr, "# Grocery (Continente)") || !strings.HasPrefix(md, "---\ntype: agent") {
		t.Fatalf("get = %v %q %v", a, instr, err)
	}
	if _, _, _, err := c.GetAgent(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent: %v", err)
	}

	// Upsert: invalid is rejected, valid is written and listed.
	if _, err := c.UpsertAgent(ctx, "---\nid: x\n---\n", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid upsert: %v", err)
	}
	if _, err := c.UpsertAgent(ctx, "no frontmatter", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-id upsert: %v", err)
	}
	na, err := c.UpsertAgent(ctx, "---\ntype: agent\nid: writer\nname: Writer\ndescription: Writes things.\nruntime: openai\ntier: simple\n---\n# Writer\nWrite well.\n", "add writer")
	if err != nil {
		t.Fatal(err)
	}
	if na.Note.Path != "agents/writer.md" || !na.Enabled || na.Tier != nexusv1.Tier_TIER_SIMPLE {
		t.Fatalf("upserted = %v", na)
	}
	// Updating an id defined in a differently named file keeps that file.
	writeFile(t, root, "agents/legacy-name.md", "---\nid: legacy\ndescription: d\nruntime: shell\n---\n")
	c.Reload(ctx)
	la, err := c.UpsertAgent(ctx, "---\nid: legacy\ndescription: new\nruntime: shell\n---\n", "")
	if err != nil || la.Note.Path != "agents/legacy-name.md" || la.Description != "new" {
		t.Fatalf("legacy upsert = %v %v", la, err)
	}
	// Writes through the vault (e.g. vault.write) reload the catalog.
	v.Write(ctx, "agents/ops.md", "---\nid: ops\ndescription: changed\nruntime: shell\nversion: 2\n---\n", "")
	if a, _, _, _ := c.GetAgent(ctx, "ops"); a.Description != "changed" || a.Version != 2 {
		t.Fatalf("not reloaded: %v", a)
	}
}

type fakeLLM struct {
	got kernel.ChatRequest
	err error
}

func (f *fakeLLM) Chat(_ context.Context, r kernel.ChatRequest) (*kernel.ChatResponse, error) {
	f.got = r
	if f.err != nil {
		return nil, f.err
	}
	return &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", Content: "Use Continente [[Shopping preferences]]."}}, nil
}
func (f *fakeLLM) ChatStream(ctx context.Context, r kernel.ChatRequest, _ func(string)) (*kernel.ChatResponse, error) {
	return f.Chat(ctx, r)
}
func (f *fakeLLM) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, errors.New("no embeddings")
}

func startModule(t *testing.T) (*kerneltest.Host, *Module, string) {
	t.Helper()
	root := copyTemplate(t)
	h := kerneltest.New(t)
	h.ConfigYAML = "path: " + root + "\ngit: false\nwatch: false\n"
	m := &Module{}
	ctx := context.Background()
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	return h, m, root
}

func TestModule(t *testing.T) {
	ctx := context.Background()
	h, m, _ := startModule(t)
	if info := m.Info(); info.Name != "vault" || !info.DefaultEnabled || info.Side != kernel.SideHub || len(info.Requires) != 0 {
		t.Fatalf("info = %+v", info)
	}
	if _, err := kernel.Resolve[kernel.Vault](h, kernel.SvcVault); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Resolve[kernel.AgentCatalog](h, kernel.SvcAgents); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "index.db")); err != nil {
		t.Fatalf("index.db not in data dir: %v", err)
	}
	var names []string
	for _, s := range h.Tools().List() {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "facts.propose,memory.search,vault.daily_append,vault.link,vault.read,vault.write" {
		t.Fatalf("tools = %v", names)
	}

	srv := httptest.NewServer(h.Mux)
	defer srv.Close()
	vc := nexusv1connect.NewVaultServiceClient(srv.Client(), srv.URL)
	ac := nexusv1connect.NewAgentServiceClient(srv.Client(), srv.URL)

	agents, err := ac.ListAgents(ctx, connect.NewRequest(&nexusv1.ListAgentsRequest{}))
	if err != nil || len(agents.Msg.Agents) != 5 {
		t.Fatalf("agents = %v %v", agents, err)
	}
	ga, err := ac.GetAgent(ctx, connect.NewRequest(&nexusv1.GetAgentRequest{Id: "librarian"}))
	if err != nil || ga.Msg.Agent.Tier != nexusv1.Tier_TIER_SIMPLE || !strings.Contains(ga.Msg.Markdown, "# Librarian") {
		t.Fatalf("get agent = %v %v", ga, err)
	}
	if _, err := ac.GetAgent(ctx, connect.NewRequest(&nexusv1.GetAgentRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing agent code = %v", connect.CodeOf(err))
	}
	if _, err := ac.UpsertAgent(ctx, connect.NewRequest(&nexusv1.UpsertAgentRequest{Markdown: "---\nid: bad\n---\n"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid upsert code = %v", connect.CodeOf(err))
	}

	wr, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Maria.md", Markdown: "---\ntype: person\n---\n# Maria\nLoves [[Continente]] deals.\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "people/Maria.md", Markdown: "x", BaseCommit: "sha256:stale"})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("conflict code = %v", connect.CodeOf(err))
	}
	if _, err := vc.WriteNote(ctx, connect.NewRequest(&nexusv1.WriteNoteRequest{Path: "../x.md", Markdown: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("traversal code = %v", connect.CodeOf(err))
	}
	gn, err := vc.GetNote(ctx, connect.NewRequest(&nexusv1.GetNoteRequest{Id: wr.Msg.Note.Ref.Id}))
	if err != nil || gn.Msg.Note.Ref.Path != "people/Maria.md" {
		t.Fatalf("get note = %v %v", gn, err)
	}
	sr, err := vc.Search(ctx, connect.NewRequest(&nexusv1.SearchRequest{Query: "continente", Types: []string{"agent"}}))
	if err != nil || len(sr.Msg.Hits) != 1 || sr.Msg.Hits[0].Note.Id != "grocery-continente" {
		t.Fatalf("search = %v %v", sr, err)
	}
	gr, err := vc.Graph(ctx, connect.NewRequest(&nexusv1.GraphRequest{CenterId: "grocery-continente", Depth: 1}))
	if err != nil || len(gr.Msg.Nodes) < 2 {
		t.Fatalf("graph = %v %v", gr, err)
	}
	cr, err := vc.Capture(ctx, connect.NewRequest(&nexusv1.CaptureRequest{Text: "remember the milk"}))
	if err != nil || !strings.HasPrefix(cr.Msg.Note.Path, "inbox/") {
		t.Fatalf("capture = %v %v", cr, err)
	}

	// Ask without an LLM: snippets.
	ar, err := vc.Ask(ctx, connect.NewRequest(&nexusv1.AskRequest{Question: "continente"}))
	if err != nil || !strings.HasPrefix(ar.Msg.Answer, "Relevant notes:") || len(ar.Msg.Sources) == 0 {
		t.Fatalf("ask = %v %v", ar, err)
	}
	// With an LLM (resolved lazily, provided after Init).
	llm := &fakeLLM{}
	h.Provide(kernel.SvcLLM, kernel.LLM(llm))
	ar, err = vc.Ask(ctx, connect.NewRequest(&nexusv1.AskRequest{Question: "continente"}))
	if err != nil || ar.Msg.Answer != "Use Continente [[Shopping preferences]]." {
		t.Fatalf("ask llm = %v %v", ar, err)
	}
	if llm.got.Model != "smart" || !strings.Contains(llm.got.Messages[1].Content, "Question: continente") {
		t.Fatalf("llm request = %+v", llm.got)
	}
	llm.err = errors.New("down")
	ar, _ = vc.Ask(ctx, connect.NewRequest(&nexusv1.AskRequest{Question: "continente"}))
	if !strings.HasPrefix(ar.Msg.Answer, "Relevant notes:") {
		t.Fatalf("fallback = %q", ar.Msg.Answer)
	}

	// Facts via tools + RPCs.
	call := func(name string, in any) map[string]any {
		t.Helper()
		tool, ok := h.Tools().Get(name)
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		b, _ := json.Marshal(in)
		res, err := tool.Invoke(ctx, kernel.ToolCall{RunID: "run_1", AgentID: "librarian", Input: b})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out map[string]any
		json.Unmarshal(res.Output, &out)
		return out
	}
	fo := call("facts.propose", map[string]any{"subject": "Maria", "predicate": "prefers", "object": "Continente", "confidence": 0.6})
	lf, err := vc.ListFacts(ctx, connect.NewRequest(&nexusv1.ListFactsRequest{Status: nexusv1.FactStatus_FACT_STATUS_PROPOSED}))
	if err != nil || len(lf.Msg.Facts) != 1 || lf.Msg.Facts[0].Id != fo["id"] || lf.Msg.Facts[0].Subject.Path != "people/Maria.md" {
		t.Fatalf("facts = %v %v", lf, err)
	}
	rf, err := vc.ReviewFact(ctx, connect.NewRequest(&nexusv1.ReviewFactRequest{Id: fo["id"].(string), Status: nexusv1.FactStatus_FACT_STATUS_REJECTED}))
	if err != nil || rf.Msg.Fact.Status != nexusv1.FactStatus_FACT_STATUS_REJECTED {
		t.Fatalf("review = %v %v", rf, err)
	}

	// Remaining tools.
	hits := call("memory.search", map[string]any{"query": "maria"})["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("memory.search found nothing")
	}
	rd := call("vault.read", map[string]any{"path": "people/Maria.md"})
	if rd["title"] != "Maria" || rd["commit"] == "" {
		t.Fatalf("vault.read = %v", rd)
	}
	wo := call("vault.write", map[string]any{"path": "people/Maria.md", "append": "Birthday:: 1990-01-01", "base_commit": "ignored-for-append"})
	if wo["path"] != "people/Maria.md" {
		t.Fatalf("vault.write = %v", wo)
	}
	call("vault.write", map[string]any{"path": "topics/New.md", "content": "# New\n", "message": "new topic"})
	call("vault.link", map[string]any{"from": "topics/New.md", "to": "people/Maria.md", "relation": "about"})
	call("vault.daily_append", map[string]any{"text": "tested tools", "date": "2026-10-11"})
	n, err := m.V.Read(ctx, "daily/2026-10-11.md")
	if err != nil || !strings.Contains(n.Body, "tested tools") {
		t.Fatalf("daily = %v %v", n, err)
	}
	n, _ = m.V.Read(ctx, "people/Maria.md")
	if len(n.Backlinks) != 2 { // topics/New (about) + the fact (subject)
		t.Fatalf("maria backlinks = %v", n.Backlinks)
	}
	if !strings.Contains(n.Body, "Birthday:: 1990-01-01") {
		t.Fatalf("append failed: %q", n.Body)
	}
	tool, _ := h.Tools().Get("vault.write")
	if _, err := tool.Invoke(ctx, kernel.ToolCall{Input: []byte(`{"path":"x.md"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty write: %v", err)
	}
}
