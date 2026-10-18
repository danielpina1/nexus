package anthrexrt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/anthrex/anthrextest"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

type fixture struct {
	t    *testing.T
	d    *anthrextest.Daemon
	repo string
	rt   *Runtime
	h    *kerneltest.Host
}

func newFixture(t *testing.T, extra string) *fixture {
	t.Helper()
	d := anthrextest.Start(t)
	repo := t.TempDir()
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	h.ConfigYAML = "socket: " + d.Path + "\nmode: socket\nallowed_repos: [" + repo + "]\n" + extra
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return &fixture{t: t, d: d, repo: repo, rt: m.rt, h: h}
}

func (f *fixture) assignment(prompt string, ctx map[string]any) *nexusv1.RunAssignment {
	if ctx == nil {
		ctx = map[string]any{}
	}
	if _, ok := ctx["repo_dir"]; !ok {
		ctx["repo_dir"] = f.repo
	}
	st, err := structpb.NewStruct(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return &nexusv1.RunAssignment{RunId: "run_1", TaskId: "tsk_1", Prompt: prompt, Context: st,
		Agent: &nexusv1.Agent{Id: "anthrex-dev", Runtime: "anthrex"}}
}

// collector drains a run's events.
type collector struct {
	mu   sync.Mutex
	evs  []*nexusv1.Event
	done chan struct{}
	note chan struct{}
}

func collect(run kernel.Run) *collector {
	c := &collector{done: make(chan struct{}), note: make(chan struct{}, 1)}
	go func() {
		defer close(c.done)
		for ev := range run.Events() {
			c.mu.Lock()
			c.evs = append(c.evs, ev)
			c.mu.Unlock()
			select {
			case c.note <- struct{}{}:
			default:
			}
		}
	}()
	return c
}

func (c *collector) wait(t *testing.T, what string, pred func(*nexusv1.Event) bool) *nexusv1.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	seen := 0
	for {
		c.mu.Lock()
		for ; seen < len(c.evs); seen++ {
			if pred(c.evs[seen]) {
				ev := c.evs[seen]
				c.mu.Unlock()
				return ev
			}
		}
		c.mu.Unlock()
		select {
		case <-c.note:
		case <-c.done:
			c.mu.Lock()
			n := len(c.evs)
			c.mu.Unlock()
			if seen == n {
				t.Fatalf("events ended before %s:\n%s", what, c.dump())
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s:\n%s", what, c.dump())
		}
	}
}

func (c *collector) dump() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.evs {
		b.WriteString(ev.String())
		b.WriteString("\n")
	}
	return b.String()
}

func waitReq(t *testing.T, d *anthrextest.Daemon, kind string, n int) []anthrextest.Request {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if r := d.Find(kind); len(r) >= n {
			return r
		}
		select {
		case <-d.Changed():
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no %d× %s request; got %+v", n, kind, d.Requests())
		}
	}
}

func approve(t *testing.T, run kernel.Run, ev *nexusv1.Event, ok bool) {
	t.Helper()
	state := nexusv1.ApprovalState_APPROVAL_STATE_APPROVED
	if !ok {
		state = nexusv1.ApprovalState_APPROVAL_STATE_DENIED
	}
	if err := run.Resolve(context.Background(), &nexusv1.ApprovalResolution{RunId: "run_1",
		Approval: &nexusv1.Approval{Id: ev.GetApproval().GetId(), State: state}}); err != nil {
		t.Fatal(err)
	}
}

func isApproval(class nexusv1.ActionClass) func(*nexusv1.Event) bool {
	return func(ev *nexusv1.Event) bool {
		return ev.GetApproval() != nil && ev.GetApproval().GetActionClass() == class
	}
}

