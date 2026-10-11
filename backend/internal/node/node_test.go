package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/echo"
	"github.com/danielpina1/nexus/backend/kernel"
)

func TestBackoff(t *testing.T) {
	base, max := 100*time.Millisecond, 2*time.Second
	tests := []struct {
		n        int
		rnd      float64
		min, top time.Duration
	}{
		{0, 0, 50 * time.Millisecond, 50 * time.Millisecond},
		{0, 1, 100 * time.Millisecond, 100 * time.Millisecond},
		{3, 0.5, 600 * time.Millisecond, 600 * time.Millisecond},
		{10, 1, max, max},
		{10, 0, max / 2, max / 2},
	}
	for _, tc := range tests {
		got := Backoff(tc.n, base, max, func() float64 { return tc.rnd })
		if got < tc.min || got > tc.top {
			t.Errorf("Backoff(%d, %v) = %v, want [%v, %v]", tc.n, tc.rnd, got, tc.min, tc.top)
		}
	}
}

func TestConfine(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o700)
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, "link"))
	tests := []struct {
		rel string
		ok  bool
	}{
		{"", true}, {".", true}, {"sub", true}, {"sub/../sub", true}, {"new/file", true},
		{"..", false}, {"../x", false}, {"sub/../../x", false}, {"link", false}, {"link/f", false},
		{filepath.Join(root, "sub"), true}, {"/etc/passwd", false},
	}
	for _, tc := range tests {
		_, err := Confine(root, tc.rel)
		if (err == nil) != tc.ok {
			t.Errorf("Confine(%q): err=%v, want ok=%v", tc.rel, err, tc.ok)
		}
	}
}

func invoke(t *testing.T, tools []kernel.Tool, name, input string) (map[string]any, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Spec().Name == name {
			res, err := tl.Invoke(context.Background(), kernel.ToolCall{Input: json.RawMessage(input)})
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal(res.Output, &m); err != nil {
				t.Fatal(err)
			}
			return m, nil
		}
	}
	t.Fatalf("no tool %s", name)
	return nil, nil
}

func TestLocalTools(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world"), 0o600)
	tools := LocalTools(func(kernel.ToolCall) (string, error) { return root, nil })

	if specs := map[string]nexusv1.ActionClass{}; true {
		for _, tl := range tools {
			specs[tl.Spec().Name] = tl.Spec().ActionClass
		}
		if specs["shell.exec"] != nexusv1.ActionClass_ACTION_CLASS_COMPUTE || specs["fs.read"] != nexusv1.ActionClass_ACTION_CLASS_READ {
			t.Fatalf("action classes %v", specs)
		}
	}
	ls, err := invoke(t, tools, "fs.list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if es := ls["entries"].([]any); len(es) != 1 || es[0].(map[string]any)["name"] != "a.txt" {
		t.Fatalf("fs.list %v", ls)
	}
	rd, err := invoke(t, tools, "fs.read", `{"path":"a.txt","max_bytes":5}`)
	if err != nil || rd["content"] != "hello" || rd["truncated"] != true {
		t.Fatalf("fs.read %v %v", rd, err)
	}
	if _, err := invoke(t, tools, "fs.read", `{"path":"../x"}`); err == nil {
		t.Fatal("fs.read escape allowed")
	}
	sh, err := invoke(t, tools, "shell.exec", `{"command":"pwd; echo oops >&2; exit 3"}`)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(root)
	if sh["exit_code"].(float64) != 3 || sh["stderr"] != "oops\n" || filepath.Clean(sh["stdout"].(string)[:len(sh["stdout"].(string))-1]) != real {
		t.Fatalf("shell.exec %v", sh)
	}
	to, err := invoke(t, tools, "shell.exec", `{"command":"sleep 5","timeout_seconds":1}`)
	if err != nil || to["timed_out"] != true {
		t.Fatalf("shell.exec timeout %v %v", to, err)
	}
	big, _ := invoke(t, tools, "shell.exec", `{"command":"head -c 400000 /dev/zero"}`)
	if big["truncated"] != true || len(big["stdout"].(string)) != ShellOutputCap {
		t.Fatalf("output cap not applied: truncated=%v", big["truncated"])
	}
	if _, err := invoke(t, tools, "shell.exec", `{"command":"true","workdir":".."}`); err == nil {
		t.Fatal("shell.exec workdir escape allowed")
	}
}

// --- RunManager ---

