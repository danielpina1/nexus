package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/chatsession"
	"github.com/danielpina1/nexus/backend/kernel"
)

func chatAssignment(t *testing.T, runID, prompt, conv string) *nexusv1.RunAssignment {
	t.Helper()
	ctx, err := structpb.NewStruct(map[string]any{
		"chat": true, "conversation_id": conv, "nexus_status": "Agents (2):\n- assistant: helps",
		"task_id": "tsk_" + runID, "tier": "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &nexusv1.RunAssignment{
		RunId: runID, TaskId: "tsk_" + runID, Prompt: prompt, Context: ctx,
		Agent: &nexusv1.Agent{Id: "assistant", Name: "Assistant", Model: "smart"},
	}
}

// outputs returns the non-final RunOutput texts.
func outputs(evs []*nexusv1.Event) []string {
	var out []string
	for _, ev := range evs {
		if o := ev.GetRunOutput(); o != nil && !o.IsFinal {
			out = append(out, o.Text)
		}
	}
	return out
}

func TestPartialMessagesStreamWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: dir,
		Env: map[string]string{"FAKE_CLAUDE_MODE": "partial", "FAKE_ARGS_FILE": argsFile, "FAKE_DELAY": "0.08"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	if task := last(t, evs); task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("task %v", task)
	}
	outs := outputs(evs)
	// Deltas 80 ms apart are flushed separately (no batching delay beyond
	// flushEvery); the subagent delta is ignored; the full messages add only
	// the tail the deltas missed.
	if got := strings.Join(outs, ""); got != "Hello from Nexus\n\nSecond. Plus tail" {
		t.Fatalf("streamed text %q (chunks %q)", got, outs)
	}
	if len(outs) < 3 {
		t.Fatalf("expected incremental chunks, got %q", outs)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--include-partial-messages") {
		t.Fatalf("args %s", args)
	}
}

func TestModelIDFromInit(t *testing.T) {
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: t.TempDir(),
		Env: map[string]string{"FAKE_CLAUDE_MODE": "partial", "FAKE_MODEL": "claude-opus-4-1-20250805"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	var models []string
	for _, ev := range evs {
		if c := ev.GetCost().GetCost(); c != nil {
			if c.Runtime != "claude-code" {
				t.Errorf("cost runtime %q", c.Runtime)
			}
			models = append(models, c.Model)
		}
	}
	// Announced from init before any text, then the model that actually
	// answered (assistant message.model) when it differs; the final cost
	// carries the latter.
	if len(models) != 3 || models[0] != "claude-opus-4-1-20250805" || models[1] != "claude-sonnet-4-5-20250929" || models[2] != "claude-sonnet-4-5-20250929" {
		t.Fatalf("models %v", models)
	}
	if evs[0].GetCost() == nil {
		t.Fatalf("first event %v, want the model announcement", evs[0])
	}
}

func TestChatSessionResume(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stdinFile := filepath.Join(dir, "stdin")
	rt := newRuntime(t, Config{})
	env := map[string]string{"FAKE_CLAUDE_MODE": "partial", "FAKE_ARGS_FILE": argsFile, "FAKE_STDIN_FILE": stdinFile, "FAKE_SESSION": "chat-a"}
	start := func(runID, prompt string) []*nexusv1.Event {
		run, err := rt.Start(context.Background(), kernel.RunSpec{
			Assignment: chatAssignment(t, runID, prompt, "conv-1"), Workdir: t.TempDir(), Env: env,
		})
		if err != nil {
			t.Fatal(err)
		}
		return collect(t, run)
	}

	// First message: fresh session in the stable chat dir, chat prompt.
	evs := start("r1", "What can you do?")
	if last(t, evs).State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatal(evs)
	}
	args1, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args1), "--resume") || !strings.Contains(string(args1), "--strict-mcp-config") {
		t.Fatalf("first run args: %s", args1)
	}
	chatDir := filepath.Join(rt.host.DataDir(), "chat", "claude-code", "assistant")
	if !strings.HasSuffix(strings.TrimSpace(string(args1)), chatDir) {
		t.Fatalf("cwd not the chat dir %s: %s", chatDir, args1)
	}
	in, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(in), "nexus_status") || !strings.Contains(string(in), "What can you do?") || strings.Contains(string(in), "## Context") {
		t.Fatalf("chat prompt %s", in)
	}
	store := chatsession.Open(rt.host.DataDir())
	key := chatsession.Key("claude-code", "assistant", "conv-1")
	if e := store.Get(key); e.SessionID != "sess-chat-a" || len(e.Turns) != 1 || e.Turns[0].User != "What can you do?" || e.Dir != chatDir {
		t.Fatalf("entry %+v", e)
	}

	// Follow-up: --resume the session, no history in the prompt.
	_ = os.Remove(stdinFile)
	evs = start("r2", "And what are you doing now?")
	args2, _ := os.ReadFile(argsFile)
	if v, _ := argValue(strings.Split(strings.TrimSpace(string(args2)), "\n"), "--resume"); v != "sess-chat-a" {
		t.Fatalf("second run args %s", args2)
	}
	if got := strings.Join(outputs(evs), ""); !strings.Contains(got, "(resumed sess-chat-a)") {
		t.Fatalf("second run output %q", got)
	}
	in, _ = os.ReadFile(stdinFile)
	if strings.Contains(string(in), "earlier_conversation") {
		t.Fatalf("resumed run got history: %s", in)
	}
	if e := store.Get(key); len(e.Turns) != 2 {
		t.Fatalf("entry %+v", e)
	}

	// Another conversation does not resume this one.
	run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: chatAssignment(t, "r3", "hi", "conv-2"), Workdir: t.TempDir(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	args3, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args3), "--resume") {
		t.Fatalf("new conversation resumed: %s", args3)
	}
}