func TestRunLifecycle(t *testing.T) {
	f := newFixture(t, "")
	report := filepath.Join(t.TempDir(), "REPORT.md")
	if err := os.WriteFile(report, []byte("# Report\n\nAdded password reset; 2 tasks merged.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := f.rt.Available(context.Background()); !ok || !strings.Contains(v, "socket") {
		t.Fatalf("available %q %v", v, ok)
	}
	run, err2 := f.rt.Start(context.Background(), kernel.RunSpec{
		Assignment: f.assignment("Add password reset", map[string]any{"anthrex": map[string]any{"design": "full", "delivery": "local"}}),
		Gate:       kernel.NewRunGate(), Env: map[string]string{"NEXUS_NODE_ID": "node_1"},
	})
	if err2 != nil {
		t.Fatal(err2)
	}
	c := collect(run)

	sg := waitReq(t, f.d, "Run.StartGoal", 1)[0].Body
	if sg["goal"] != "Add password reset" || sg["design"] != "full" || sg["delivery"] != "local" || sg["yes"] != false {
		t.Fatalf("StartGoal %#v", sg)
	}
	if dir, _ := sg["dir"].(string); !strings.HasSuffix(dir, filepath.Base(f.repo)) {
		t.Fatalf("dir %v", sg["dir"])
	}
	c.wait(t, "started line", func(ev *nexusv1.Event) bool {
		return strings.Contains(ev.GetRunOutput().GetText(), "run-fake1 started")
	})

	// Plan gate → Nexus approval (COMPUTE raised to APPROVE) → Approve.
	tasks := []any{anthrextest.Task("t1", "Add API", "pending", "", 0, 0), anthrextest.Task("t2", "Add UI", "pending", "", 0, 0)}
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "awaiting_approval", tasks, nil)})
	ev := c.wait(t, "plan approval", isApproval(nexusv1.ActionClass_ACTION_CLASS_COMPUTE))
	if ev.GetApproval().GetLevel() != nexusv1.TrustLevel_TRUST_LEVEL_APPROVE || !strings.Contains(ev.GetApproval().GetSummary(), "t2 [code M] Add UI") {
		t.Fatalf("plan approval %v", ev.GetApproval())
	}
	approve(t, run, ev, true)
	waitReq(t, f.d, "Run.Approve", 1)

	// Running: one ToolCall per task, task transitions as output, usage as cost.
	ask := map[string]any{"id": uint64(4), "question": "Which DB?", "options": []any{"postgres", "sqlite"}, "asked_at": uint64(1)}
	orch := map[string]any{"route": map[string]any{"runtime": "claude", "model": "opus", "effort": "high"}, "window_id": uint32(3),
		"live": true, "started_at": uint64(1), "plan_submitted": true, "summary": nil, "notes": []any{}, "wakes": uint32(0), "ask": ask}
	tasks = []any{anthrextest.Task("t1", "Add API", "working", "worker", 1000, 200), anthrextest.Task("t2", "Add UI", "pending", "", 0, 0)}
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "running", tasks, map[string]any{"orchestrator": orch})})
	tc := c.wait(t, "task tool call", func(ev *nexusv1.Event) bool { return ev.GetToolCall().GetTool() == "anthrex.task" })
	if tc.GetToolCall().GetInput().AsMap()["task_id"] == nil {
		t.Fatalf("tool call %v", tc)
	}
	c.wait(t, "cost", func(ev *nexusv1.Event) bool { return ev.GetCost().GetCost().GetInputTokens() == 1000 })

	// ask_user → Question → AnswerAsk with the option index.
	q := c.wait(t, "question", func(ev *nexusv1.Event) bool { return ev.GetQuestion() != nil })
	if got := q.GetQuestion().GetChoices(); len(got) != 2 || got[1] != "sqlite" {
		t.Fatalf("choices %v", got)
	}
	if err := run.Send(context.Background(), &nexusv1.RunInput{QuestionId: q.GetQuestion().GetQuestionId(), Choice: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	aa := waitReq(t, f.d, "Run.AnswerAsk", 1)[0].Body
	if aa["ask"] != int64(4) || aa["choice"] != int64(1) {
		t.Fatalf("AnswerAsk %#v", aa)
	}

	// A plain user message goes to the live orchestrator's window.
	if err := run.Send(context.Background(), &nexusv1.RunInput{Text: "use sqlite please"}); err != nil {
		t.Fatal(err)
	}
	in := waitReq(t, f.d, "Input", 1)[0].Body
	if b, _ := in["bytes"].([]byte); string(b) != "use sqlite please\r" || in["window_id"] != int64(3) {
		t.Fatalf("input %#v", in)
	}

	// Complete → CODE_PUSH approval → Finish (ConfirmNeeded) → Finish(confirm).
	tasks = []any{anthrextest.Task("t1", "Add API", "merged", "worker", 1500, 300), anthrextest.Task("t2", "Add UI", "merged", "worker", 500, 100)}
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "complete", tasks, map[string]any{"report_path": report})})
	ev = c.wait(t, "accept approval", isApproval(nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH))
	if s := ev.GetApproval().GetSummary(); !strings.Contains(s, "Merge anthrex/run-fake1/integration into main") || !strings.Contains(s, "2/2 tasks done") || !strings.Contains(s, "Added password reset") {
		t.Fatalf("accept summary:\n%s", s)
	}
	if p := ev.GetApproval().GetPayload().AsMap(); p["action"] != "accept" || p["base_sha"] != "b0b0b0b" {
		t.Fatalf("payload %v", p)
	}
	approve(t, run, ev, true)
	fin := waitReq(t, f.d, "Run.Finish", 2)
	if fin[0].Body["confirm"] != nil || fin[1].Body["confirm"] != "run-fake1" || fin[1].Body["action"] != "accept" {
		t.Fatalf("finish %#v", fin)
	}

	// Accepted → SUCCEEDED, REPORT.md as summary, artifact and result.
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "accepted", tasks, map[string]any{"report_path": report, "outcome": "merged into main"})})
	end := c.wait(t, "terminal", func(ev *nexusv1.Event) bool {
		s := ev.GetTaskUpdated().GetTask().GetState()
		return s == nexusv1.TaskState_TASK_STATE_SUCCEEDED || s == nexusv1.TaskState_TASK_STATE_FAILED
	})
	task := end.GetTaskUpdated().GetTask()
	if task.GetState() != nexusv1.TaskState_TASK_STATE_SUCCEEDED || !strings.Contains(task.GetSummary(), "Added password reset") {
		t.Fatalf("terminal %v", task)
	}
	res := task.GetResult().AsMap()
	if res["anthrex_run_id"] != "run-fake1" || res["state"] != "accepted" || !strings.Contains(res["report"].(string), "# Report") {
		t.Fatalf("result %v", res)
	}
	if task.GetCost().GetInputTokens() != 2000 || task.GetCost().GetOutputTokens() != 400 {
		t.Fatalf("cost %v", task.GetCost())
	}
	c.wait(t, "artifact", func(ev *nexusv1.Event) bool { return ev.GetArtifact().GetAttachment().GetName() == "REPORT.md" })
	<-c.done
}

