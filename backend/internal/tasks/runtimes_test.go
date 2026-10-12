package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// rtDispatcher is a Dispatcher whose nodes offer a fixed set of runtimes.
type rtDispatcher struct {
	taskstest.Dispatcher
	mu    sync.Mutex
	avail map[string]bool
	tried []string
}

func (d *rtDispatcher) Assign(ctx context.Context, sel kernel.NodeSelector, a *nexusv1.RunAssignment) (string, error) {
	d.mu.Lock()
	d.tried = append(d.tried, sel.Runtime)
	ok := d.avail[sel.Runtime]
	d.mu.Unlock()
	if !ok {
		return "", errors.New("nodelink: no connected node matches the selector")
	}
	if a.GetAgent().GetRuntime() != sel.Runtime {
		return "", errors.New("assignment agent runtime does not match the selector")
	}
	return d.Dispatcher.Assign(ctx, sel, a)
}

// rootVault is a kernel.Vault that only knows its root.
type rootVault struct{ root string }

func (rootVault) Read(context.Context, string) (*nexusv1.Note, error) { return nil, errors.New("n/a") }
func (rootVault) Write(context.Context, string, string, string) (*nexusv1.Note, error) {
	return nil, errors.New("n/a")
}
func (rootVault) Search(context.Context, string, []string, int) ([]*nexusv1.SearchHit, error) {
	return nil, nil
}
func (rootVault) Graph(context.Context, *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error) {
	return nil, nil
}
func (rootVault) Capture(context.Context, *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error) {
	return nil, nil
}
func (v rootVault) Root() string { return v.root }

type rtFixture struct {
	e    *Engine
	disp *rtDispatcher
	cat  *mdCatalog
	root string
}

func newRTFixture(t *testing.T, avail ...string) *rtFixture {
	t.Helper()
	f := &rtFixture{disp: &rtDispatcher{avail: map[string]bool{}}, root: t.TempDir()}
	for _, a := range avail {
		f.disp.avail[a] = true
	}
	f.cat = &mdCatalog{Catalog: taskstest.NewCatalog(), md: map[string]string{}}
	env := taskstest.New(t)
	env.Host.Provide(kernel.SvcDispatcher, f.disp)
	env.Host.Provide(kernel.SvcAgents, f.cat)
	env.Host.Provide(kernel.SvcVault, rootVault{f.root})
	m := &Module{}
	if err := m.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	f.e = m.Engine
	return f
}

func (f *rtFixture) agent(id, runtime string, tier nexusv1.Tier, frontmatter string) {
	f.cat.Add(&nexusv1.Agent{Id: id, Runtime: runtime, Tier: tier, Enabled: true, NodeTags: []string{"any"}}, "# "+id)
	f.cat.md[id] = "---\ntype: agent\nid: " + id + "\nruntime: " + runtime + "\n" + frontmatter + "---\n# " + id + "\n"
}

