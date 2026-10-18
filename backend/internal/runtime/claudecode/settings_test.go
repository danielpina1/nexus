package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/mcp"
	"github.com/danielpina1/nexus/backend/kernel"
)

func withClaude(t *testing.T, a *nexusv1.RunAssignment, claude map[string]any) *nexusv1.RunAssignment {
	t.Helper()
	ctx, err := structpb.NewStruct(map[string]any{"claude": claude})
	if err != nil {
		t.Fatal(err)
	}
	a.Context = ctx
	return a
}

func argValue(args []string, flag string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func TestArgsForAgentSettings(t *testing.T) {
	agent := func(id, model string) *nexusv1.RunAssignment {
		return &nexusv1.RunAssignment{RunId: "r", TaskId: "t", Instructions: "Be a researcher.", Agent: &nexusv1.Agent{Id: id, Model: model}}
	}
	for _, tc := range []struct {
		name       string
		cfg        Config
		a          *nexusv1.RunAssignment
		nexus      string
		want       map[string]string // flag → value ("" = must be absent)
		notContain []string
	}{
		{
			name: "non-coding defaults", a: agent("researcher", "smart"), nexus: "/w/.nexus-mcp.json",
			want: map[string]string{
				"--model": "sonnet", "--allowedTools": "mcp__nexus",
				"--disallowedTools":        "Bash,Edit,MultiEdit,Write,NotebookEdit,WebSearch,WebFetch,Task,Agent,BashOutput,KillShell,KillBash",
				"--permission-prompt-tool": "mcp__nexus__nexus_permission",
				"--append-system-prompt":   "Be a researcher.",
				"--max-turns":              "",
			},
		},
		{
			name: "researcher allows Claude web tools", nexus: "/w/.nexus-mcp.json",
			a: withClaude(t, agent("researcher", "smart"), map[string]any{
				"model": "opus", "allowed_tools": []any{"WebSearch", "WebFetch"}, "disallowed_tools": []any{"Task"},
				"permission_mode": "plan", "max_turns": 30.0,
			}),
			want: map[string]string{
				"--model": "opus", "--allowedTools": "mcp__nexus,WebSearch,WebFetch",
				"--disallowedTools": "Bash,Edit,MultiEdit,Write,NotebookEdit,Task,Agent,BashOutput,KillShell,KillBash",
				"--permission-mode": "plan", "--max-turns": "30",
			},
		},
		{
			name: "coding agent keeps built-ins", nexus: "/w/.nexus-mcp.json",
			a:    withClaude(t, agent("code-fixer", "best"), map[string]any{"coding": true}),
			want: map[string]string{"--model": "opus", "--allowedTools": "mcp__nexus", "--disallowedTools": "Task,Agent,BashOutput,KillShell,KillBash"},
		},
		{
			name: "workdir makes a coding agent", cfg: Config{Workdirs: map[string]string{"coder": "/src"}},
			a:    agent("coder", "best"),
			want: map[string]string{"--disallowedTools": "Task,Agent,BashOutput,KillShell,KillBash", "--allowedTools": ""},
		},
		{
			name: "bypassPermissions from an agent note is ignored", cfg: Config{PermissionMode: "acceptEdits"}, nexus: "/w/c.json",
			a:    withClaude(t, agent("x", ""), map[string]any{"permission_mode": "bypassPermissions"}),
			want: map[string]string{"--permission-mode": "acceptEdits", "--permission-prompt-tool": "mcp__nexus__nexus_permission"},
		},
		{
			name: "operator model and empty non-coding list", cfg: Config{Model: "claude-sonnet-4-5", NonCodingDisallowed: []string{}},
			a:    agent("x", "smart"),
			want: map[string]string{"--model": "claude-sonnet-4-5", "--disallowedTools": "Task,Agent,BashOutput,KillShell,KillBash"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Args = []string{"--extra"}
			rt := New(tc.cfg, nil)
			args := rt.ArgsFor(tc.a, tc.nexus)
			for flag, want := range tc.want {
				got, ok := argValue(args, flag)
				if want == "" && ok {
					t.Errorf("%s present (%q): %v", flag, got, args)
				} else if flag == "--append-system-prompt" {
					if !strings.HasPrefix(got, want) || !strings.Contains(got, "spawn_agent") {
						t.Errorf("%s = %q, want prefix %q plus the Nexus run note", flag, got, want)
					}
				} else if want != "" && got != want {
					t.Errorf("%s = %q, want %q: %v", flag, got, want, args)
				}
			}
			if args[len(args)-1] != "--extra" {
				t.Errorf("operator args not last: %v", args)
			}
		})
	}
}

func TestUsageLimitAndLogin(t *testing.T) {
	for _, tc := range []struct {
		mode, kind, summary string
	}{
		{"ratelimit", kernel.ErrorKindRateLimited, "Claude usage limit reached; resets at "},
		{"limitok", kernel.ErrorKindRateLimited, "Claude usage limit reached; resets 3pm."},
		{"nologin", kernel.ErrorKindAuth, "Claude Code is not logged in on node mac"},
		{"crash", "", "boom"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			rt := newRuntime(t, Config{})
			run, err := rt.Start(context.Background(), kernel.RunSpec{
				Assignment: assignment(), Workdir: t.TempDir(),
				Env: map[string]string{"FAKE_CLAUDE_MODE": tc.mode, "NEXUS_NODE_ID": "mac"},
			})
			if err != nil {
				t.Fatal(err)
			}
			task := last(t, collect(t, run))
			res := task.GetResult().AsMap()
			if task.State != nexusv1.TaskState_TASK_STATE_FAILED || !strings.Contains(task.Summary, tc.summary) {
				t.Fatalf("task %v", task)
			}
			if kind, _ := res["error_kind"].(string); kind != tc.kind {
				t.Fatalf("error_kind %q, result %v", kind, res)
			}
			if tc.kind != "" && res["runtime"] != "claude-code" {
				t.Errorf("result %v", res)
			}
		})
	}
}

