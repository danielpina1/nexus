package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func runShell(t *testing.T, rt *Runtime, a *nexusv1.RunAssignment, during func(kernel.Run)) (string, *nexusv1.Task) {
	t.Helper()
	a.RunId, a.TaskId = "r1", "t1"
	r, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: a, Workdir: t.TempDir(), Env: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if during != nil {
		during(r)
	}
	var out strings.Builder
	var last *nexusv1.Task
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return out.String(), last
			}
			out.WriteString(ev.GetRunOutput().GetText())
			if tu := ev.GetTaskUpdated(); tu != nil {
				last = tu.Task
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
}

func TestCommand(t *testing.T) {
	tests := []struct {
		instr, prompt, want string
	}{
		{"", "echo hi", "echo hi"},
		{"grep -r {{prompt}} .", "it's", `grep -r 'it'\''s' .`},
		{"ls", "", "ls"},
	}
	for _, tc := range tests {
		if got := Command(&nexusv1.RunAssignment{Instructions: tc.instr, Prompt: tc.prompt}); got != tc.want {
			t.Errorf("Command(%q,%q) = %q, want %q", tc.instr, tc.prompt, got, tc.want)
		}
	}
}

func TestShellRuntime(t *testing.T) {
	rt := &Runtime{}
	if _, ok := rt.Available(context.Background()); !ok {
		t.Skip("sh not available")
	}
	tests := []struct {
		name    string
		a       *nexusv1.RunAssignment
		out     []string
		state   nexusv1.TaskState
		summary string
	}{
		{"success", &nexusv1.RunAssignment{Prompt: "echo one; echo two >&2; echo $FOO $NEXUS_RUN_ID"},
			[]string{"one\n", "[stderr] two\n", "bar r1\n"}, nexusv1.TaskState_TASK_STATE_SUCCEEDED, "exit code 0"},
		{"failure", &nexusv1.RunAssignment{Prompt: "exit 7"}, nil, nexusv1.TaskState_TASK_STATE_FAILED, "exit code 7"},
		{"template", &nexusv1.RunAssignment{Instructions: "printf '%s\\n' {{prompt}}", Prompt: "a b; rm -rf /"},
			[]string{"a b; rm -rf /\n"}, nexusv1.TaskState_TASK_STATE_SUCCEEDED, "exit code 0"},
		{"timeout", &nexusv1.RunAssignment{Prompt: "sleep 30", Agent: &nexusv1.Agent{Limits: &nexusv1.AgentLimits{MaxRuntimeSeconds: 1}}},
			nil, nexusv1.TaskState_TASK_STATE_FAILED, "timed out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, task := runShell(t, rt, tc.a, nil)
			for _, w := range tc.out {
				if !strings.Contains(out, w) {
					t.Errorf("output %q missing %q", out, w)
				}
			}
			if task == nil || task.State != tc.state || !strings.HasPrefix(task.Summary, tc.summary) {
				t.Fatalf("task %v", task)
			}
		})
	}
}

func TestShellCancelKillsGroup(t *testing.T) {
	rt := &Runtime{}
	start := time.Now()
	_, task := runShell(t, rt, &nexusv1.RunAssignment{Prompt: "sleep 30 & sleep 30; wait"}, func(r kernel.Run) {
		time.Sleep(100 * time.Millisecond)
		if err := r.Cancel(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if task.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("state %v", task.State)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not kill the process group")
	}
}