func (f *rtFixture) policy(t *testing.T, yaml string) {
	t.Helper()
	p := filepath.Join(f.root, "policies", "models.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ntype: policy\nid: models\n"+yaml+"---\n# Models\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *rtFixture) last() *nexusv1.RunAssignment { return f.disp.LastAssignment() }

func TestRuntimeCandidates(t *testing.T) {
	prefs := map[string][]string{"simple": {"ollama", "codex"}, "medium": {"claude-code", "codex", "ollama"}}
	for _, tc := range []struct {
		name, runtime, md string
		tier              nexusv1.Tier
		want              []string
	}{
		{"auto medium", "auto", "", nexusv1.Tier_TIER_MEDIUM, []string{"claude-code", "codex", "ollama"}},
		{"auto simple", "auto", "", nexusv1.Tier_TIER_SIMPLE, []string{"ollama", "codex"}},
		{"auto unspecified tier = medium", "auto", "", nexusv1.Tier_TIER_UNSPECIFIED, []string{"claude-code", "codex", "ollama"}},
		{"auto heavy falls back to built-in", "auto", "", nexusv1.Tier_TIER_HEAVY, []string{"claude-code", "codex", "ollama"}},
		{"explicit runtime", "openai", "", nexusv1.Tier_TIER_MEDIUM, []string{"openai"}},
		{"agent list wins", "claude-code", "---\nruntimes: [claude-code, codex, auto, codex]\n---\n", nexusv1.Tier_TIER_SIMPLE, []string{"claude-code", "codex"}},
		{"comma list", "auto", "---\nruntimes: \"codex, ollama\"\n---\n", nexusv1.Tier_TIER_MEDIUM, []string{"codex", "ollama"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RuntimeCandidates(&nexusv1.Agent{Runtime: tc.runtime}, tc.md, tc.tier, prefs)
			if tc.name == "auto heavy falls back to built-in" {
				tc.want = DefaultRuntimePrefs["medium"] // no heavy entry in prefs → medium default
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	got := ParseRuntimePrefs([]byte("---\nruntimes:\n  simple: [ollama]\n  Medium: [codex, claude-code]\n  bad: 3\n---\n"))
	if !reflect.DeepEqual(got, map[string][]string{"simple": {"ollama"}, "medium": {"codex", "claude-code"}}) {
		t.Fatalf("prefs %v", got)
	}
	if ParseRuntimePrefs([]byte("no frontmatter")) != nil {
		t.Fatal("prefs from a note without runtimes")
	}
}

func TestRuntimeAutoSelection(t *testing.T) {
	ctx := context.Background()
	t.Run("first available of the tier list", func(t *testing.T) {
		f := newRTFixture(t, "codex", "ollama")
		f.agent("assistant", "auto", nexusv1.Tier_TIER_MEDIUM, "claude: {model: sonnet}\n")
		task, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "assistant"})
		if err != nil || task.State != stAssigned {
			t.Fatalf("task %v %v", task, err)
		}
		if got := f.last().GetAgent().GetRuntime(); got != "codex" {
			t.Fatalf("runtime %q (tried %v)", got, f.disp.tried)
		}
		if !reflect.DeepEqual(f.disp.tried, []string{"claude-code", "codex"}) {
			t.Fatalf("tried %v", f.disp.tried)
		}
		run, _ := f.e.loadRun(ctx, task.CurrentRunId)
		if run.runtime != "codex" {
			t.Fatalf("run runtime %q", run.runtime)
		}
		// The catalog's agent is untouched (copy in the assignment).
		if a, _, _, _ := f.cat.GetAgent(ctx, "assistant"); a.Runtime != "auto" {
			t.Fatalf("catalog agent mutated: %v", a.Runtime)
		}
		// claude/codex blocks are forwarded.
		if c, _ := f.last().GetContext().AsMap()["claude"].(map[string]any); c["model"] != "sonnet" {
			t.Fatalf("claude settings not forwarded: %v", f.last().GetContext().AsMap())
		}
	})
	t.Run("policy note overrides defaults", func(t *testing.T) {
		f := newRTFixture(t, "claude-code", "codex", "ollama")
		f.policy(t, "runtimes:\n  medium: [ollama, codex]\n")
		f.agent("assistant", "auto", nexusv1.Tier_TIER_MEDIUM, "")
		if _, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "assistant"}); err != nil {
			t.Fatal(err)
		}
		if got := f.last().GetAgent().GetRuntime(); got != "ollama" {
			t.Fatalf("runtime %q", got)
		}
		// Edited policy is picked up (mtime cache).
		f.policy(t, "runtimes:\n  medium: [codex]\n")
		future := f.e.now().Add(1e9 * 10)
		_ = os.Chtimes(filepath.Join(f.root, "policies", "models.md"), future, future)
		if _, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "assistant"}); err != nil {
			t.Fatal(err)
		}
		if got := f.last().GetAgent().GetRuntime(); got != "codex" {
			t.Fatalf("runtime after policy edit %q", got)
		}
	})
	t.Run("agent runtimes list", func(t *testing.T) {
		f := newRTFixture(t, "claude-code", "codex")
		f.agent("code-fixer", "claude-code", nexusv1.Tier_TIER_HEAVY, "runtimes: [codex, claude-code]\n")
		if _, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "fix", AgentId: "code-fixer"}); err != nil {
			t.Fatal(err)
		}
		if got := f.last().GetAgent().GetRuntime(); got != "codex" {
			t.Fatalf("runtime %q", got)
		}
	})
	t.Run("nothing available", func(t *testing.T) {
		f := newRTFixture(t, "shell")
		f.agent("assistant", "auto", nexusv1.Tier_TIER_HEAVY, "")
		task, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "hi", AgentId: "assistant"})
		if err != nil {
			t.Fatal(err)
		}
		if task.State != stFailed || !strings.Contains(task.Summary, "no node available") || !strings.Contains(task.Summary, "claude-code → codex") {
			t.Fatalf("task %v: %s", task.State, task.Summary)
		}
	})
}

