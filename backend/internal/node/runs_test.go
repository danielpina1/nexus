package node

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// heldRuntime starts runs that stay open until released (or cancelled) and
// records the RunSpec of each run.
type heldRuntime struct {
	name  string
	limit int

	mu      sync.Mutex
	specs   map[string]kernel.RunSpec
	runs    map[string]*heldRun
	started []string
}

func newHeld(name string, limit int) *heldRuntime {
	return &heldRuntime{name: name, limit: limit, specs: map[string]kernel.RunSpec{}, runs: map[string]*heldRun{}}
}

func (r *heldRuntime) Name() string                             { return r.name }
func (r *heldRuntime) Capabilities() kernel.RuntimeCapabilities { return kernel.RuntimeCapabilities{} }
func (r *heldRuntime) Available(context.Context) (string, bool) { return "1", true }
func (r *heldRuntime) MaxConcurrent() int                       { return r.limit }
func (r *heldRuntime) Start(_ context.Context, spec kernel.RunSpec) (kernel.Run, error) {
	run := &heldRun{ch: make(chan *nexusv1.Event, 4), a: spec.Assignment}
	r.mu.Lock()
	r.specs[spec.Assignment.RunId] = spec
	r.runs[spec.Assignment.RunId] = run
	r.started = append(r.started, spec.Assignment.RunId)
	r.mu.Unlock()
	return run, nil
}

func (r *heldRuntime) run(id string) *heldRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

func (r *heldRuntime) spec(id string) (kernel.RunSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.specs[id]
	return s, ok
}

func (r *heldRuntime) startedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

type heldRun struct {
	ch   chan *nexusv1.Event
	a    *nexusv1.RunAssignment
	once sync.Once
}

func (r *heldRun) finish(st nexusv1.TaskState) {
	r.once.Do(func() {
		r.ch <- &nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{State: st}}}}
		close(r.ch)
	})
}
func (r *heldRun) Events() <-chan *nexusv1.Event                              { return r.ch }
func (r *heldRun) Send(context.Context, *nexusv1.RunInput) error              { return nil }
func (r *heldRun) Resolve(context.Context, *nexusv1.ApprovalResolution) error { return nil }
func (r *heldRun) Cancel(context.Context) error {
	r.finish(nexusv1.TaskState_TASK_STATE_CANCELLED)
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assignment(id, runtime string, tools ...string) *nexusv1.RunAssignment {
	return &nexusv1.RunAssignment{RunId: id, TaskId: "t-" + id, Instructions: "Be useful.", Agent: &nexusv1.Agent{Id: "ag", Runtime: runtime, Tools: tools}}
}

func TestRunManagerConcurrencyQueue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int            // runtime's own MaxConcurrent
		override map[string]int // node config
		want     int            // runs allowed at once
	}{
		{"runtime default", 2, nil, 2},
		{"node override", 2, map[string]int{"claude-code": 1}, 1},
		{"unlimited", 0, nil, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSink()
			rt := newHeld("claude-code", tc.limit)
			reg := kernel.NewRuntimeRegistry()
			_ = reg.Register(rt)
			m := NewRunManager(RunManagerConfig{NodeID: "n1", WorkspacesDir: t.TempDir(), Runtimes: reg,
				Emit: s.emit, MaxConcurrent: tc.override})
			t.Cleanup(func() { m.Close(context.Background()) })

			for _, id := range []string{"r1", "r2", "r3"} {
				m.Assign(assignment(id, "claude-code"))
			}
			waitFor(t, func() bool { return len(rt.startedIDs()) == tc.want })
			if q := m.Queued(); q != 3-tc.want {
				t.Fatalf("queued = %d, want %d", q, 3-tc.want)
			}
			if tc.want == 3 {
				return
			}
			// The queued run reports that it is waiting.
			out := s.wait(t, func(ev *nexusv1.Event) bool { return ev.GetRunOutput() != nil })
			if !strings.Contains(out.GetRunOutput().Text, "Waiting for a free Claude Code slot") || out.RunId != "r3" && out.RunId != "r2" {
				t.Fatalf("waiting output %v", out)
			}
			// Finishing a run starts the next queued one (FIFO).
			rt.run("r1").finish(nexusv1.TaskState_TASK_STATE_SUCCEEDED)
			waitFor(t, func() bool { return len(rt.startedIDs()) == tc.want+1 })
			if got := rt.startedIDs(); got[len(got)-1] != []string{"r1", "r2", "r3"}[tc.want] {
				t.Fatalf("start order %v", got)
			}
		})
	}
}

func TestRunManagerCancelQueuedRun(t *testing.T) {
	s := newSink()
	rt := newHeld("codex", 1)
	reg := kernel.NewRuntimeRegistry()
	_ = reg.Register(rt)
	m := NewRunManager(RunManagerConfig{NodeID: "n1", WorkspacesDir: t.TempDir(), Runtimes: reg, Emit: s.emit})
	t.Cleanup(func() { m.Close(context.Background()) })
	m.Assign(assignment("r1", "codex"))
	m.Assign(assignment("r2", "codex"))
	waitFor(t, func() bool { return m.Queued() == 1 })
	if err := m.Cancel(context.Background(), &nexusv1.RunCancel{RunId: "r2"}); err != nil {
		t.Fatal(err)
	}
	s.wait(t, func(ev *nexusv1.Event) bool {
		return ev.RunId == "r2" && state(nexusv1.TaskState_TASK_STATE_CANCELLED)(ev)
	})
	rt.run("r1").finish(nexusv1.TaskState_TASK_STATE_SUCCEEDED)
	waitFor(t, func() bool { return m.Active() == 0 })
	if got := rt.startedIDs(); len(got) != 1 {
		t.Fatalf("cancelled queued run was started: %v", got)
	}
}