func TestMaxConcurrentDefault(t *testing.T) {
	if n := New(Config{}, nil).MaxConcurrent(); n != DefaultMaxConcurrent {
		t.Fatalf("default %d", n)
	}
	zero := 0
	if n := New(Config{MaxConcurrent: &zero}, nil).MaxConcurrent(); n != 0 {
		t.Fatalf("configured %d", n)
	}
	var _ kernel.ConcurrencyLimited = (*Runtime)(nil)
}

// TestRunGateThroughBridge: a run tool that asks for a SPEND approval via
// RunSpec.Gate (like the browser's checkout click) is gated by the Claude
// Code run's approval gate when called over the Nexus MCP endpoint, and the
// denial reaches the tool.
func TestRunGateThroughBridge(t *testing.T) {
	dir := t.TempDir()
	gate := kernel.NewRunGate()
	clicked := make(chan bool, 2)
	checkout := kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "browser.click", ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE, InputSchema: []byte(`{"type":"object"}`)},
		F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			out, err := gate.Approve(ctx, kernel.ApprovalRequest{Class: nexusv1.ActionClass_ACTION_CLASS_SPEND,
				Title: "checkout", Payload: map[string]any{"element": "Finalizar compra"}})
			if err != nil || !out.Approved {
				clicked <- false
				return kernel.ToolOutput(map[string]any{"done": false, "reason": out.Reason})
			}
			clicked <- true
			return kernel.ToolOutput(map[string]any{"done": true})
		},
	}
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: &nexusv1.RunAssignment{RunId: "run_1", TaskId: "tsk_1", Prompt: "buy", Agent: &nexusv1.Agent{Id: "shop", Tools: []string{"browser"}}},
		Workdir:    dir, Tools: []kernel.Tool{checkout}, Gate: gate,
		Env: map[string]string{"FAKE_CLAUDE_MODE": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Cancel(context.Background()); collect(t, run) }()
	if !gate.Bound() {
		t.Fatal("claude-code did not bind its gate to RunSpec.Gate")
	}
	conf, err := os.ReadFile(filepath.Join(dir, MCPConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	url := strings.Split(strings.Split(string(conf), `"url": "`)[1], `"`)[0]
	c := mcp.NewClient(url)
	ctx := context.Background()
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []nexusv1.ApprovalState{nexusv1.ApprovalState_APPROVAL_STATE_DENIED, nexusv1.ApprovalState_APPROVAL_STATE_APPROVED} {
		done := make(chan *mcp.CallResult, 1)
		go func() {
			r, err := c.CallTool(ctx, "browser_click", map[string]any{"ref": 7})
			if err != nil {
				t.Error(err)
			}
			done <- r
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
		if apr.ActionClass != nexusv1.ActionClass_ACTION_CLASS_SPEND || apr.Level != nexusv1.TrustLevel_TRUST_LEVEL_BIOMETRIC {
			t.Fatalf("approval %v", apr)
		}
		if err := run.Resolve(ctx, &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: apr.Id, State: decision}}); err != nil {
			t.Fatal(err)
		}
		r := <-done
		ok := <-clicked
		if ok != (decision == nexusv1.ApprovalState_APPROVAL_STATE_APPROVED) || r == nil {
			t.Fatalf("decision %v: clicked=%v result=%+v", decision, ok, r)
		}
	}
}