type sink struct {
	mu  sync.Mutex
	evs []*nexusv1.Event
	ch  chan *nexusv1.Event
}

func newSink() *sink { return &sink{ch: make(chan *nexusv1.Event, 256)} }

func (s *sink) emit(ev *nexusv1.Event) { s.ch <- ev }

func (s *sink) wait(t *testing.T, pred func(*nexusv1.Event) bool) *nexusv1.Event {
	t.Helper()
	to := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.ch:
			if pred(ev) {
				return ev
			}
		case <-to:
			t.Fatal("timeout waiting for event")
		}
	}
}

func state(st nexusv1.TaskState) func(*nexusv1.Event) bool {
	return func(ev *nexusv1.Event) bool { return ev.GetTaskUpdated().GetTask().GetState() == st }
}

type verifierFunc func(context.Context, *nexusv1.ApprovalResolution) error

func (f verifierFunc) VerifyApproval(ctx context.Context, r *nexusv1.ApprovalResolution) error {
	return f(ctx, r)
}

func newManager(t *testing.T, s *sink, v ApprovalVerifier) *RunManager {
	reg := kernel.NewRuntimeRegistry()
	_ = reg.Register(&echo.Runtime{})
	_ = reg.Register(silentRuntime{})
	m := NewRunManager(RunManagerConfig{
		NodeID: "n1", WorkspacesDir: t.TempDir(), Runtimes: reg, Tools: kernel.NewToolRegistry(),
		Emit: s.emit, Verifier: func() ApprovalVerifier { return v },
	})
	t.Cleanup(func() { m.Close(context.Background()) })
	return m
}