// fakeProvider is a RunToolProvider returning browser-like tools and a
// wrapper of shops.get.
type fakeProvider struct {
	cleaned atomic.Int32
	gate    atomic.Pointer[kernel.RunGate]
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) RunTools(_ context.Context, spec kernel.RunSpec) (kernel.RunToolSet, error) {
	p.gate.Store(spec.Gate)
	var shops kernel.Tool
	for _, t := range spec.Tools {
		if t.Spec().Name == "shops.get" {
			shops = t
		}
	}
	mk := func(name string) kernel.Tool {
		return kernel.ToolFunc{S: kernel.ToolSpec{Name: name}, F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]string{"tool": name, "run": c.RunID})
		}}
	}
	tools := []kernel.Tool{mk("browser.navigate"), mk("browser.click"), mk("other.thing")}
	if shops != nil {
		tools = append(tools, kernel.ToolFunc{S: shops.Spec(), F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			res, err := shops.Invoke(ctx, c)
			if err == nil {
				res.Output = json.RawMessage(`{"wrapped":` + string(res.Output) + `}`)
			}
			return res, err
		}})
	}
	return kernel.RunToolSet{Tools: tools, Instructions: "## Browser guidance", Cleanup: func() { p.cleaned.Add(1) }}, nil
}

type shopsHub struct{}

func (shopsHub) CallHubTool(_ context.Context, runID, tool string, _ []byte) ([]byte, error) {
	return json.Marshal(map[string]string{"run": runID, "tool": tool})
}
func (shopsHub) HubTools() []kernel.ToolSpec { return nil } // unknown list: placeholders

func TestRunManagerProviderTools(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		allow   []string
		want    []string
	}{
		{"cli agent with browser", "claude-code", []string{"browser", "shops.get", "memory.search"},
			[]string{"browser.click", "browser.navigate", "memory.search", "shops.get"}},
		{"single browser tool", "codex", []string{"browser.navigate"}, []string{"browser.navigate"}},
		{"group wildcard", "ollama", []string{"browser.*"}, []string{"browser.click", "browser.navigate"}},
		{"not allowed", "claude-code", []string{"memory.search"}, []string{"memory.search"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSink()
			rt := newHeld(tc.runtime, 0)
			reg := kernel.NewRuntimeRegistry()
			_ = reg.Register(rt)
			p := &fakeProvider{}
			m := NewRunManager(RunManagerConfig{NodeID: "n1", WorkspacesDir: t.TempDir(), Runtimes: reg,
				Tools: kernel.NewToolRegistry(), Hub: shopsHub{}, Emit: s.emit,
				Providers: func() []kernel.RunToolProvider { return []kernel.RunToolProvider{p} }})
			t.Cleanup(func() { m.Close(context.Background()) })
			m.Assign(assignment("r1", tc.runtime, tc.allow...))
			waitFor(t, func() bool { _, ok := rt.spec("r1"); return ok })
			spec, _ := rt.spec("r1")
			var names []string
			byName := map[string]kernel.Tool{}
			for _, tl := range spec.Tools {
				names = append(names, tl.Spec().Name)
				byName[tl.Spec().Name] = tl
			}
			if got := strings.Join(sorted(names), ","); got != strings.Join(tc.want, ",") {
				t.Fatalf("tools = %s, want %s", got, strings.Join(tc.want, ","))
			}
			if spec.Gate == nil || p.gate.Load() != spec.Gate || spec.Emit == nil {
				t.Fatal("spec gate/emit not set or not shared with providers")
			}
			// Guidance is appended only when provider tools were added.
			hasGuide := strings.Contains(spec.Assignment.GetInstructions(), "## Browser guidance")
			if hasGuide != (len(tc.want) > 1 || tc.want[0] != "memory.search") {
				t.Fatalf("instructions = %q", spec.Assignment.GetInstructions())
			}
			// Provider tools are bound to the run.
			if tl := byName["browser.navigate"]; tl != nil {
				res, err := tl.Invoke(context.Background(), kernel.ToolCall{RunID: "spoofed"})
				if err != nil || !strings.Contains(string(res.Output), `"run":"r1"`) {
					t.Fatalf("navigate %s %v", res.Output, err)
				}
			}
			if tl := byName["shops.get"]; tl != nil {
				res, err := tl.Invoke(context.Background(), kernel.ToolCall{})
				if err != nil || !strings.HasPrefix(string(res.Output), `{"wrapped":{"run":"r1"`) {
					t.Fatalf("shops.get not replaced by the provider wrapper: %s %v", res.Output, err)
				}
			}
			rt.run("r1").finish(nexusv1.TaskState_TASK_STATE_SUCCEEDED)
			waitFor(t, func() bool { return p.cleaned.Load() == 1 })
		})
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestToolAllowed(t *testing.T) {
	for _, tc := range []struct {
		allow []string
		name  string
		want  bool
	}{
		{[]string{"browser"}, "browser.click", true},
		{[]string{"browser.*"}, "browser.click", true},
		{[]string{"browser.click"}, "browser.click", true},
		{[]string{"browser.click"}, "browser.navigate", false},
		{[]string{"memory.search"}, "memory.search.extra", false},
		{[]string{"web"}, "web.search", true},
		{[]string{"browsers"}, "browser.click", false},
		{nil, "browser.click", false},
	} {
		if got := kernel.ToolAllowed(tc.allow, tc.name); got != tc.want {
			t.Errorf("ToolAllowed(%v, %q) = %v", tc.allow, tc.name, got)
		}
	}
}