func TestChatResumeFallsBackToHistory(t *testing.T) {
	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin")
	argsFile := filepath.Join(dir, "args")
	rt := newRuntime(t, Config{})
	store := chatsession.Open(rt.host.DataDir())
	key := chatsession.Key("claude-code", "assistant", "conv-9")
	chatDir, _ := chatsession.Dir(rt.host.DataDir(), "claude-code", "assistant")
	if err := store.Record(key, "gone", chatDir, "", chatsession.Turn{User: "My name is Ana", Assistant: "Nice to meet you, Ana."}); err != nil {
		t.Fatal(err)
	}
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: chatAssignment(t, "r1", "What is my name?", "conv-9"), Workdir: t.TempDir(),
		Env: map[string]string{"FAKE_CLAUDE_MODE": "partial", "FAKE_STDIN_FILE": stdinFile, "FAKE_ARGS_FILE": argsFile, "FAKE_SESSION": "new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, run)
	if task := last(t, evs); task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("task %v", task)
	}
	in, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(in), "earlier_conversation") || !strings.Contains(string(in), "My name is Ana") {
		t.Fatalf("fresh session prompt lacks history: %s", in)
	}
	args, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args), "--resume") {
		t.Fatalf("fallback still resumes: %s", args)
	}
	if e := store.Get(key); e.SessionID != "sess-new" || len(e.Turns) != 2 {
		t.Fatalf("entry %+v", e)
	}
}

