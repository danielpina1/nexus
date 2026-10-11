package nodelink_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/node"
	"github.com/danielpina1/nexus/backend/internal/nodelink"
	"github.com/danielpina1/nexus/backend/internal/runtime/echo"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

type fakeBus struct {
	mu  sync.Mutex
	evs []*nexusv1.Event
}

func (b *fakeBus) Publish(_ context.Context, ev *nexusv1.Event) (*nexusv1.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ev.Seq = int64(len(b.evs) + 1)
	b.evs = append(b.evs, ev)
	return ev, nil
}
func (b *fakeBus) Subscribe(context.Context, int64, kernel.EventFilter) (<-chan *nexusv1.Event, error) {
	return nil, errors.New("not implemented")
}
func (b *fakeBus) LastSeq(context.Context) (int64, error) { return 0, nil }

func (b *fakeBus) nodeStatuses() []*nexusv1.NodeStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*nexusv1.NodeStatus
	for _, e := range b.evs {
		if s := e.GetNodeStatus(); s != nil {
			out = append(out, s)
		}
	}
	return out
}

type fakeTasks struct {
	kernel.TaskEngine // unused methods panic
	ch                chan *nexusv1.RunEvent
}

func (f *fakeTasks) HandleRunEvent(_ context.Context, nodeID string, ev *nexusv1.RunEvent) error {
	if nodeID != "n1" {
		return errors.New("unexpected node " + nodeID)
	}
	f.ch <- ev
	return nil
}

type env struct {
	srv    *httptest.Server
	hubH   *kerneltest.Host
	disp   kernel.Dispatcher
	bus    *fakeBus
	tasks  *fakeTasks
	daemon *node.Daemon
	cancel context.CancelFunc
	done   chan struct{}
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{bus: &fakeBus{}, tasks: &fakeTasks{ch: make(chan *nexusv1.RunEvent, 256)}}

	// Hub.
	e.hubH = kerneltest.New(t)
	e.hubH.ConfigYAML = "tokens:\n  n1: secret\n"
	e.hubH.Provide(kernel.SvcEventBus, e.bus)
	e.hubH.Provide(kernel.SvcTasks, e.tasks)
	_ = e.hubH.Tools().Register(kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "test.ping", Description: "ping"},
		F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]any{"pong": c.RunID, "node": c.NodeID})
		},
	})
	m := &nodelink.Module{}
	ctx := context.Background()
	if err := m.Init(ctx, e.hubH); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	disp, err := kernel.Resolve[kernel.Dispatcher](e.hubH, kernel.SvcDispatcher)
	if err != nil {
		t.Fatal(err)
	}
	e.disp = disp

	e.srv = httptest.NewUnstartedServer(e.hubH.Mux)
	e.srv.EnableHTTP2 = true
	e.srv.StartTLS()
	t.Cleanup(e.srv.Close)

	// Node.
	nh := kerneltest.New(t)
	nh.SideValue = kernel.SideNode
	if err := (echo.Module{}).Init(ctx, nh); err != nil {
		t.Fatal(err)
	}
	_ = nh.Runtimes().Register(toolRuntime{})
	d, err := node.New(nh, node.Config{
		HubURL: e.srv.URL, NodeID: "n1", Token: "secret", Tags: []string{"test"},
		WorkspacesDir: filepath.Join(nh.Dir, "ws"), BackoffMin: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	}, node.Options{HTTPClient: e.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RegisterTools(); err != nil {
		t.Fatal(err)
	}
	nh.Provide(kernel.SvcHubTools, d)
	e.daemon = d
	rctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() { defer close(e.done); _ = d.Run(rctx) }()
	t.Cleanup(func() { cancel(); <-e.done })

	e.waitOnline(t)
	return e
}

func (e *env) waitOnline(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nodes, _ := e.disp.Nodes(context.Background())
		if len(nodes) == 1 && nodes[0].Status.Online {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("node did not come online")
}

// next waits for a run event matching pred, returning everything seen.
func (e *env) next(t *testing.T, pred func(*nexusv1.Event) bool) *nexusv1.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case re := <-e.tasks.ch:
			if re.NodeSeq == 0 {
				t.Fatalf("run event without node_seq: %v", re)
			}
			if pred(re.Event) {
				return re.Event
			}
		case <-timeout:
			t.Fatal("timed out waiting for run event")
		}
	}
}

func terminal(state nexusv1.TaskState) func(*nexusv1.Event) bool {
	return func(ev *nexusv1.Event) bool {
		return ev.GetTaskUpdated().GetTask().GetState() == state
	}
}

