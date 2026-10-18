package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
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
	if m := first.GetCost().GetCost(); m.GetModel() != "claude-sonnet-4-5" || m.GetRuntime() != "claude-code" {
		t.Fatalf("model announcement %v", first)
	}
	first = <-run.Events()
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

var memoryTool = kernel.ToolFunc{
	S: kernel.ToolSpec{Name: "memory.search", Description: "Search memory", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
	F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
		var in struct{ Query string }
		_ = kernel.ToolInput(c, &in)
		return kernel.ToolOutput(map[string]string{"hit": in.Query + "!"})
	},
}

func TestNexusPermissionOverMCP(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			rt := newRuntime(t, Config{})
			run, err := rt.Start(context.Background(), kernel.RunSpec{
				Assignment: assignment(), Workdir: dir, Tools: []kernel.Tool{memoryTool},
				Env: map[string]string{"FAKE_CLAUDE_MODE": "permission", "FAKE_ARGS_FILE": argsFile},
			})
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
						if a.ActionClass != nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH || a.Summary != "git push origin main" ||
							a.Payload.Fields["tool"].GetStringValue() != "Bash" || a.AgentId != "coder" {
							t.Errorf("approval %v", a)
						}
						if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{RunId: "run_1",
							Approval: &nexusv1.Approval{Id: a.Id},
							Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: approve, ActionSha256: a.PayloadHash}}); err != nil {
							t.Fatal(err)
						}
					}
				case <-timeout:
					t.Fatalf("timeout; events %v", evs)
				}
			}
			task := last(t, evs)
			want := "verdict: deny memory: ok"
			if approve {
				want = "verdict: allow memory: ok"
			}
			if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != want {
				t.Fatalf("task %v", task)
			}
			var states []nexusv1.TaskState
			for _, ev := range evs {
				if tu := ev.GetTaskUpdated(); tu != nil {
					states = append(states, tu.Task.State)
				}
			}
			if len(states) != 3 || states[0] != nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL || states[1] != nexusv1.TaskState_TASK_STATE_RUNNING {
				t.Fatalf("states %v", states)
			}
			args, _ := os.ReadFile(argsFile)
			cfgFile := filepath.Join(dir, MCPConfigFile)
			if !strings.Contains(string(args), "--mcp-config\n"+cfgFile+"\n") ||
				!strings.Contains(string(args), "--permission-prompt-tool\nmcp__nexus__nexus_permission\n") {
				t.Fatalf("args %s", args)
			}
			if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
				t.Errorf("mcp config not removed after the run: %v", err)
			}
		})
	}
}

// TestNexusBridgeDirect drives the run's MCP endpoint from the test while
// the fake CLI idles (no curl needed).
func TestNexusBridgeDirect(t *testing.T) {
	dir := t.TempDir()
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: &nexusv1.RunAssignment{RunId: "run_1", TaskId: "tsk_1", Prompt: "x",
			Agent: &nexusv1.Agent{Id: "coder", Policy: map[string]nexusv1.TrustLevel{"compute": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE}}},
		Workdir: dir, Tools: []kernel.Tool{memoryTool},
		Env: map[string]string{"FAKE_CLAUDE_MODE": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(filepath.Join(dir, MCPConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, MCPConfigFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", fi.Mode())
	}
	var cfg struct {
		MCPServers map[string]struct{ Type, URL string } `json:"mcpServers"`
	}
	if err := json.Unmarshal(conf, &cfg); err != nil || cfg.MCPServers["nexus"].Type != "http" {
		t.Fatalf("config %s", conf)
	}
	c := mcp.NewClient(cfg.MCPServers["nexus"].URL)
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	// Agent policy compute: approve → Edit needs approval.
	type res struct {
		r   *mcp.CallResult
		err error
	}
	ch := make(chan res, 1)
	go func() {
		r, err := c.CallTool(ctx, mcp.PermissionToolName, map[string]any{"tool_name": "Edit", "input": map[string]any{"file_path": "a.go", "old_string": "x", "new_string": "y"}})
		ch <- res{r, err}
	}()
	var apr *nexusv1.Approval
	for apr == nil {
		select {
		case ev := <-run.Events():
			apr = ev.GetApproval()
		case <-time.After(10 * time.Second):
			t.Fatal("no approval")
		}
	}
	if apr.ActionClass != nexusv1.ActionClass_ACTION_CLASS_COMPUTE || apr.Summary != "Edit a.go" {
		t.Fatalf("approval %v", apr)
	}
	if err := run.Resolve(ctx, &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: apr.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED}}); err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.err != nil || !strings.Contains(got.r.Content[0].Text, `"behavior":"allow"`) || !strings.Contains(got.r.Content[0].Text, `"new_string":"y"`) {
		t.Fatalf("permission result %+v %v", got.r, got.err)
	}
	// Nexus tool over MCP.
	r, err := c.CallTool(ctx, "memory_search", map[string]string{"query": "milk"})
	if err != nil || r.IsError || !strings.Contains(r.Content[0].Text, "milk!") {
		t.Fatalf("memory_search %+v %v", r, err)
	}
	// A pending prompt is denied when the run is cancelled.
	go func() {
		r, err := c.CallTool(ctx, mcp.PermissionToolName, map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "rm -rf /tmp/x"}})
		ch <- res{r, err}
	}()
	for apr = nil; apr == nil; {
		select {
		case ev := <-run.Events():
			apr = ev.GetApproval()
		case <-time.After(10 * time.Second):
			t.Fatal("no approval")
		}
	}
	if apr.ActionClass != nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE || apr.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
		t.Fatalf("approval %v", apr)
	}
	if err := run.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	got = <-ch
	if got.err == nil && !strings.Contains(got.r.Content[0].Text, `"behavior":"deny"`) {
		t.Fatalf("cancelled prompt %+v", got.r)
	}
	if task := last(t, collect(t, run)); task.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("task %v", task)
	}
	if _, err := c.Call(ctx, "ping", nil); err == nil {
		t.Error("endpoint still up after the run")
	}
}