func TestRunManagerApprovalAndVerification(t *testing.T) {
	s := newSink()
	var reject bool
	var mu sync.Mutex
	v := verifierFunc(func(context.Context, *nexusv1.ApprovalResolution) error {
		mu.Lock()
		defer mu.Unlock()
		if reject {
			return errors.New("bad signature")
		}
		return nil
	})
	m := newManager(t, s, v)
	a := &nexusv1.RunAssignment{RunId: "run/../1", TaskId: "t1", Prompt: "go ?approve", Agent: &nexusv1.Agent{Id: "ag", Runtime: "echo"}}
	m.Assign(a)
	m.Assign(a) // duplicate (resend) ignored
	if wd, ok := m.Workdir(a.RunId); !ok || filepath.Dir(wd) != m.WorkspacesDir {
		t.Fatalf("workdir %q not confined", wd)
	}
	run := s.wait(t, state(nexusv1.TaskState_TASK_STATE_RUNNING))
	if run.Actor != "agent:ag" || run.GetTaskUpdated().Task.NodeId != "n1" {
		t.Fatalf("running event %v", run)
	}
	apr := s.wait(t, func(ev *nexusv1.Event) bool { return ev.GetApproval() != nil }).GetApproval()
	s.wait(t, state(nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL))

	mu.Lock()
	reject = true
	mu.Unlock()
	res := &nexusv1.ApprovalResolution{RunId: a.RunId, Approval: apr, Decision: &nexusv1.SignedApproval{ApprovalId: apr.Id, Approve: true}}
	if err := m.Resolve(context.Background(), res); !errors.Is(err, ErrApprovalRejected) {
		t.Fatalf("want rejection, got %v", err)
	}
	alert := s.wait(t, func(ev *nexusv1.Event) bool { return ev.GetNotice() != nil })
	if alert.GetNotice().Kind != nexusv1.NoticeKind_NOTICE_KIND_ALERT {
		t.Fatalf("alert %v", alert)
	}
	mu.Lock()
	reject = false
	mu.Unlock()
	if err := m.Resolve(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	s.wait(t, state(nexusv1.TaskState_TASK_STATE_SUCCEEDED))
	deadline := time.Now().Add(time.Second)
	for m.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.Active() != 0 {
		t.Fatal("run not removed")
	}
}

func TestRunManagerFailuresAndCancel(t *testing.T) {
	s := newSink()
	m := newManager(t, s, DevApprovalVerifier{})

	m.Assign(&nexusv1.RunAssignment{RunId: "r1", TaskId: "t1", Agent: &nexusv1.Agent{Runtime: "nope"}})
	ev := s.wait(t, state(nexusv1.TaskState_TASK_STATE_FAILED))
	if ev.RunId != "r1" || ev.GetTaskUpdated().Task.Summary == "" {
		t.Fatalf("failure event %v", ev)
	}

	// A runtime that ends without a terminal event gets one synthesised.
	m.Assign(&nexusv1.RunAssignment{RunId: "r2", TaskId: "t2", Agent: &nexusv1.Agent{Runtime: "silent"}})
	s.wait(t, func(ev *nexusv1.Event) bool {
		return ev.RunId == "r2" && state(nexusv1.TaskState_TASK_STATE_FAILED)(ev)
	})

	// Cancel -> CANCELLED.
	m.Assign(&nexusv1.RunAssignment{RunId: "r3", TaskId: "t3", Prompt: "?ask", Agent: &nexusv1.Agent{Runtime: "echo"}})
	s.wait(t, state(nexusv1.TaskState_TASK_STATE_WAITING_INPUT))
	if err := m.Cancel(context.Background(), &nexusv1.RunCancel{RunId: "r3"}); err != nil {
		t.Fatal(err)
	}
	s.wait(t, state(nexusv1.TaskState_TASK_STATE_CANCELLED))

	// Max runtime cancels the run.
	m.Assign(&nexusv1.RunAssignment{RunId: "r4", TaskId: "t4", Prompt: "?ask",
		Agent: &nexusv1.Agent{Runtime: "echo", Limits: &nexusv1.AgentLimits{MaxRuntimeSeconds: 1}}})
	s.wait(t, func(ev *nexusv1.Event) bool {
		return ev.RunId == "r4" && state(nexusv1.TaskState_TASK_STATE_CANCELLED)(ev)
	})

	if err := m.Input(context.Background(), &nexusv1.RunInput{RunId: "zzz"}); err == nil {
		t.Fatal("input to unknown run should fail")
	}
}

type fakeHub struct{}

func (fakeHub) CallHubTool(_ context.Context, runID, tool string, _ []byte) ([]byte, error) {
	return json.Marshal(map[string]string{"run": runID, "tool": tool})
}
func (fakeHub) HubTools() []kernel.ToolSpec {
	return []kernel.ToolSpec{{Name: "memory.search"}, {Name: "vault.write"}}
}

func TestRunToolsAllowListAndBinding(t *testing.T) {
	reg := kernel.NewToolRegistry()
	for _, tl := range LocalTools(func(kernel.ToolCall) (string, error) { return t.TempDir(), nil }) {
		_ = reg.Register(tl)
	}
	m := NewRunManager(RunManagerConfig{NodeID: "n1", Tools: reg, Hub: fakeHub{}, Emit: func(*nexusv1.Event) {}})
	a := &nexusv1.RunAssignment{RunId: "r", TaskId: "t", Agent: &nexusv1.Agent{Id: "ag", Tools: []string{"fs.list", "memory.search", "unknown.tool"}}}
	tools := m.runTools(a)
	names := map[string]kernel.Tool{}
	for _, tl := range tools {
		names[tl.Spec().Name] = tl
	}
	if len(names) != 2 || names["fs.list"] == nil || names["memory.search"] == nil {
		t.Fatalf("tools %v", names)
	}
	res, err := names["memory.search"].Invoke(context.Background(), kernel.ToolCall{RunID: "spoofed"})
	if err != nil || string(res.Output) != `{"run":"r","tool":"memory.search"}` {
		t.Fatalf("hub tool %s %v", res.Output, err)
	}
	if got := m.runTools(&nexusv1.RunAssignment{RunId: "x", Agent: &nexusv1.Agent{}}); len(got) != 0 {
		t.Fatal("empty allow-list must yield no tools")
	}
}

type silentRuntime struct{}

func (silentRuntime) Name() string                             { return "silent" }
func (silentRuntime) Capabilities() kernel.RuntimeCapabilities { return kernel.RuntimeCapabilities{} }
func (silentRuntime) Available(context.Context) (string, bool) { return "", true }
func (silentRuntime) Start(context.Context, kernel.RunSpec) (kernel.Run, error) {
	ch := make(chan *nexusv1.Event)
	close(ch)
	return silentRun{ch}, nil
}

type silentRun struct{ ch chan *nexusv1.Event }

func (r silentRun) Events() <-chan *nexusv1.Event                            { return r.ch }
func (silentRun) Send(context.Context, *nexusv1.RunInput) error              { return nil }
func (silentRun) Resolve(context.Context, *nexusv1.ApprovalResolution) error { return nil }
func (silentRun) Cancel(context.Context) error                               { return nil }