func TestEchoRunWithApproval(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// Hello published an online NodeStatus with tags from the node.
	nodes, _ := e.disp.Nodes(ctx)
	if got := nodes[0]; got.Id != "n1" || got.Os == "" || len(got.Runtimes) != 2 {
		t.Fatalf("unexpected node %+v", got)
	}
	if len(e.bus.nodeStatuses()) == 0 {
		t.Fatal("no NodeStatus published")
	}

	a := &nexusv1.RunAssignment{
		RunId: "run_1", TaskId: "tsk_1", Prompt: "hello brave world ?approve",
		Agent: &nexusv1.Agent{Id: "echoer", Runtime: "echo"},
	}
	nodeID, err := e.disp.Assign(ctx, kernel.NodeSelector{Tags: []string{"test"}}, a)
	if err != nil || nodeID != "n1" {
		t.Fatalf("assign: %v %q", err, nodeID)
	}
	if _, err := e.disp.Assign(ctx, kernel.NodeSelector{Tags: []string{"gpu"}}, &nexusv1.RunAssignment{RunId: "run_x", Agent: &nexusv1.Agent{Runtime: "echo"}}); !errors.Is(err, nodelink.ErrNoNode) {
		t.Fatalf("want ErrNoNode, got %v", err)
	}

	e.next(t, terminal(nexusv1.TaskState_TASK_STATE_RUNNING))
	out := e.next(t, func(ev *nexusv1.Event) bool { return ev.GetRunOutput() != nil })
	if out.GetRunOutput().Text != "hello " || out.RunId != "run_1" || out.TaskId != "tsk_1" || out.Actor != "agent:echoer" {
		t.Fatalf("unexpected output event %v", out)
	}
	apr := e.next(t, func(ev *nexusv1.Event) bool { return ev.GetApproval() != nil }).GetApproval()
	if apr.NodeId != "n1" || apr.State != nexusv1.ApprovalState_APPROVAL_STATE_PENDING {
		t.Fatalf("unexpected approval %v", apr)
	}
	e.next(t, terminal(nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL))

	// Drop the link: the resolution is queued/re-sent on reconnect.
	e.srv.CloseClientConnections()
	apr.State = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	if err := e.disp.Resolve(ctx, nodeID, &nexusv1.ApprovalResolution{
		RunId: "run_1", Approval: apr,
		Decision: &nexusv1.SignedApproval{ApprovalId: apr.Id, Approve: true},
	}); err != nil {
		t.Fatal(err)
	}
	final := e.next(t, func(ev *nexusv1.Event) bool {
		return ev.GetTaskUpdated() != nil && ev.GetTaskUpdated().Task.State >= nexusv1.TaskState_TASK_STATE_SUCCEEDED
	})
	if s := final.GetTaskUpdated().Task; s.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || s.Summary != "echoed 4 words" || s.NodeId != "n1" {
		t.Fatalf("unexpected final %v", s)
	}
	// Active count drops after the terminal event.
	nodes, _ = e.disp.Nodes(ctx)
	if nodes[0].Status.ActiveRuns != 0 {
		t.Fatalf("active runs = %d", nodes[0].Status.ActiveRuns)
	}
}

func TestAskAndCancel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := &nexusv1.RunAssignment{RunId: "run_q", TaskId: "tsk_q", Prompt: "x ?ask", Agent: &nexusv1.Agent{Runtime: "echo"}}
	if _, err := e.disp.Assign(ctx, kernel.NodeSelector{NodeID: "n1"}, a); err != nil {
		t.Fatal(err)
	}
	q := e.next(t, func(ev *nexusv1.Event) bool { return ev.GetQuestion() != nil }).GetQuestion()
	if err := e.disp.SendInput(ctx, "n1", &nexusv1.RunInput{RunId: "run_q", QuestionId: q.QuestionId, Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	e.next(t, func(ev *nexusv1.Event) bool { return ev.GetRunOutput().GetText() == "\nanswer: yes" })
	e.next(t, terminal(nexusv1.TaskState_TASK_STATE_SUCCEEDED))

	// Cancel a run waiting on a question.
	b := &nexusv1.RunAssignment{RunId: "run_c", TaskId: "tsk_c", Prompt: "?ask", Agent: &nexusv1.Agent{Runtime: "echo"}}
	if _, err := e.disp.Assign(ctx, kernel.NodeSelector{}, b); err != nil {
		t.Fatal(err)
	}
	e.next(t, terminal(nexusv1.TaskState_TASK_STATE_WAITING_INPUT))
	if err := e.disp.Cancel(ctx, "n1", &nexusv1.RunCancel{RunId: "run_c"}); err != nil {
		t.Fatal(err)
	}
	e.next(t, terminal(nexusv1.TaskState_TASK_STATE_CANCELLED))
}

// toolRuntime calls every tool it was given and reports the results.
type toolRuntime struct{}

func (toolRuntime) Name() string                                  { return "tools" }
func (toolRuntime) Capabilities() kernel.RuntimeCapabilities      { return kernel.RuntimeCapabilities{} }
func (toolRuntime) Available(context.Context) (string, bool)      { return "t", true }
func (toolRuntime) Send(context.Context, *nexusv1.RunInput) error { return nil }
func (toolRuntime) Start(ctx context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	ch := make(chan *nexusv1.Event, 16)
	go func() {
		defer close(ch)
		res := map[string]any{}
		for _, tl := range spec.Tools {
			out, err := tl.Invoke(ctx, kernel.ToolCall{RunID: "spoofed", Input: json.RawMessage(`{}`)})
			if err != nil {
				res[tl.Spec().Name] = "error: " + err.Error()
				continue
			}
			var v any
			_ = json.Unmarshal(out.Output, &v)
			res[tl.Spec().Name] = v
		}
		b, _ := json.Marshal(res)
		ch <- &nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: string(b), IsFinal: true}}}
		// no terminal event: the node must synthesise one
	}()
	return fakeRun{ch}, nil
}