func TestDocGateDeniedAsksForChanges(t *testing.T) {
	f := newFixture(t, "")
	run, err := f.rt.Start(context.Background(), kernel.RunSpec{Assignment: f.assignment("Design a thing", nil)})
	if err != nil {
		t.Fatal(err)
	}
	c := collect(run)
	waitReq(t, f.d, "Run.StartGoal", 1)
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "specifying", nil, map[string]any{"design": "full",
		"doc_gate": map[string]any{"kind": "spec", "version": uint32(2), "revising": nil, "disputed": []any{}, "changes_summary": []any{"+ R4"}, "same_runtime": false}})})
	ev := c.wait(t, "doc approval", isApproval(nexusv1.ActionClass_ACTION_CLASS_COMPUTE))
	if !strings.Contains(ev.GetApproval().GetSummary(), "# Spec") {
		t.Fatalf("doc text missing: %s", ev.GetApproval().GetSummary())
	}
	if sd := waitReq(t, f.d, "Run.ShowDoc", 1)[0].Body; sd["kind"] != "spec" || sd["version"] != int64(2) {
		t.Fatalf("ShowDoc %#v", sd)
	}
	approve(t, run, ev, false)
	q := c.wait(t, "what-now question", func(ev *nexusv1.Event) bool { return ev.GetQuestion() != nil })
	if err := run.Send(context.Background(), &nexusv1.RunInput{QuestionId: q.GetQuestion().GetQuestionId(), Text: "split R4 in two"}); err != nil {
		t.Fatal(err)
	}
	dg := waitReq(t, f.d, "Run.DocGate", 1)[0].Body
	act, _ := dg["action"].(map[string]any)
	ch, _ := act["changes"].(map[string]any)
	if dg["kind"] != "spec" || ch["note"] != "split R4 in two" || ch["review"] != false {
		t.Fatalf("DocGate %#v", dg)
	}
	// Cancel → Anthrex Cancel and a CANCELLED terminal.
	if err := run.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitReq(t, f.d, "Run.Cancel", 1)
	c.wait(t, "cancelled", func(ev *nexusv1.Event) bool {
		return ev.GetTaskUpdated().GetTask().GetState() == nexusv1.TaskState_TASK_STATE_CANCELLED
	})
	<-c.done
}

func TestStartRefusesOutsideAllowedRepos(t *testing.T) {
	f := newFixture(t, "")
	_, err := f.rt.Start(context.Background(), kernel.RunSpec{Assignment: f.assignment("x", map[string]any{"repo_dir": t.TempDir()})})
	if err == nil || !strings.Contains(err.Error(), "not under anthrex.allowed_repos") {
		t.Fatalf("got %v", err)
	}
	cfg := Config{}
	cfg.defaults()
	if _, err := cfg.AllowedRepo("/tmp"); !errors.Is(err, ErrNoRepos) {
		t.Fatalf("no repos: %v", err)
	}
	// ../ escapes are resolved before the check.
	if _, err := f.rt.cfg.AllowedRepo(filepath.Join(f.repo, "..")); err == nil {
		t.Fatal("parent dir accepted")
	}
	if got, err := f.rt.cfg.AllowedRepo(""); err != nil || !strings.HasSuffix(got, filepath.Base(f.repo)) {
		t.Fatalf("single allowed repo default: %q %v", got, err)
	}
}

