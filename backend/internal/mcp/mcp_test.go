package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/approvalgate"
	"github.com/danielpina1/nexus/backend/kernel"
)

func echoServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer("test", "1.0")
	if err := s.AddTool(Tool{Name: "echo", Description: "Echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		Call: func(_ context.Context, args json.RawMessage) (*CallResult, error) {
			var in struct{ Text string }
			_ = json.Unmarshal(args, &in)
			if in.Text == "fail" {
				return nil, errors.New("boom")
			}
			if in.Text == "panic" {
				panic("kaboom")
			}
			return TextResult("echo: " + in.Text), nil
		}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func rpc(t *testing.T, s *Server, msg string) map[string]any {
	t.Helper()
	out := s.Handle(context.Background(), []byte(msg))
	if out == nil {
		t.Fatalf("no response to %s", msg)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("bad response %s: %v", out, err)
	}
	return m
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return 0
	}
	return int(e["code"].(float64))
}

func TestProtocol(t *testing.T) {
	s := echoServer(t)
	init := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["serverInfo"].(map[string]any)["name"] != "test" ||
		res["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize %v", init)
	}
	if init["id"].(float64) != 1 {
		t.Fatalf("id %v", init["id"])
	}
	init = rpc(t, s, `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if init["result"].(map[string]any)["protocolVersion"] != ProtocolVersion || init["id"] != "a" {
		t.Fatalf("unknown version → %v", init)
	}
	if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); out != nil {
		t.Fatalf("notification answered: %s", out)
	}
	if p := rpc(t, s, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); p["result"] == nil || p["error"] != nil {
		t.Fatalf("ping %v", p)
	}
	list := rpc(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" || tools[0].(map[string]any)["inputSchema"].(map[string]any)["type"] != "object" {
		t.Fatalf("tools/list %v", list)
	}
	call := rpc(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)
	content := call["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "echo: hi" || call["result"].(map[string]any)["isError"] != nil {
		t.Fatalf("tools/call %v", call)
	}
	for _, txt := range []string{"fail", "panic"} {
		c := rpc(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":{"text":"`+txt+`"}}}`)
		if c["result"].(map[string]any)["isError"] != true {
			t.Fatalf("%s: %v", txt, c)
		}
	}
	cases := map[string]int{
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope"}}`: CodeInvalidParams,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`:                      CodeMethodNotFound,
		`{"jsonrpc":"2.0","id":8`:                                                 CodeParseError,
		`{"jsonrpc":"1.0","id":9,"method":"ping"}`:                                CodeInvalidRequest,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`:                             CodeInvalidRequest,
		`[]`: CodeInvalidRequest,
	}
	for msg, code := range cases {
		if got := errCode(rpc(t, s, msg)); got != code {
			t.Errorf("%s → code %d, want %d", msg, got, code)
		}
	}
	// Batch: two requests and a notification → two responses.
	out := s.Handle(context.Background(), []byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`))
	var batch []map[string]any
	if err := json.Unmarshal(out, &batch); err != nil || len(batch) != 2 {
		t.Fatalf("batch %s", out)
	}
	if !IsNotification([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) || IsNotification([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)) {
		t.Fatal("IsNotification")
	}
}

func TestSanitizeAndAddTool(t *testing.T) {
	for in, want := range map[string]string{"memory.search": "memory_search", "ask_user": "ask_user", "a b/c": "a_b_c", strings.Repeat("x", 70): strings.Repeat("x", 64)} {
		if got := SanitizeToolName(in); got != want || !ToolNamePattern.MatchString(got) {
			t.Errorf("Sanitize(%q) = %q", in, got)
		}
	}
	s := NewServer("t", "1")
	if s.AddTool(Tool{Name: "bad.name", Call: func(context.Context, json.RawMessage) (*CallResult, error) { return nil, nil }}) == nil {
		t.Error("dotted name accepted")
	}
	ok := Tool{Name: "ok", Call: func(context.Context, json.RawMessage) (*CallResult, error) { return nil, nil }}
	if err := s.AddTool(ok); err != nil {
		t.Fatal(err)
	}
	if s.AddTool(ok) == nil {
		t.Error("duplicate accepted")
	}
}

func TestHTTPEndpoint(t *testing.T) {
	ep, err := Listen(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if !strings.HasPrefix(ep.URL(), "http://127.0.0.1:") || !strings.Contains(ep.URL(), "/mcp/") {
		t.Fatalf("url %s", ep.URL())
	}
	ctx := context.Background()
	c := NewClient(ep.URL())
	res, err := c.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), ProtocolVersion) {
		t.Fatalf("init %s", res)
	}
	if s, _ := c.session.Load().(string); s == "" {
		t.Error("no Mcp-Session-Id issued")
	}
	r, err := c.CallTool(ctx, "echo", map[string]string{"text": "over http"})
	if err != nil || r.Content[0].Text != "echo: over http" {
		t.Fatalf("call %v %v", r, err)
	}

	// Notification → 202 Accepted, no body.
	resp, err := http.Post(ep.URL(), "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification status %d", resp.StatusCode)
	}
	// GET → 405; wrong token → 404; foreign Origin → 403.
	resp, _ = http.Get(ep.URL())
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d", resp.StatusCode)
	}
	bad := ep.URL()[:strings.LastIndex(ep.URL(), "/")+1] + "wrongtoken"
	resp, _ = http.Post(bad, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bad token status %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, ep.URL(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("evil origin status %d", resp.StatusCode)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, "ping", nil); err == nil {
		t.Error("closed endpoint still answers")
	}
}