func TestNexusMCPArgs(t *testing.T) {
	off := false
	cases := []struct {
		name       string
		cfg        Config
		mcp, perms bool
	}{
		{"default", Config{}, true, true},
		{"bypass", Config{PermissionMode: "bypassPermissions"}, true, false},
		{"disabled", Config{NexusMCP: &off}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			rt := newRuntime(t, c.cfg)
			if rt.Capabilities().ApprovalHooks != c.mcp {
				t.Errorf("ApprovalHooks %v", rt.Capabilities().ApprovalHooks)
			}
			run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: assignment(), Workdir: dir,
				Env: map[string]string{"FAKE_ARGS_FILE": argsFile, "FAKE_CLAUDE_MODE": "error"}})
			if err != nil {
				t.Fatal(err)
			}
			collect(t, run)
			args, _ := os.ReadFile(argsFile)
			if got := strings.Contains(string(args), MCPConfigFile); got != c.mcp {
				t.Errorf("mcp config in args = %v:\n%s", got, args)
			}
			if got := strings.Contains(string(args), "--permission-prompt-tool"); got != c.perms {
				t.Errorf("permission tool in args = %v:\n%s", got, args)
			}
			if !c.mcp {
				if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{}); !errors.Is(err, ErrApprovalsUnsupported) {
					t.Errorf("resolve without bridge: %v", err)
				}
			}
		})
	}
}

func TestPermissionClass(t *testing.T) {
	compute, push, destructive, read := nexusv1.ActionClass_ACTION_CLASS_COMPUTE, nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH,
		nexusv1.ActionClass_ACTION_CLASS_DESTRUCTIVE, nexusv1.ActionClass_ACTION_CLASS_READ
	bash := map[string]nexusv1.ActionClass{
		"ls -la":                              compute,
		"go test ./...":                       compute,
		"rm file.txt":                         compute,
		"rm -r dir":                           compute,
		"rm -rf build":                        destructive,
		"rm -fr build":                        destructive,
		"rm -r -f build":                      destructive,
		"sudo rm --recursive --force /":       destructive,
		"cd x && /bin/rm -Rf y":               destructive,
		"git push":                            push,
		"git push -u origin feature":          push,
		"git -C repo push origin main":        push,
		"git commit -m x && git push":         push,
		"git push --force origin main":        destructive,
		"git push -f":                         destructive,
		"git push --force-with-lease":         destructive,
		"git push origin +main":               destructive,
		"git push origin :old-branch":         destructive,
		"git push --delete origin x":          destructive,
		`bash -c "git push origin main"`:      push,
		`ssh host 'rm -rf /srv/data'`:         destructive,
		"echo 'git pushover'":                 push, // conservative substring fallback
		"git status; git log --oneline | cat": compute,
	}
	for cmd, want := range bash {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		if got, sum := PermissionClass("Bash", in); got != want || sum != cmd {
			t.Errorf("Bash %q → %s %q, want %s", cmd, got, sum, want)
		}
	}
	tools := map[string]nexusv1.ActionClass{
		"Write": compute, "Edit": compute, "MultiEdit": compute, "NotebookEdit": compute,
		"Read": read, "Grep": read, "Glob": read, "LS": read, "WebFetch": read, "WebSearch": read,
		"mcp__github__create_pr": nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED,
		"SomethingNew":           nexusv1.ActionClass_ACTION_CLASS_UNSPECIFIED,
	}
	for tool, want := range tools {
		if got, _ := PermissionClass(tool, json.RawMessage(`{"file_path":"a"}`)); got != want {
			t.Errorf("%s → %s, want %s", tool, got, want)
		}
	}
}