func failedEvent(kind, summary string) *nexusv1.Event {
	res, _ := structpb.NewStruct(map[string]any{"error": summary, "error_kind": kind})
	return &nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: &nexusv1.Task{
		State: stFailed, Summary: summary, Result: res,
	}}}}
}

func TestRateLimitFallsBackToNextRuntime(t *testing.T) {
	ctx := context.Background()
	f := newRTFixture(t, "claude-code", "codex", "ollama")
	f.agent("researcher", "auto", nexusv1.Tier_TIER_MEDIUM, "")
	task, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "research X", AgentId: "researcher"})
	if err != nil || f.last().GetAgent().GetRuntime() != "claude-code" {
		t.Fatalf("first run: %v %v", task, err)
	}
	send := func(ev *nexusv1.Event) {
		t.Helper()
		cur, _ := f.e.GetTask(ctx, task.Id)
		ev.RunId = cur.CurrentRunId
		if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: ev}); err != nil {
			t.Fatal(err)
		}
	}
	firstRun := task.CurrentRunId
	send(&nexusv1.Event{Body: &nexusv1.Event_RunOutput{RunOutput: &nexusv1.RunOutput{Text: "searching…"}}})
	send(failedEvent(kernel.ErrorKindRateLimited, "Claude usage limit reached; resets 3pm."))

	cur, _ := f.e.GetTask(ctx, task.Id)
	if cur.State != stAssigned || cur.CurrentRunId == firstRun || f.last().GetAgent().GetRuntime() != "codex" {
		t.Fatalf("after rate limit: state %v run %s runtime %s", cur.State, cur.CurrentRunId, f.last().GetAgent().GetRuntime())
	}
	c := f.last().GetContext().AsMap()
	prev, _ := c["previous_attempt"].(map[string]any)
	if prev["run_id"] != firstRun || !strings.Contains(prev["summary"].(string), "usage limit") ||
		!strings.Contains(c["escalation_hint"].(string), "Claude Code") {
		t.Fatalf("context %v", c)
	}
	if r, _ := f.e.loadRun(ctx, firstRun); r.state != runFailed {
		t.Fatalf("first run state %s", r.state)
	}
	// Auth failure on codex → ollama.
	send(failedEvent(kernel.ErrorKindAuth, "Codex is not logged in on node mac."))
	if f.last().GetAgent().GetRuntime() != "ollama" {
		t.Fatalf("after auth failure: %s", f.last().GetAgent().GetRuntime())
	}
	// Last runtime rate-limited → the task fails.
	send(failedEvent(kernel.ErrorKindRateLimited, "Ollama overloaded."))
	cur, _ = f.e.GetTask(ctx, task.Id)
	if cur.State != stFailed || cur.Summary != "Ollama overloaded." {
		t.Fatalf("final %v %q", cur.State, cur.Summary)
	}
	if got := len(f.disp.Assigned); got != 3 {
		t.Fatalf("assignments %d", got)
	}
}

func TestRateLimitNoFallback(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, runtime, kind string
		avail               []string
		wantSummary         string
	}{
		{"single runtime agent", "claude-code", kernel.ErrorKindRateLimited, []string{"claude-code", "codex"}, "Claude usage limit reached."},
		{"other failure kinds", "auto", "", []string{"claude-code", "codex"}, "Claude usage limit reached."},
		{"next runtime offline", "auto", kernel.ErrorKindRateLimited, []string{"claude-code"}, "Claude usage limit reached. No other runtime could take over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRTFixture(t, tc.avail...)
			f.agent("a", tc.runtime, nexusv1.Tier_TIER_HEAVY, "")
			task, err := f.e.CreateTask(ctx, &nexusv1.CreateTaskRequest{Prompt: "x", AgentId: "a"})
			if err != nil {
				t.Fatal(err)
			}
			ev := failedEvent(tc.kind, "Claude usage limit reached.")
			ev.RunId = task.CurrentRunId
			if err := f.e.HandleRunEvent(ctx, "node-1", &nexusv1.RunEvent{Event: ev}); err != nil {
				t.Fatal(err)
			}
			cur, _ := f.e.GetTask(ctx, task.Id)
			if cur.State != stFailed || !strings.HasPrefix(cur.Summary, tc.wantSummary) {
				t.Fatalf("task %v %q", cur.State, cur.Summary)
			}
		})
	}
}
