package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func newRuntime(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires a POSIX shell")
	}
	src, err := os.ReadFile("testdata/fake-codex.sh")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Binary = filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cfg.Binary, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(cfg, kerneltest.New(t))
}

func assignment() *nexusv1.RunAssignment {
	return &nexusv1.RunAssignment{
		RunId: "run_1", TaskId: "tsk_1", Prompt: "create c.txt",
		Instructions: "You are the coder agent.",
		Agent:        &nexusv1.Agent{Id: "coder", Model: "best"},
	}
}

func collect(t *testing.T, run kernel.Run) []*nexusv1.Event {
	t.Helper()
	var evs []*nexusv1.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatalf("timeout; events so far: %v", evs)
		}
	}
}

func last(t *testing.T, evs []*nexusv1.Event) *nexusv1.Task {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	task := evs[len(evs)-1].GetTaskUpdated().GetTask()
	if task == nil {
		t.Fatalf("last event not TaskUpdated: %v", evs[len(evs)-1])
	}
	return task
}

func start(t *testing.T, rt *Runtime, env map[string]string) kernel.Run {
	t.Helper()
	run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: assignment(), Workdir: t.TempDir(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type summary struct {
	texts   []string
	calls   map[string]*nexusv1.ToolCall
	results map[string]*nexusv1.ToolResult
	costs   []*nexusv1.Cost
}

func summarize(evs []*nexusv1.Event) summary {
	s := summary{calls: map[string]*nexusv1.ToolCall{}, results: map[string]*nexusv1.ToolResult{}}
	for _, ev := range evs {
		switch {
		case ev.GetRunOutput() != nil && !ev.GetRunOutput().IsFinal:
			s.texts = append(s.texts, ev.GetRunOutput().Text)
		case ev.GetToolCall() != nil:
			s.calls[ev.GetToolCall().CallId] = ev.GetToolCall()
		case ev.GetToolResult() != nil:
			s.results[ev.GetToolResult().CallId] = ev.GetToolResult()
		case ev.GetCost() != nil:
			s.costs = append(s.costs, ev.GetCost().Cost)
		}
	}
	return s
}

func TestSuccess(t *testing.T) {
	dir := t.TempDir()
	argsFile, stdinFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	rt := newRuntime(t, Config{Models: map[string]string{"best": "gpt-5-codex"}, Sandbox: "workspace-write"})
	if v, ok := rt.Available(context.Background()); !ok || v != "codex-cli 0.99.0" {
		t.Fatalf("available %q %v", v, ok)
	}
	evs := collect(t, start(t, rt, map[string]string{"FAKE_ARGS_FILE": argsFile, "FAKE_STDIN_FILE": stdinFile}))
	task := last(t, evs)
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != "Created c.txt." {
		t.Fatalf("task %v", task)
	}
	s := summarize(evs)
	if strings.Join(s.texts, "|") != "Created c.txt." {
		t.Errorf("texts %v", s.texts)
	}
	if c := s.calls["item_1"]; c == nil || c.Tool != "shell" || c.ActionClass != nexusv1.ActionClass_ACTION_CLASS_COMPUTE ||
		c.Input.Fields["command"].GetStringValue() != "bash -lc ls" {
		t.Errorf("call 1 %v", c)
	}
	if r := s.results["item_1"]; r == nil || r.Error != "" || r.Output.Fields["output"].GetStringValue() != "a.txt\nb.txt\n" {
		t.Errorf("result 1 %v", r)
	}
	if r := s.results["item_2"]; r == nil || r.Error != "exit code 1" {
		t.Errorf("result 2 %v", r)
	}
	if c := s.calls["item_3"]; c == nil || c.Tool != "apply_patch" {
		t.Errorf("file change call %v", c)
	}
	if c := s.calls["item_4"]; c == nil || c.Tool != "nexus.memory.search" || c.Input.Fields["q"].GetStringValue() != "x" {
		t.Errorf("mcp call %v", c)
	}
	if r := s.results["item_4"]; r == nil || r.Output.Fields["hits"].GetNumberValue() != 1 {
		t.Errorf("mcp result %v", r)
	}
	if len(s.costs) != 1 || s.costs[0].InputTokens != 2000 || s.costs[0].OutputTokens != 300 ||
		s.costs[0].Model != "gpt-5-codex" || s.costs[0].Runtime != "codex" {
		t.Errorf("costs %v", s.costs)
	}
	if task.Cost == nil || task.Cost.InputTokens != 2000 {
		t.Errorf("task cost %v", task.Cost)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"exec\n--json\n", "--model\ngpt-5-codex\n", "--sandbox\nworkspace-write\n", "--skip-git-repo-check\n", "\n-\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "--full-auto") {
		t.Error("--full-auto must be off by default")
	}
	in, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(in), "You are the coder agent.") || !strings.Contains(string(in), "create c.txt") {
		t.Errorf("prompt %q", in)
	}
}

func TestLegacy(t *testing.T) {
	evs := collect(t, start(t, newRuntime(t, Config{}), map[string]string{"FAKE_CODEX_MODE": "legacy"}))
	task := last(t, evs)
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != "Found a.txt" {
		t.Fatalf("task %v", task)
	}
	s := summarize(evs)
	if c := s.calls["call_1"]; c == nil || c.Input.Fields["command"].GetStringValue() != "bash -lc ls" {
		t.Errorf("call %v", c)
	}
	if r := s.results["call_1"]; r == nil || r.Output.Fields["output"].GetStringValue() != "a.txt\n" {
		t.Errorf("result %v", r)
	}
	if len(s.costs) != 1 || s.costs[0].InputTokens != 10 {
		t.Errorf("costs %v", s.costs)
	}
}

func TestFailures(t *testing.T) {
	for mode, want := range map[string]string{"fail": "stream disconnected", "crash": "not logged in"} {
		t.Run(mode, func(t *testing.T) {
			task := last(t, collect(t, start(t, newRuntime(t, Config{}), map[string]string{"FAKE_CODEX_MODE": mode})))
			if task.State != nexusv1.TaskState_TASK_STATE_FAILED || !strings.Contains(task.Summary, want) {
				t.Fatalf("task %v", task)
			}
		})
	}
}

func TestCancelAndSend(t *testing.T) {
	run := start(t, newRuntime(t, Config{}), map[string]string{"FAKE_CODEX_MODE": "hang"})
	if err := run.Send(context.Background(), &nexusv1.RunInput{Text: "x"}); err == nil {
		t.Error("send should be unsupported")
	}
	time.Sleep(100 * time.Millisecond)
	begin := time.Now()
	_ = run.Cancel(context.Background())
	task := last(t, collect(t, run))
	if task.State != nexusv1.TaskState_TASK_STATE_CANCELLED || time.Since(begin) > 5*time.Second {
		t.Fatalf("task %v after %v", task, time.Since(begin))
	}
}

func TestModuleAndUnavailable(t *testing.T) {
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	h.ConfigYAML = "binary: /nonexistent/codex\n"
	if err := (&Module{}).Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	rt, ok := h.Runtimes().Get("codex")
	if !ok {
		t.Fatal("not registered")
	}
	if _, ok := rt.Available(context.Background()); ok {
		t.Fatal("missing binary reported available")
	}
	if rt.Capabilities().Steering {
		t.Error("codex should not claim steering")
	}
}

func TestNexusMCP(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	push := kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "github.push", ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH},
		F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			return kernel.ToolOutput(map[string]string{"status": "pushed"})
		},
	}
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			rt := newRuntime(t, Config{})
			if !rt.Capabilities().ApprovalHooks {
				t.Error("ApprovalHooks off with nexus_mcp")
			}
			run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: assignment(), Workdir: dir, Tools: []kernel.Tool{push},
				Env: map[string]string{"FAKE_CODEX_MODE": "mcp", "FAKE_ARGS_FILE": argsFile}})
			if err != nil {
				t.Fatal(err)
			}
			var evs []*nexusv1.Event
			timeout := time.After(15 * time.Second)
		loop:
			for {
				select {
				case ev, ok := <-run.Events():
					if !ok {
						break loop
					}
					evs = append(evs, ev)
					if a := ev.GetApproval(); a != nil {
						if a.Payload.Fields["tool"].GetStringValue() != "github.push" || a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH {
							t.Errorf("approval %v", a)
						}
						if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id},
							Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: approve, ActionSha256: a.PayloadHash}}); err != nil {
							t.Fatal(err)
						}
					}
				case <-timeout:
					t.Fatalf("timeout; events %v", evs)
				}
			}
			want := "denied"
			if approve {
				want = "pushed"
			}
			if task := last(t, evs); task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != want {
				t.Fatalf("task %v", task)
			}
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "-c\nmcp_servers.nexus.url=\"http://127.0.0.1:") ||
				!strings.Contains(string(args), "-c\nmcp_servers.nexus.tool_timeout_sec=3720\n") {
				t.Fatalf("args %s", args)
			}
		})
	}
}

func TestNexusMCPArgsTemplate(t *testing.T) {
	rt := New(Config{NexusMCPArgs: []string{"-c", "mcp_servers.nx.url='{url}'", "-c", "experimental_use_rmcp_client=true"}}, kerneltest.New(t))
	got := strings.Join(rt.ArgsWithNexus("", "http://h/mcp/t"), " ")
	if !strings.Contains(got, "-c mcp_servers.nx.url='http://h/mcp/t' -c experimental_use_rmcp_client=true") || !strings.HasSuffix(got, " -") {
		t.Fatalf("args %s", got)
	}
	if strings.Contains(strings.Join(rt.Args(""), " "), "mcp_servers") {
		t.Fatal("Args without url attached nexus")
	}
	// Disabled explicitly: no endpoint, Resolve unsupported.
	off := false
	rt2 := newRuntime(t, Config{NexusMCP: &off})
	run := start(t, rt2, map[string]string{"FAKE_CODEX_MODE": "fail"})
	if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{}); !errors.Is(err, ErrApprovalsUnsupported) {
		t.Fatalf("resolve: %v", err)
	}
	collect(t, run)
}