func TestPlanApprovalAuto(t *testing.T) {
	f := newFixture(t, "plan_approval: auto\ndefault_orchestrator: codex:gpt-5\n")
	run, err := f.rt.Start(context.Background(), kernel.RunSpec{Assignment: f.assignment("Fix bug", nil)})
	if err != nil {
		t.Fatal(err)
	}
	c := collect(run)
	sg := waitReq(t, f.d, "Run.StartGoal", 1)[0].Body
	if sg["yes"] != true || sg["orchestrator"].(map[string]any)["runtime"] != "codex" {
		t.Fatalf("StartGoal %#v", sg)
	}
	f.d.SetRuns([]any{anthrextest.Run("run-fake1", "failed", nil, map[string]any{"outcome": "checks never passed"})})
	end := c.wait(t, "failed", func(ev *nexusv1.Event) bool {
		return ev.GetTaskUpdated().GetTask().GetState() == nexusv1.TaskState_TASK_STATE_FAILED
	})
	if r := end.GetTaskUpdated().GetTask().GetResult().AsMap(); r["error"] != "checks never passed" {
		t.Fatalf("result %v", r)
	}
}

func callTool(t *testing.T, h *kerneltest.Host, name string, in any) (map[string]any, error) {
	t.Helper()
	tool, ok := h.Tools().Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	b, _ := json.Marshal(in)
	res, err := tool.Invoke(context.Background(), kernel.ToolCall{Input: b})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func TestTools(t *testing.T) {
	f := newFixture(t, "")
	want := map[string]nexusv1.ActionClass{
		"anthrex.status": nexusv1.ActionClass_ACTION_CLASS_READ, "anthrex.start_goal": nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL,
		"anthrex.message": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, "anthrex.approve": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL,
		"anthrex.answer": nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL, "anthrex.accept": nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH,
		"anthrex.cancel": nexusv1.ActionClass_ACTION_CLASS_COMPUTE, "anthrex.windows": nexusv1.ActionClass_ACTION_CLASS_READ,
		"anthrex.new_window": nexusv1.ActionClass_ACTION_CLASS_COMPUTE, "anthrex.send_input": nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL,
		"anthrex.conversation": nexusv1.ActionClass_ACTION_CLASS_READ, "anthrex.profile": nexusv1.ActionClass_ACTION_CLASS_READ,
		"anthrex.stats": nexusv1.ActionClass_ACTION_CLASS_READ,
	}
	for name, class := range want {
		tool, ok := f.h.Tools().Get(name)
		if !ok || tool.Spec().ActionClass != class || !json.Valid(tool.Spec().InputSchema) {
			t.Errorf("%s: %v", name, ok)
		}
	}

	out, err := callTool(t, f.h, "anthrex.start_goal", map[string]any{"goal": "Refactor", "design": "full"})
	if err != nil || out["run_id"] != "run-fake1" {
		t.Fatalf("start_goal %v %v", out, err)
	}
	if _, err := callTool(t, f.h, "anthrex.start_goal", map[string]any{"goal": "x", "repo": "/etc"}); err == nil {
		t.Fatal("start_goal outside allowed repos")
	}

	ask := map[string]any{"id": uint64(9), "question": "Go?", "options": []any{"yes", "no"}, "asked_at": uint64(1)}
	orch := map[string]any{"route": map[string]any{"runtime": "claude", "model": "opus"}, "window_id": uint32(3), "live": true,
		"started_at": uint64(1), "plan_submitted": true, "summary": nil, "notes": []any{}, "wakes": uint32(0), "ask": ask}
	f.d.SetRuns([]any{
		anthrextest.Run("run-abc123", "awaiting_approval", []any{anthrextest.Task("t1", "A", "pending", "", 0, 0)},
			map[string]any{"orchestrator": orch}),
		anthrextest.Run("run-zzz999", "complete", nil, map[string]any{"base_moved": map[string]any{"from": "b0b0b0b", "to": "d4d4d4d4d4", "commits": []any{"d4d4d4d bob: hotfix"}, "total": uint32(1)}}),
	})
	out, err = callTool(t, f.h, "anthrex.status", map[string]any{})
	if err != nil || len(out["runs"].([]any)) != 2 {
		t.Fatalf("status %v %v", out, err)
	}
	first := out["runs"].([]any)[0].(map[string]any)
	if p := first["pending"].([]any); len(p) != 2 {
		t.Fatalf("pending %v", p)
	}
	out, err = callTool(t, f.h, "anthrex.status", map[string]any{"run_id": "abc123"})
	if err != nil || out["run"].(map[string]any)["run_id"] != "run-abc123" {
		t.Fatalf("status one %v %v", out, err)
	}
	if _, err := callTool(t, f.h, "anthrex.approve", map[string]any{"run_id": "run-abc123"}); err != nil {
		t.Fatal(err)
	}
	waitReq(t, f.d, "Run.Approve", 1)
	if _, err := callTool(t, f.h, "anthrex.answer", map[string]any{"run_id": "run-abc123", "choice": "2"}); err != nil {
		t.Fatal(err)
	}
	if aa := waitReq(t, f.d, "Run.AnswerAsk", 1)[0].Body; aa["choice"] != int64(1) || aa["ask"] != int64(9) {
		t.Fatalf("answer %#v", aa)
	}
	if _, err := callTool(t, f.h, "anthrex.message", map[string]any{"run_id": "run-abc123", "to": "t1", "text": "mind the cache", "kind": "change"}); err != nil {
		t.Fatal(err)
	}
	edits := waitReq(t, f.d, "Run.Edit", 1)[0].Body["edits"].([]any)
	if e := edits[0].(map[string]any); e["op"] != "message" || e["kind"] != "change" || e["to"].([]any)[0] != "t1" {
		t.Fatalf("message edit %#v", e)
	}

	// accept with a moved base: refused with the new head, then confirmed.
	f.d.Handler = func(_ *anthrextest.Daemon, kind string, b map[string]any) any {
		if kind == "Finish" && b["confirm"] == nil {
			return map[string]any{"ConfirmNeeded": map[string]any{"run_id": b["run_id"], "prompt": "merge?",
				"base_moved": map[string]any{"from": "b0b0b0b", "to": "d4d4d4d4d4", "commits": []any{"d4d4d4d bob: hotfix"}, "total": uint32(1)}}}
		}
		return nil
	}
	if _, err := callTool(t, f.h, "anthrex.accept", map[string]any{"run_id": "zzz999"}); err == nil || !strings.Contains(err.Error(), "d4d4d4d4d4") {
		t.Fatalf("accept on moved base: %v", err)
	}
	if out, err := callTool(t, f.h, "anthrex.accept", map[string]any{"run_id": "zzz999", "base": "d4d4d4d"}); err != nil || out["done"] != true {
		t.Fatalf("accept %v %v", out, err)
	}
	fin := waitReq(t, f.d, "Run.Finish", 3)
	if fin[2].Body["confirm"] != "run-zzz999@d4d4d4d4d4" {
		t.Fatalf("confirm %#v", fin[2].Body)
	}
	if _, err := callTool(t, f.h, "anthrex.accept", map[string]any{"run_id": "abc123"}); err == nil {
		t.Fatal("accept of a non-complete run")
	}

	out, err = callTool(t, f.h, "anthrex.new_window", map[string]any{"runtime": "claude", "prompt": "hi", "worktree": "feat/x"})
	if err != nil || out["window_id"] != float64(7) {
		t.Fatalf("new_window %v %v", out, err)
	}
	if _, err := callTool(t, f.h, "anthrex.send_input", map[string]any{"window_id": 7, "text": "ls"}); err != nil {
		t.Fatal(err)
	}
	out, err = callTool(t, f.h, "anthrex.conversation", map[string]any{"window_id": 7})
	if err != nil || len(out["turns"].([]any)) != 1 {
		t.Fatalf("conversation %v %v", out, err)
	}
	if out, err = callTool(t, f.h, "anthrex.profile", map[string]any{}); err != nil || out["result"].(map[string]any)["state"] != "confirmed" {
		t.Fatalf("profile %v %v", out, err)
	}
	if out, err = callTool(t, f.h, "anthrex.stats", map[string]any{}); err != nil {
		t.Fatalf("stats %v %v", out, err)
	}
	if _, err := callTool(t, f.h, "anthrex.windows", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableWithoutDaemon(t *testing.T) {
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	h.ConfigYAML = "socket: " + filepath.Join(t.TempDir(), "none.sock") + "\nbin: /nonexistent/anthrex\n"
	m := &Module{}
	if err := m.Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.rt.Available(context.Background()); ok {
		t.Fatal("available without daemon or binary")
	}
	if _, err := m.rt.Start(context.Background(), kernel.RunSpec{Assignment: &nexusv1.RunAssignment{Prompt: "x"}}); err == nil {
		t.Fatal("start without daemon")
	}
}