type fakeRun struct{ ch chan *nexusv1.Event }

func (r fakeRun) Events() <-chan *nexusv1.Event                            { return r.ch }
func (fakeRun) Send(context.Context, *nexusv1.RunInput) error              { return nil }
func (fakeRun) Resolve(context.Context, *nexusv1.ApprovalResolution) error { return nil }
func (fakeRun) Cancel(context.Context) error                               { return nil }

func TestHubAndLocalTools(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	// Wait for the hub tool list to be fetched.
	deadline := time.Now().Add(5 * time.Second)
	for len(e.daemon.HubTools()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(e.daemon.HubTools()) == 0 {
		t.Fatal("hub tools not fetched")
	}
	a := &nexusv1.RunAssignment{RunId: "run_t", TaskId: "tsk_t",
		Agent: &nexusv1.Agent{Id: "toolie", Runtime: "tools", Tools: []string{"test.ping", "fs.list", "memory.nope"}}}
	if _, err := e.disp.Assign(ctx, kernel.NodeSelector{Runtime: "tools"}, a); err != nil {
		t.Fatal(err)
	}
	out := e.next(t, func(ev *nexusv1.Event) bool { return ev.GetRunOutput() != nil }).GetRunOutput().Text
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	ping, _ := res["test.ping"].(map[string]any)
	if ping["pong"] != "run_t" || ping["node"] != "n1" {
		t.Fatalf("hub tool result %v", res)
	}
	if _, ok := res["fs.list"].(map[string]any); !ok {
		t.Fatalf("fs.list result %v", res["fs.list"])
	}
	if _, ok := res["memory.nope"]; ok {
		t.Fatalf("unknown hub tool should not be offered: %v", res)
	}
	fin := e.next(t, func(ev *nexusv1.Event) bool {
		return ev.GetTaskUpdated() != nil && ev.GetTaskUpdated().Task.State != nexusv1.TaskState_TASK_STATE_RUNNING
	})
	if fin.GetTaskUpdated().Task.State != nexusv1.TaskState_TASK_STATE_FAILED {
		t.Fatalf("expected synthesised FAILED, got %v", fin)
	}
}

func TestNodeServiceOverHTTP2(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ws := e.daemon.Config().WorkspacesDir
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := nexusv1connect.NewNodeServiceClient(e.srv.Client(), e.srv.URL)
	ln, err := c.ListNodes(ctx, connect.NewRequest(&nexusv1.ListNodesRequest{}))
	if err != nil || len(ln.Msg.Nodes) != 1 {
		t.Fatalf("ListNodes: %v %v", err, ln)
	}
	if _, err := c.GetNode(ctx, connect.NewRequest(&nexusv1.GetNodeRequest{Id: "zz"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetNode unknown: %v", err)
	}
	lf, err := c.ListFiles(ctx, connect.NewRequest(&nexusv1.ListFilesRequest{NodeId: "n1"}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range lf.Msg.Entries {
		if f.Name == "hello.txt" && f.SizeBytes == 2 && f.ModifiedAt != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("hello.txt not listed: %v", lf.Msg.Entries)
	}
	if _, err := c.ListFiles(ctx, connect.NewRequest(&nexusv1.ListFilesRequest{NodeId: "n1", Path: "../.."})); err == nil {
		t.Fatal("escape should fail")
	}
	if _, err := c.WakeNode(ctx, connect.NewRequest(&nexusv1.WakeNodeRequest{Id: "n1"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("WakeNode: %v", err)
	}
}

func TestRejectsBadToken(t *testing.T) {
	e := setup(t)
	c := nexusv1connect.NewNodeLinkServiceClient(e.srv.Client(), e.srv.URL)
	s := c.Connect(context.Background())
	s.RequestHeader().Set("Authorization", "Bearer wrong")
	s.RequestHeader().Set("X-Nexus-Node-Id", "n1")
	_ = s.Send(&nexusv1.ConnectRequest{Msg: &nexusv1.ConnectRequest_Hello{Hello: &nexusv1.NodeHello{NodeId: "n1"}}})
	_, err := s.Receive()
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want unauthenticated, got %v", err)
	}
	_ = s.CloseRequest()
	_ = s.CloseResponse()
	_ = http.StatusOK
}