func TestStdio(t *testing.T) {
	s := echoServer(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.ServeStdio(context.Background(), inR, outW); outW.Close() }()
	sc := bufio.NewScanner(outR)
	send := func(msg string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
		if !sc.Scan() {
			t.Fatal("no response")
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if r := send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`); r["result"] == nil {
		t.Fatalf("init %v", r)
	}
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n\n")
	if r := send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"stdio"}}}`); !strings.Contains(sc.Text(), "echo: stdio") {
		t.Fatalf("call %v", r)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---- Nexus bridge ----

type evSink struct {
	mu  sync.Mutex
	evs []*nexusv1.Event
	ch  chan *nexusv1.Event
}

func (s *evSink) emit(ev *nexusv1.Event) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	s.ch <- ev
}

func (s *evSink) approvals() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.evs {
		if ev.GetApproval() != nil {
			n++
		}
	}
	return n
}

type countTool struct {
	spec kernel.ToolSpec
	mu   sync.Mutex
	n    int
}

func (c *countTool) Spec() kernel.ToolSpec { return c.spec }
func (c *countTool) Invoke(_ context.Context, call kernel.ToolCall) (kernel.ToolResult, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return kernel.ToolOutput(map[string]any{"ok": true, "run": call.RunID, "input": json.RawMessage(call.Input)})
}

func (c *countTool) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func newBridge(t *testing.T, tools ...kernel.Tool) (*Bridge, *approvalgate.Gate, *evSink, *Client) {
	t.Helper()
	sink := &evSink{ch: make(chan *nexusv1.Event, 64)}
	gate := approvalgate.New(approvalgate.Config{TaskID: "tsk_1", RunID: "run_1", Agent: &nexusv1.Agent{Id: "coder"}, Emit: sink.emit})
	b, err := NewBridge(RunConfig{
		Tools: tools, Gate: gate, Permission: true, Who: "Claude Code", RunID: "run_1", TaskID: "tsk_1",
		Classify: func(tool string, input json.RawMessage) (nexusv1.ActionClass, string) {
			switch tool {
			case "Read":
				return nexusv1.ActionClass_ACTION_CLASS_READ, ""
			case "Bash":
				var in struct{ Command string }
				_ = json.Unmarshal(input, &in)
				if strings.Contains(in.Command, "git push") {
					return nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH, in.Command
				}
				return nexusv1.ActionClass_ACTION_CLASS_COMPUTE, in.Command
			}
			return nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED, ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := Listen(b.Server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close(); _ = ep.Close() })
	c := NewClient(ep.URL())
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b, gate, sink, c
}

func decision(t *testing.T, r *CallResult, err error) PermissionDecision {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var d PermissionDecision
	if len(r.Content) == 0 || json.Unmarshal([]byte(r.Content[0].Text), &d) != nil {
		t.Fatalf("bad permission result %+v", r)
	}
	return d
}

func decider(t *testing.T) func(*CallResult, error) PermissionDecision {
	return func(r *CallResult, err error) PermissionDecision { return decision(t, r, err) }
}

// resolveNext answers the next approval event.
func resolveNext(t *testing.T, sink *evSink, gate *approvalgate.Gate, approve bool) *nexusv1.Approval {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sink.ch:
			if a := ev.GetApproval(); a != nil {
				if err := gate.Resolve(context.Background(), &nexusv1.ApprovalResolution{
					Approval: &nexusv1.Approval{Id: a.Id},
					Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: approve, ActionSha256: a.PayloadHash},
				}); err != nil {
					t.Fatal(err)
				}
				return a
			}
		case <-timeout:
			t.Fatal("no approval requested")
		}
	}
}

