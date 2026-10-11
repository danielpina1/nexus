package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func fakeBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires a POSIX shell")
	}
	src, err := os.ReadFile("testdata/fake-claude.sh")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newRuntime(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	cfg.Binary = fakeBinary(t)
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	return New(cfg, h)
}

func assignment() *nexusv1.RunAssignment {
	return &nexusv1.RunAssignment{
		RunId: "run_1", TaskId: "tsk_1", Prompt: "list the files",
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

func TestSuccess(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stdinFile := filepath.Join(dir, "stdin")
	rt := newRuntime(t, Config{Models: map[string]string{"best": "opus"}, PermissionMode: "acceptEdits", Args: []string{"--max-turns", "5"}})
	if v, ok := rt.Available(context.Background()); !ok || v != "9.9.9 (Claude Code)" {
		t.Fatalf("available %q %v", v, ok)
	}
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: dir,
		Env: map[string]string{"FAKE_ARGS_FILE": argsFile, "FAKE_STDIN_FILE": stdinFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	task := last(t, evs)
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != "There are two files." {
		t.Fatalf("task %v", task)
	}

	var texts []string
	calls := map[string]*nexusv1.ToolCall{}
	results := map[string]*nexusv1.ToolResult{}
	var cost *nexusv1.Cost
	for _, ev := range evs {
		switch {
		case ev.GetRunOutput() != nil && !ev.GetRunOutput().IsFinal:
			texts = append(texts, ev.GetRunOutput().Text)
		case ev.GetToolCall() != nil:
			calls[ev.GetToolCall().CallId] = ev.GetToolCall()
		case ev.GetToolResult() != nil:
			results[ev.GetToolResult().CallId] = ev.GetToolResult()
		case ev.GetCost() != nil:
			cost = ev.GetCost().Cost
		}
	}
	if strings.Join(texts, "|") != "Let me look.|There are two files." {
		t.Errorf("texts %v", texts)
	}
	if c := calls["toolu_1"]; c == nil || c.Tool != "Bash" || c.ActionClass != nexusv1.ActionClass_ACTION_CLASS_COMPUTE ||
		c.Input.Fields["command"].GetStringValue() != "ls" {
		t.Errorf("call 1 %v", c)
	}
	if c := calls["toolu_2"]; c == nil || c.ActionClass != nexusv1.ActionClass_ACTION_CLASS_READ {
		t.Errorf("call 2 %v", c)
	}
	if r := results["toolu_1"]; r == nil || r.Output.Fields["content"].GetStringValue() != "a.txt\nb.txt" || r.Error != "" {
		t.Errorf("result 1 %v", r)
	}
	if r := results["toolu_2"]; r == nil || r.Error != "File does not exist." {
		t.Errorf("result 2 %v", r)
	}
	if cost == nil || cost.Amount.Currency != "USD" || cost.Amount.MinorUnits != 2 || cost.InputTokens != 150 ||
		cost.OutputTokens != 40 || cost.Model != "claude-sonnet-4-5" || cost.Runtime != "claude-code" {
		t.Errorf("cost %v", cost)
	}
	if task.Cost == nil || task.Cost.Amount.MinorUnits != 2 {
		t.Errorf("task cost %v", task.Cost)
	}

	args, _ := os.ReadFile(argsFile)
	got := string(args)
	for _, want := range []string{"-p\n", "--output-format\nstream-json\n", "--input-format\nstream-json\n", "--verbose\n",
		"--model\nopus\n", "--permission-mode\nacceptEdits\n", "--append-system-prompt\nYou are the coder agent.\n", "--max-turns\n5\n", dir} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q in:\n%s", want, got)
		}
	}
	in, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(in), `"type":"user"`) || !strings.Contains(string(in), "list the files") {
		t.Errorf("stdin %s", in)
	}
	if err := run.Send(context.Background(), &nexusv1.RunInput{Text: "late"}); err == nil {
		t.Error("send after result should fail")
	}
}

func TestSendMidRun(t *testing.T) {
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: t.TempDir(),
		Env: map[string]string{"FAKE_CLAUDE_MODE": "send"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-run.Events()
	if first.GetRunOutput().GetText() != "Working…" {
		t.Fatalf("first %v", first)
	}
	if err := run.Send(context.Background(), &nexusv1.RunInput{Text: "use main"}); err != nil {
		t.Fatal(err)
	}
	task := last(t, collect(t, run))
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != "got: use main" {
		t.Fatalf("task %v", task)
	}
}

func TestFailures(t *testing.T) {
	tests := []struct {
		mode, want string
	}{
		{"error", "error_max_turns"},
		{"crash", "boom: something broke"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			rt := newRuntime(t, Config{})
			run, err := rt.Start(context.Background(), kernel.RunSpec{
				Assignment: assignment(), Workdir: t.TempDir(),
				Env: map[string]string{"FAKE_CLAUDE_MODE": tt.mode},
			})
			if err != nil {
				t.Fatal(err)
			}
			task := last(t, collect(t, run))
			if task.State != nexusv1.TaskState_TASK_STATE_FAILED || !strings.Contains(task.Summary, tt.want) {
				t.Fatalf("task %v", task)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: t.TempDir(),
		Env: map[string]string{"FAKE_CLAUDE_MODE": "hang"},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if err := run.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := last(t, collect(t, run))
	if task.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("task %v", task)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
}

func TestUnavailable(t *testing.T) {
	rt := New(Config{Binary: filepath.Join(t.TempDir(), "missing")}, kerneltest.New(t))
	if _, ok := rt.Available(context.Background()); ok {
		t.Fatal("missing binary reported available")
	}
	if _, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: assignment(), Workdir: t.TempDir()}); err == nil {
		t.Fatal("start with missing binary should fail")
	}
}

func TestModelMapping(t *testing.T) {
	rt := New(Config{Model: "sonnet", Models: map[string]string{"best": "opus"}}, kerneltest.New(t))
	for alias, want := range map[string]string{"best": "opus", "haiku": "haiku", "claude-opus-4-1": "claude-opus-4-1", "smart": "sonnet"} {
		if got := rt.claudeModel(alias); got != want {
			t.Errorf("%s → %s want %s", alias, got, want)
		}
	}
}

func TestModuleRegisters(t *testing.T) {
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	if err := (&Module{}).Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Runtimes().Get("claude-code"); !ok {
		t.Fatal("runtime not registered")
	}
}
