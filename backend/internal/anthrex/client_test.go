package anthrex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/anthrex"
	"github.com/danielpina1/nexus/backend/internal/anthrex/anthrextest"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHandshake(t *testing.T) {
	d := anthrextest.Start(t)
	d.SetWindows([]any{map[string]any{"id": uint32(1), "name": "shell", "runtime": "shell", "cwd": "/r", "project": "/r",
		"worktree": nil, "branch": nil, "status": "idle", "tool": nil, "since_secs": 0, "last_output_secs": 0,
		"session_id": nil, "model": nil, "subagents": []any{}, "exit": nil, "kind": "pty", "run": nil, "signals_seen": false}})
	c, err := anthrex.Dial(ctxT(t), anthrex.Options{Socket: d.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.DaemonVersion() != "0.9.10-fake" {
		t.Fatalf("version %q", c.DaemonVersion())
	}
	hello := d.Find("Hello")[0].Body
	if hello["proto_version"] != int64(21) || hello["client"] != "cli" {
		t.Fatalf("hello %#v", hello)
	}
	w, err := c.ListWindows(ctxT(t))
	if err != nil || len(w) != 1 || w[0].Name != "shell" || w[0].Kind != "pty" {
		t.Fatalf("windows %+v %v", w, err)
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	d := anthrextest.Start(t)
	d.Version = 22
	_, err := anthrex.Dial(ctxT(t), anthrex.Options{Socket: d.Path})
	if !errors.Is(err, anthrex.ErrProtoVersion) {
		t.Fatalf("want ErrProtoVersion, got %v", err)
	}
	_, err = anthrex.Dial(ctxT(t), anthrex.Options{Socket: filepath.Join(t.TempDir(), "none.sock")})
	if !errors.Is(err, anthrex.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestStartGoalSubscribeAnswerFinish(t *testing.T) {
	d := anthrextest.Start(t)
	s := anthrex.NewSupervisor(anthrex.Options{Socket: d.Path})
	defer s.Close()
	ctx := ctxT(t)

	res, err := s.StartGoal(ctx, anthrex.StartGoalRequest{Goal: "Add reset", Dir: "/repo", Delivery: "local", Design: "off"})
	if err != nil || res.RunID != "run-fake1" || res.State != "planning" {
		t.Fatalf("start %+v %v", res, err)
	}
	sg := d.Find("Run.StartGoal")[0]
	if sg.ID == 0 || sg.Body["goal"] != "Add reset" || sg.Body["delivery"] != "local" || sg.Body["design"] != "off" {
		t.Fatalf("start goal on the wire %#v", sg)
	}

	snaps, err := s.WatchRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := <-snaps
	if len(first.Runs) != 0 {
		t.Fatalf("first snapshot %+v", first)
	}
	ask := map[string]any{"id": uint64(4), "question": "Which DB?", "options": []any{"postgres", "sqlite"}, "asked_at": uint64(1)}
	d.SetRuns([]any{anthrextest.Run("run-fake1", "running",
		[]any{anthrextest.Task("t1", "Add API", "working", "worker", 100, 20)},
		map[string]any{"orchestrator": map[string]any{
			"route": map[string]any{"runtime": "claude", "model": "opus", "effort": "high"}, "window_id": uint32(3),
			"live": true, "started_at": uint64(1), "plan_submitted": true, "summary": nil, "notes": []any{}, "wakes": uint32(0),
			"ask": ask,
		}})})
	var snap *anthrex.RunsSnapshot
	for snap = range snaps {
		if len(snap.Runs) == 1 {
			break
		}
	}
	r := snap.Run("run-fake1")
	if r == nil || r.Orchestrator == nil || r.Orchestrator.Ask == nil || r.Orchestrator.Ask.ID != 4 || len(r.Orchestrator.Ask.Options) != 2 {
		t.Fatalf("run %+v", r)
	}
	if u := r.Tasks[0].Usage(); u.Input != 100 || u.Output != 20 || r.Tasks[0].Role() != "worker" {
		t.Fatalf("task usage/role %+v %s", u, r.Tasks[0].Role())
	}

	choice := uint32(1)
	if msg, err := s.AnswerAsk(ctx, "run-fake1", 4, &choice); err != nil || msg != "answered" {
		t.Fatalf("answer %q %v", msg, err)
	}
	aa := d.Find("Run.AnswerAsk")[0].Body
	if aa["run_id"] != "run-fake1" || aa["ask"] != int64(4) || aa["choice"] != int64(1) {
		t.Fatalf("answer on the wire %#v", aa)
	}

	f, err := s.Finish(ctx, "run-fake1", anthrex.FinishAccept, "")
	if err != nil || !f.ConfirmNeeded || f.Prompt == "" {
		t.Fatalf("finish 1 %+v %v", f, err)
	}
	f, err = s.Finish(ctx, "run-fake1", anthrex.FinishAccept, "run-fake1")
	if err != nil || !f.Done || f.Message != "merged into main" {
		t.Fatalf("finish 2 %+v %v", f, err)
	}
	fin := d.Find("Run.Finish")
	if len(fin) != 2 || fin[0].Body["confirm"] != nil || fin[1].Body["confirm"] != "run-fake1" || fin[1].Body["action"] != "accept" {
		t.Fatalf("finish on the wire %#v", fin)
	}

	// Refused replies surface as errors.
	d.Handler = func(_ *anthrextest.Daemon, kind string, _ map[string]any) any {
		if kind == "Approve" {
			return map[string]any{"Refused": map[string]any{"request": "run approve", "message": "not awaiting approval"}}
		}
		return nil
	}
	if _, err := s.Approve(ctx, "run-fake1"); err == nil || !strings.Contains(err.Error(), "not awaiting approval") {
		t.Fatalf("refusal: %v", err)
	}

	// Snapshots survive a dropped connection (reconnect + re-subscribe).
	d.DropConnections()
	time.Sleep(50 * time.Millisecond)
	d.SetRuns([]any{anthrextest.Run("run-fake1", "complete", nil, nil)})
	deadline := time.After(8 * time.Second)
	for {
		select {
		case snap, ok := <-snaps:
			if !ok {
				t.Fatal("watch closed")
			}
			if r := snap.Run("run-fake1"); r != nil && r.State == "complete" {
				return
			}
		case <-deadline:
			t.Fatal("no snapshot after reconnect")
		}
	}
}

func TestWindowsPTYAndConversation(t *testing.T) {
	d := anthrextest.Start(t)
	s := anthrex.NewSupervisor(anthrex.Options{Socket: d.Path})
	defer s.Close()
	ctx := ctxT(t)

	id, err := s.CreateWindow(ctx, anthrex.WindowSpec{Runtime: "claude", Cwd: "/repo", Name: "w", InitialPrompt: "hi"})
	if err != nil || id != 7 {
		t.Fatalf("create %d %v", id, err)
	}
	spec := d.Find("CreateWindow")[0].Body["spec"].(map[string]any)
	if spec["runtime"] != "claude" || spec["cwd"] != "/repo" || spec["initial_prompt"] != "hi" || spec["worktree_branch"] != nil {
		t.Fatalf("spec on the wire %#v", spec)
	}

	if err := s.SendInput(ctx, 7, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	in := d.Find("Input")[0].Body
	if b, ok := in["bytes"].([]byte); !ok || string(b) != "ls\r" {
		t.Fatalf("input bytes must travel as bin: %#v", in["bytes"])
	}
	if err := s.SendInput(ctx, 404, []byte("x")); err == nil {
		t.Fatal("input error not reported")
	}

	ch, err := s.StreamWindow(ctx, 7, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ev := <-ch
	if !ev.Snapshot || ev.Cols != 80 || !strings.Contains(string(ev.Bytes), "hello") {
		t.Fatalf("pty snapshot %+v", ev)
	}
	ev = <-ch
	if ev.Snapshot || string(ev.Bytes) != " world" {
		t.Fatalf("pty output %+v", ev)
	}
	ch2, err := s.StreamWindow(ctx, 404, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-ch2; ev.Err == nil {
		t.Fatalf("subscribe error not reported: %+v", ev)
	}

	conv, err := s.Conversation(ctx, 7, "")
	if err != nil || conv.Rev != 3 || len(conv.Turns) != 1 || conv.Turns[0].Blocks[0]["text"] == nil {
		t.Fatalf("conversation %+v %v", conv, err)
	}
}

func TestOpenFallsBackToCLI(t *testing.T) {
	d := anthrextest.Start(t)
	d.Version = 99 // a newer daemon: the socket refuses our Hello
	dir := t.TempDir()
	bin := filepath.Join(dir, "anthrex")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + filepath.Join(dir, "args") + "\n" +
		"case \"$*\" in\n" +
		"  *'run status --json'*) echo '{\"revision\":3,\"runs\":[{\"run_id\":\"r1\",\"goal\":\"g\",\"state\":\"running\",\"tasks\":[]}],\"now\":1}' ;;\n" +
		"  *'run start'*) echo r-new; echo 'watch with: anthrex run status r-new' >&2 ;;\n" +
		"  *'ls --json'*) echo '[{\"id\":2,\"name\":\"x\",\"runtime\":\"codex\",\"cwd\":\"/r\",\"project\":\"/r\",\"status\":\"idle\"}]' ;;\n" +
		"  *'--version'*) echo 'anthrex 0.9.10' ;;\n" +
		"  *'run approve'*) echo 'approved' ;;\n" +
		"  *) echo \"unknown: $*\" >&2; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	api, err := anthrex.Open(ctx, anthrex.OpenOptions{Options: anthrex.Options{Socket: d.Path}, Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	if api.Mode() != "cli" || api.Version() != "anthrex 0.9.10" {
		t.Fatalf("mode %s version %s", api.Mode(), api.Version())
	}
	s, err := api.ListRuns(ctx)
	if err != nil || s.Run("r1") == nil {
		t.Fatalf("runs %+v %v", s, err)
	}
	res, err := api.StartGoal(ctx, anthrex.StartGoalRequest{Goal: "g", Dir: dir, Delivery: "local"})
	if err != nil || res.RunID != "r-new" {
		t.Fatalf("start %+v %v", res, err)
	}
	w, err := api.ListWindows(ctx)
	if err != nil || len(w) != 1 || w[0].Runtime != "codex" {
		t.Fatalf("windows %+v %v", w, err)
	}
	if msg, err := api.Approve(ctx, "r1"); err != nil || msg != "approved" {
		t.Fatalf("approve %q %v", msg, err)
	}
	if _, err := api.AnswerAsk(ctx, "r1", 1, nil); !errors.Is(err, anthrex.ErrUnsupported) {
		t.Fatalf("answer over cli: %v", err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--dir "+dir+" run start --goal g --delivery local") {
		t.Fatalf("cli args:\n%s", args)
	}
	// The socket was refused for its version, and the env carried it.
	if _, err := api.CreateWindow(ctx, anthrex.WindowSpec{Runtime: "shell", Cwd: dir}); err == nil {
		t.Fatal("unknown command must fail")
	}
}