func TestBridgeToolsList(t *testing.T) {
	ms := &countTool{spec: kernel.ToolSpec{Name: "memory.search", Description: "Search", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}
	b, _, _, c := newBridge(t, ms, &countTool{spec: kernel.ToolSpec{Name: "memory_search"}})
	res, err := c.Call(context.Background(), "tools/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Tools []toolDesc }
	_ = json.Unmarshal(res, &list)
	if len(list.Tools) != 2 || list.Tools[0].Name != PermissionToolName || list.Tools[1].Name != "memory_search" ||
		!strings.Contains(list.Tools[1].Description, "memory.search") {
		t.Fatalf("tools %+v", list.Tools)
	}
	if tool, ok := b.NexusTool("memory_search"); !ok || tool.Spec().Name != "memory.search" {
		t.Fatal("reverse mapping")
	}
	r, err := c.CallTool(context.Background(), "memory_search", map[string]string{"query": "milk"})
	if err != nil || r.IsError || !strings.Contains(r.Content[0].Text, `"run":"run_1"`) || ms.calls() != 1 {
		t.Fatalf("auto tool call %+v %v", r, err)
	}
}

func TestPermissionTool(t *testing.T) {
	push := &countTool{spec: kernel.ToolSpec{Name: "github.push", ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH}}
	_, gate, sink, c := newBridge(t, push)
	ctx := context.Background()

	// READ → auto allow with the input unchanged.
	d := decider(t)(c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "Read", "input": map[string]any{"file_path": "/x"}, "tool_use_id": "t1"}))
	if d.Behavior != "allow" || string(d.UpdatedInput) != `{"file_path":"/x"}` {
		t.Fatalf("read: %+v", d)
	}
	// COMPUTE (Bash ls) → auto allow by default.
	if d := decider(t)(c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "ls"}})); d.Behavior != "allow" {
		t.Fatalf("bash ls: %+v", d)
	}
	if sink.approvals() != 0 {
		t.Fatal("auto actions asked for approval")
	}

	// git push → CODE_PUSH → approval; denied.
	type res struct {
		r   *CallResult
		err error
	}
	ch := make(chan res, 1)
	go func() {
		r, err := c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "git push origin main"}})
		ch <- res{r, err}
	}()
	a := resolveNext(t, sink, gate, false)
	if a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH || a.Summary != "git push origin main" ||
		a.Payload.Fields["tool"].GetStringValue() != "Bash" || a.Title != "Claude Code wants to use Bash" {
		t.Fatalf("approval %v", a)
	}
	got := <-ch
	if d := decision(t, got.r, got.err); d.Behavior != "deny" || d.Message != "denied by user" {
		t.Fatalf("denied push: %+v", d)
	}

	// Unknown tool → fail safe to approval; approved.
	go func() {
		r, err := c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "Mystery", "input": map[string]any{}})
		ch <- res{r, err}
	}()
	resolveNext(t, sink, gate, true)
	got = <-ch
	if d := decision(t, got.r, got.err); d.Behavior != "allow" {
		t.Fatalf("approved unknown: %+v", d)
	}

	// Nexus tool through the permission prompt: one approval, then the
	// tools/call with the same input runs without asking again.
	go func() {
		r, err := c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "mcp__nexus__github_push", "input": map[string]any{"branch": "main", "repo": "x"}})
		ch <- res{r, err}
	}()
	a = resolveNext(t, sink, gate, true)
	if a.Payload.Fields["tool"].GetStringValue() != "github.push" || a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH {
		t.Fatalf("nexus approval %v", a)
	}
	got = <-ch
	if d := decision(t, got.r, got.err); d.Behavior != "allow" {
		t.Fatalf("nexus allow %+v", d)
	}
	before := sink.approvals()
	r, err := c.CallTool(ctx, "github_push", map[string]any{"repo": "x", "branch": "main"}) // key order differs: canonical match
	if err != nil || r.IsError || push.calls() != 1 {
		t.Fatalf("preapproved call %+v %v", r, err)
	}
	if sink.approvals() != before {
		t.Fatal("preapproved call asked again")
	}

	// A direct tools/call without a permission prompt is gated itself.
	go func() {
		r, err := c.CallTool(ctx, "github_push", map[string]any{"repo": "y"})
		ch <- res{r, err}
	}()
	resolveNext(t, sink, gate, false)
	got = <-ch
	if got.err != nil || !got.r.IsError || got.r.Content[0].Text != "denied by user" || push.calls() != 1 {
		t.Fatalf("direct denied call %+v %v", got.r, got.err)
	}

	// The permission tool cannot be used to approve itself.
	if d := decider(t)(c.CallTool(ctx, PermissionToolName, map[string]any{"tool_name": "mcp__nexus__nexus_permission", "input": map[string]any{}})); d.Behavior != "deny" {
		t.Fatalf("self permission %+v", d)
	}
	// Malformed request → deny.
	if d := decider(t)(c.CallTool(ctx, PermissionToolName, map[string]any{"input": map[string]any{}})); d.Behavior != "deny" {
		t.Fatalf("malformed %+v", d)
	}
}

func TestPermissionCancelledOnGateClose(t *testing.T) {
	_, gate, sink, c := newBridge(t)
	ch := make(chan PermissionDecision, 1)
	go func() {
		r, err := c.CallTool(context.Background(), PermissionToolName, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "git push"}})
		ch <- decision(t, r, err)
	}()
	for ev := range sink.ch {
		if ev.GetApproval() != nil {
			break
		}
	}
	gate.Close()
	select {
	case d := <-ch:
		if d.Behavior != "deny" || !strings.Contains(d.Message, "approval failed") {
			t.Fatalf("%+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permission call not released by gate close")
	}
}

func TestClientConfig(t *testing.T) {
	b, err := ClientConfig("nexus", "http://127.0.0.1:1/mcp/x")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct{ Type, URL string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.MCPServers["nexus"].Type != "http" || cfg.MCPServers["nexus"].URL != "http://127.0.0.1:1/mcp/x" {
		t.Fatalf("config %s", b)
	}
}