func TestAskUserQuestionBecomesQuestions(t *testing.T) {
	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skip("needs curl")
	}
	dir := t.TempDir()
	respFile := filepath.Join(dir, "resp")
	rt := newRuntime(t, Config{})
	a := assignment()
	a.Agent.Name = "Shopper"
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: a, Workdir: dir,
		Env: map[string]string{"FAKE_CLAUDE_MODE": "ask", "FAKE_RESP_FILE": respFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	var qs []*nexusv1.Question
	var evs []*nexusv1.Event
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				done = true
				break
			}
			evs = append(evs, ev)
			if ev.GetApproval() != nil {
				t.Fatalf("AskUserQuestion became an approval: %v", ev)
			}
			if q := ev.GetQuestion(); q != nil {
				qs = append(qs, q)
				in := &nexusv1.RunInput{QuestionId: q.QuestionId, Choice: "Promotions"}
				if q.MultiSelect {
					in = &nexusv1.RunInput{QuestionId: q.QuestionId, Choice: "Worten, Fnac"}
				}
				if err := run.Send(context.Background(), in); err != nil {
					t.Fatal(err)
				}
			}
		case <-timeout:
			t.Fatalf("timeout; events %v", evs)
		}
	}
	if len(qs) != 2 {
		t.Fatalf("questions %v", qs)
	}
	q := qs[0]
	if q.Text != "Which category?" || q.Header != "Product/category" || !q.AllowFreeText || q.MultiSelect ||
		len(q.Options) != 2 || q.Options[0].Label != "Promotions" || q.Options[0].Description != "Browse current promotions" ||
		strings.Join(q.Choices, ",") != "Promotions,Laptops" {
		t.Fatalf("question %v", q)
	}
	if !qs[1].MultiSelect {
		t.Fatalf("second question %v", qs[1])
	}
	raw, _ := os.ReadFile(respFile)
	text := mcpText(t, raw)
	var d struct {
		Behavior     string `json:"behavior"`
		UpdatedInput struct {
			Questions []map[string]any  `json:"questions"`
			Answers   map[string]string `json:"answers"`
		} `json:"updatedInput"`
	}
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		t.Fatalf("decision %s: %v", text, err)
	}
	if d.Behavior != "allow" || len(d.UpdatedInput.Questions) != 2 ||
		d.UpdatedInput.Answers["Which category?"] != "Promotions" || d.UpdatedInput.Answers["Which shops?"] != "Worten, Fnac" {
		t.Fatalf("decision %+v", d)
	}
}

func TestBackgroundBashDenied(t *testing.T) {
	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skip("needs curl")
	}
	dir := t.TempDir()
	respFile := filepath.Join(dir, "resp")
	rt := newRuntime(t, Config{})
	run, err := rt.Start(context.Background(), kernel.RunSpec{
		Assignment: assignment(), Workdir: dir,
		Env: map[string]string{"FAKE_CLAUDE_MODE": "bgbash", "FAKE_RESP_FILE": respFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range collect(t, run) {
		if ev.GetApproval() != nil {
			t.Fatalf("background bash asked for approval: %v", ev)
		}
	}
	raw, _ := os.ReadFile(respFile)
	text := mcpText(t, raw)
	if !strings.Contains(text, `"behavior":"deny"`) || !strings.Contains(text, "Run commands in the foreground") {
		t.Fatalf("decision %s", text)
	}
}

func TestDescribePermission(t *testing.T) {
	for _, tc := range []struct{ tool, input, want string }{
		{"Bash", `{"command":"git push origin fix/x"}`, "run: git push origin fix/x"},
		{"Edit", `{"file_path":"src/app.ts","old_string":"a\nb\nc","new_string":"x"}`, "edit src/app.ts (+1 −3)"},
		{"Edit", `{"file_path":"src/app.ts","old_string":"a\nkeep","new_string":"b\nc\nkeep"}`, "edit src/app.ts (+2 −1)"},
		{"MultiEdit", `{"file_path":"a.go","edits":[{"old_string":"a","new_string":"b\nc"}]}`, "edit a.go (+2 −1)"},
		{"Write", `{"file_path":"n.md","content":"1\n2\n"}`, "write n.md (2 lines)"},
		{"WebFetch", `{"url":"https://x.dev"}`, "fetch https://x.dev"},
		{"mcp__github__create_issue", `{}`, "use github · create_issue"},
		{"Weird", `{}`, ""},
	} {
		if got := DescribePermission(tc.tool, json.RawMessage(tc.input)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// mcpText extracts the text content of a JSON-RPC tools/call response
// (plain JSON or a single SSE data line).
func mcpText(t *testing.T, raw []byte) string {
	t.Helper()
	s := strings.TrimSpace(string(raw))
	if i := strings.Index(s, "data: "); i >= 0 {
		s = strings.TrimSpace(strings.SplitN(s[i+6:], "\n", 2)[0])
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(s), &resp); err != nil || len(resp.Result.Content) == 0 {
		t.Fatalf("mcp response %s: %v", raw, err)
	}
	return resp.Result.Content[0].Text
}
