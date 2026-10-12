package shell

import (
	"context"
	"os"
	"path/filepath"
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

// approvalRun starts a gated shell run and returns the pending approval.
func approvalRun(t *testing.T, prompt string) (kernel.Run, *nexusv1.Approval, string) {
	t.Helper()
	rt := &Runtime{}
	if _, ok := rt.Available(context.Background()); !ok {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	a := &nexusv1.RunAssignment{RunId: "r1", TaskId: "t1", Prompt: prompt,
		Agent: &nexusv1.Agent{Id: "shell", Policy: map[string]nexusv1.TrustLevel{"compute": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE}}}
	r, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: a, Workdir: dir, Env: map[string]string{"NEXUS_NODE_ID": "node-1"}})
	if err != nil {
		t.Fatal(err)
	}
	ev := next(t, r)
	apr := ev.GetApproval()
	if apr == nil {
		t.Fatalf("first event %v, want approval", ev)
	}
	if st := next(t, r).GetTaskUpdated().GetTask().GetState(); st != nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL {
		t.Fatalf("state %s", st)
	}
	return r, apr, dir
}

func next(t *testing.T, r kernel.Run) *nexusv1.Event {
	t.Helper()
	select {
	case ev, ok := <-r.Events():
		if !ok {
			t.Fatal("events closed")
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
		return nil
	}
}

func drain(t *testing.T, r kernel.Run) (string, *nexusv1.Task, []*nexusv1.Event) {
	t.Helper()
	var out strings.Builder
	var last *nexusv1.Task
	var evs []*nexusv1.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return out.String(), last, evs
			}
			evs = append(evs, ev)
			out.WriteString(ev.GetRunOutput().GetText())
			if tu := ev.GetTaskUpdated(); tu != nil {
				last = tu.Task
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
}

func resolution(a *nexusv1.Approval, approve bool) *nexusv1.ApprovalResolution {
	st := nexusv1.ApprovalState_APPROVAL_STATE_DENIED
	if approve {
		st = nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	}
	return &nexusv1.ApprovalResolution{RunId: a.RunId,
		Approval: &nexusv1.Approval{Id: a.Id, State: st, PayloadHash: a.PayloadHash},
		Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: approve, ActionSha256: a.PayloadHash}}
}

func TestShellApprovalApproved(t *testing.T) {
	r, apr, dir := approvalRun(t, "echo approved-run > out.txt; cat out.txt")
	f := apr.Payload.GetFields()
	if apr.ActionClass != nexusv1.ActionClass_ACTION_CLASS_COMPUTE || f["action"].GetStringValue() != "shell.exec" ||
		f["command"].GetStringValue() != "echo approved-run > out.txt; cat out.txt" || f["workdir"].GetStringValue() != dir ||
		f["node"].GetStringValue() != "node-1" || apr.Level != nexusv1.TrustLevel_TRUST_LEVEL_APPROVE {
		t.Fatalf("approval %v", apr)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
		t.Fatal("command ran before approval")
	}
	if err := r.Resolve(context.Background(), resolution(apr, true)); err != nil {
		t.Fatal(err)
	}
	out, task, evs := drain(t, r)
	if evs[0].GetTaskUpdated().GetTask().GetState() != nexusv1.TaskState_TASK_STATE_RUNNING {
		t.Fatalf("first event after approval %v", evs[0])
	}
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || !strings.Contains(out, "approved-run") {
		t.Fatalf("task %v out %q", task, out)
	}
}

func TestShellApprovalDenied(t *testing.T) {
	r, apr, dir := approvalRun(t, "touch ran.txt")
	if err := r.Resolve(context.Background(), resolution(apr, false)); err != nil {
		t.Fatal(err)
	}
	_, task, _ := drain(t, r)
	if task.State != nexusv1.TaskState_TASK_STATE_FAILED || task.Summary != "denied by user" {
		t.Fatalf("task %v", task)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
		t.Fatal("denied command ran")
	}
}

func TestShellApprovalCancelled(t *testing.T) {
	r, _, dir := approvalRun(t, "touch ran.txt")
	if err := r.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, task, _ := drain(t, r)
	if task.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("task %v", task)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
		t.Fatal("cancelled command ran")
	}
}

func TestShellNotifyLevelRunsWithNotice(t *testing.T) {
	rt := &Runtime{}
	if _, ok := rt.Available(context.Background()); !ok {
		t.Skip("sh not available")
	}
	a := &nexusv1.RunAssignment{RunId: "r1", TaskId: "t1", Prompt: "echo hi",
		Agent: &nexusv1.Agent{Policy: map[string]nexusv1.TrustLevel{"compute": nexusv1.TrustLevel_TRUST_LEVEL_NOTIFY}}}
	r, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: a, Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: "apr_x"}}); err == nil {
		t.Error("resolve without pending approval should fail")
	}
	out, task, evs := drain(t, r)
	if evs[0].GetNotice() == nil || task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || out != "hi\n" {
		t.Fatalf("events %v", evs)
	}
}
