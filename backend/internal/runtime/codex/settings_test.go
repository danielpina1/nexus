package codex

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func flag(args []string, f string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == f {
			return args[i+1]
		}
	}
	return ""
}

func TestArgsForAgentSettings(t *testing.T) {
	mk := func(id string, codex map[string]any) *nexusv1.RunAssignment {
		a := &nexusv1.RunAssignment{RunId: "r", Agent: &nexusv1.Agent{Id: id, Model: "smart"}}
		if codex != nil {
			a.Context, _ = structpb.NewStruct(map[string]any{"codex": codex})
		}
		return a
	}
	for _, tc := range []struct {
		name           string
		cfg            Config
		a              *nexusv1.RunAssignment
		model, sandbox string
	}{
		{"non-coding defaults to read-only", Config{}, mk("researcher", nil), "", "read-only"},
		{"agent model and sandbox", Config{}, mk("x", map[string]any{"model": "gpt-5", "sandbox": "workspace-write"}), "gpt-5", "workspace-write"},
		{"agent cannot pick danger-full-access", Config{Sandbox: "workspace-write"}, mk("x", map[string]any{"sandbox": "danger-full-access"}), "", "workspace-write"},
		{"coding agent uses module default", Config{}, mk("code-fixer", map[string]any{"coding": true}), "", ""},
		{"alias mapping", Config{Models: map[string]string{"best": "gpt-5-codex"}}, mk("x", map[string]any{"model": "best"}), "gpt-5-codex", "read-only"},
		{"non_coding_sandbox", Config{NonCodingSandbox: "workspace-write"}, mk("x", nil), "", "workspace-write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := New(tc.cfg, kerneltest.New(t)).ArgsFor(tc.a, "")
			if got := flag(args, "--model"); got != tc.model {
				t.Errorf("model %q want %q (%v)", got, tc.model, args)
			}
			if got := flag(args, "--sandbox"); got != tc.sandbox {
				t.Errorf("sandbox %q want %q (%v)", got, tc.sandbox, args)
			}
		})
	}
}

func TestAvailableLoginStatus(t *testing.T) {
	rt := newRuntime(t, Config{})
	t.Setenv("FAKE_CODEX_LOGIN", "Logged in using ChatGPT")
	if v, ok := rt.Available(context.Background()); !ok || v != "codex-cli 0.99.0" {
		t.Fatalf("logged in: %q %v", v, ok)
	}
	t.Setenv("FAKE_CODEX_LOGIN", "Not logged in")
	if v, ok := rt.Available(context.Background()); ok || !strings.Contains(v, "codex login") {
		t.Fatalf("not logged in: %q %v", v, ok)
	}
	off := false
	rt.cfg.CheckLogin = &off
	if _, ok := rt.Available(context.Background()); !ok {
		t.Fatal("check_login: false must skip the login check")
	}
}

func TestUsageLimitAndLogin(t *testing.T) {
	for _, tc := range []struct{ mode, kind, summary string }{
		{"ratelimit", kernel.ErrorKindRateLimited, "Codex (ChatGPT) usage limit reached; resets in 2 hours 5 minutes."},
		{"crash", kernel.ErrorKindAuth, "Codex is not logged in on node mac. Run `codex login`"},
		{"fail", "", "stream disconnected"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			run := start(t, newRuntime(t, Config{}), map[string]string{"FAKE_CODEX_MODE": tc.mode, "NEXUS_NODE_ID": "mac"})
			task := last(t, collect(t, run))
			if task.State != nexusv1.TaskState_TASK_STATE_FAILED || !strings.Contains(task.Summary, tc.summary) {
				t.Fatalf("task %v", task)
			}
			if k, _ := task.GetResult().AsMap()["error_kind"].(string); k != tc.kind {
				t.Fatalf("error_kind %q", k)
			}
		})
	}
	if n := New(Config{}, kerneltest.New(t)).MaxConcurrent(); n != DefaultMaxConcurrent {
		t.Fatalf("max concurrent %d", n)
	}
}
