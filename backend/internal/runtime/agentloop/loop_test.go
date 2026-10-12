package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// step is one scripted model turn.
type step func(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error)

type fakeLLM struct {
	mu    sync.Mutex
	steps []step
	reqs  []kernel.ChatRequest
}

func (f *fakeLLM) next(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	f.mu.Lock()
	cp := req
	cp.Messages = append([]kernel.ChatMessage(nil), req.Messages...)
	f.reqs = append(f.reqs, cp)
	i := len(f.reqs) - 1
	f.mu.Unlock()
	if i >= len(f.steps) {
		return nil, errors.New("script exhausted")
	}
	return f.steps[i](ctx, req)
}

func (f *fakeLLM) Chat(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	return f.next(ctx, req)
}

func (f *fakeLLM) ChatStream(ctx context.Context, req kernel.ChatRequest, onDelta func(string)) (*kernel.ChatResponse, error) {
	resp, err := f.next(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, w := range strings.SplitAfter(resp.Message.Content, " ") {
		onDelta(w)
	}
	return resp, nil
}

func (f *fakeLLM) Embed(context.Context, string, []string) ([][]float32, error) { return nil, nil }

func (f *fakeLLM) requests() []kernel.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kernel.ChatRequest(nil), f.reqs...)
}

func answer(text string, cents int64) step {
	return func(context.Context, kernel.ChatRequest) (*kernel.ChatResponse, error) {
		return &kernel.ChatResponse{
			Message: kernel.ChatMessage{Role: "assistant", Content: text},
			Cost:    &nexusv1.Cost{InputTokens: 10, OutputTokens: 5, Model: "m", Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: cents}},
		}, nil
	}
}

func callTools(calls ...kernel.LLMToolCall) step {
	return func(context.Context, kernel.ChatRequest) (*kernel.ChatResponse, error) {
		return &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", ToolCalls: calls}}, nil
	}
}

func collect(t *testing.T, r *Run) []*nexusv1.Event {
	t.Helper()
	var evs []*nexusv1.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("timeout waiting for events")
		}
	}
}

func terminal(t *testing.T, evs []*nexusv1.Event) *nexusv1.Task {
	t.Helper()
	if len(evs) == 0 || !IsTerminal(evs[len(evs)-1]) {
		t.Fatalf("last event is not terminal: %v", evs)
	}
	for _, ev := range evs[:len(evs)-1] {
		if ev.GetTaskUpdated() != nil {
			t.Fatalf("more than one TaskUpdated")
		}
	}
	return evs[len(evs)-1].GetTaskUpdated().GetTask()
}

var echoTool = kernel.ToolFunc{
	S: kernel.ToolSpec{Name: "memory.search", Description: "Search memory", ActionClass: nexusv1.ActionClass_ACTION_CLASS_READ},
	F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
		var in struct{ Q string }
		if err := kernel.ToolInput(c, &in); err != nil {
			return kernel.ToolResult{}, err
		}
		res, err := kernel.ToolOutput(map[string]any{"hits": []string{in.Q + "!"}, "run": c.RunID})
		res.Artifacts = []*nexusv1.Attachment{{Name: "shot.png"}}
		return res, err
	},
}

var failTool = kernel.ToolFunc{
	S: kernel.ToolSpec{Name: "vault.write", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_INTERNAL},
	F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
		return kernel.ToolResult{}, errors.New("disk full")
	},
}

var panicTool = kernel.ToolFunc{
	S: kernel.ToolSpec{Name: "boom", ActionClass: nexusv1.ActionClass_ACTION_CLASS_COMPUTE},
	F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) { panic("kaboom") },
}

func baseCfg(l kernel.LLM) Config {
	return Config{LLM: l, Model: "smart", Prompt: "do it", Instructions: "Be helpful.", TaskID: "tsk_1", RunID: "run_1", Runtime: "openai"}
}

func TestSimpleAnswer(t *testing.T) {
	f := &fakeLLM{steps: []step{answer("all done here", 3)}}
	r, err := Start(context.Background(), baseCfg(f))
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, r)
	task := terminal(t, evs)
	if task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.Summary != "all done here" {
		t.Fatalf("task %+v", task)
	}
	if task.Cost.GetAmount().GetMinorUnits() != 3 || task.Cost.InputTokens != 10 || task.Cost.Runtime != "openai" {
		t.Fatalf("cost %+v", task.Cost)
	}
	var text strings.Builder
	var sawFinal, sawCost bool
	for _, ev := range evs {
		if o := ev.GetRunOutput(); o != nil {
			text.WriteString(o.Text)
			sawFinal = sawFinal || o.IsFinal
		}
		if ev.GetCost() != nil {
			sawCost = true
		}
		if ev.TaskId != "tsk_1" || ev.RunId != "run_1" {
			t.Fatalf("ids missing on %v", ev)
		}
	}
	if text.String() != "all done here" || !sawFinal || !sawCost {
		t.Fatalf("text %q final %v cost %v", text.String(), sawFinal, sawCost)
	}
	req := f.requests()[0]
	if req.Model != "smart" || req.Messages[0].Role != "system" ||
		!strings.Contains(req.Messages[0].Content, "untrusted") || !strings.Contains(req.Messages[0].Content, "Be helpful.") ||
		req.Messages[1].Content != "do it" {
		t.Fatalf("request %+v", req)
	}
	if err := r.Send(context.Background(), &nexusv1.RunInput{Text: "late"}); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("send after finish: %v", err)
	}
}

func TestToolCallsAndErrors(t *testing.T) {
	f := &fakeLLM{steps: []step{
		callTools(
			kernel.LLMToolCall{ID: "c1", Name: "memory.search", Arguments: `{"q":"milk"}`},
			kernel.LLMToolCall{ID: "c2", Name: "vault.write", Arguments: `{}`},
			kernel.LLMToolCall{ID: "c3", Name: "nope", Arguments: `{}`},
			kernel.LLMToolCall{ID: "c4", Name: "memory.search", Arguments: `{bad`},
			kernel.LLMToolCall{ID: "c5", Name: "boom"},
		),
		answer("finished", 0),
	}}
	cfg := baseCfg(f)
	cfg.Tools = []kernel.Tool{echoTool, failTool, panicTool}
	r, _ := Start(context.Background(), cfg)
	evs := collect(t, r)
	if task := terminal(t, evs); task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("task %+v", task)
	}
	calls := map[string]*nexusv1.ToolCall{}
	results := map[string]*nexusv1.ToolResult{}
	artifacts := 0
	for _, ev := range evs {
		if c := ev.GetToolCall(); c != nil {
			calls[c.CallId] = c
		}
		if res := ev.GetToolResult(); res != nil {
			results[res.CallId] = res
		}
		if ev.GetArtifact() != nil {
			artifacts++
		}
	}
	if len(calls) != 5 || len(results) != 5 || artifacts != 1 {
		t.Fatalf("calls %d results %d artifacts %d", len(calls), len(results), artifacts)
	}
	if calls["c1"].ActionClass != nexusv1.ActionClass_ACTION_CLASS_READ || calls["c1"].Input.Fields["q"].GetStringValue() != "milk" {
		t.Errorf("c1 %v", calls["c1"])
	}
	if results["c1"].Error != "" || results["c1"].Output.Fields["run"].GetStringValue() != "run_1" {
		t.Errorf("r1 %v", results["c1"])
	}
	for id, want := range map[string]string{"c2": "disk full", "c3": "unknown tool", "c4": "not valid JSON", "c5": "panicked"} {
		if !strings.Contains(results[id].Error, want) {
			t.Errorf("%s error %q want %q", id, results[id].Error, want)
		}
	}
	// The second model call sees all five tool results.
	req := f.requests()[1]
	var tools []kernel.ChatMessage
	for _, m := range req.Messages {
		if m.Role == "tool" {
			tools = append(tools, m)
		}
	}
	if len(tools) != 5 || tools[0].ToolCallID != "c1" || !strings.Contains(tools[0].Content, "milk!") ||
		!strings.Contains(tools[1].Content, "disk full") {
		t.Fatalf("tool messages %+v", tools)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(tools[2].Content), &out); err != nil || out["error"] == "" {
		t.Fatalf("error tool content %q", tools[2].Content)
	}
	if len(f.requests()[0].Tools) != 3 {
		t.Fatalf("tool specs not passed")
	}
}

func TestLimits(t *testing.T) {
	loopForever := callTools(kernel.LLMToolCall{ID: "x", Name: "memory.search", Arguments: `{"q":"a"}`})
	costly := func(context.Context, kernel.ChatRequest) (*kernel.ChatResponse, error) {
		return &kernel.ChatResponse{
			Message: kernel.ChatMessage{ToolCalls: []kernel.LLMToolCall{{ID: "x", Name: "memory.search"}}},
			Cost:    &nexusv1.Cost{Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 30}},
		}, nil
	}
	tests := []struct {
		name   string
		steps  []step
		limits Limits
		want   string
	}{
		{"max steps", []step{loopForever, loopForever, loopForever, loopForever}, Limits{MaxSteps: 3}, "step limit"},
		{"max cost", []step{costly, costly, costly}, Limits{MaxCost: &nexusv1.Money{Currency: "EUR", MinorUnits: 50}}, "cost limit"},
		{"model error", []step{func(context.Context, kernel.ChatRequest) (*kernel.ChatResponse, error) {
			return nil, errors.New("HTTP 500")
		}}, Limits{}, "model error: HTTP 500"},
		{"deadline", []step{func(ctx context.Context, _ kernel.ChatRequest) (*kernel.ChatResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}, Limits{Deadline: time.Now().Add(50 * time.Millisecond)}, "deadline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLLM{steps: tt.steps}
			cfg := baseCfg(f)
			cfg.Tools = []kernel.Tool{echoTool}
			cfg.Limits = tt.limits
			r, _ := Start(context.Background(), cfg)
			task := terminal(t, collect(t, r))
			if task.State != nexusv1.TaskState_TASK_STATE_FAILED || !strings.Contains(task.Summary, tt.want) ||
				task.Result.Fields["error"].GetStringValue() == "" {
				t.Fatalf("task %+v", task)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	f := &fakeLLM{steps: []step{func(ctx context.Context, _ kernel.ChatRequest) (*kernel.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}}
	r, _ := Start(context.Background(), baseCfg(f))
	<-started
	if err := r.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := terminal(t, collect(t, r))
	if task.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		t.Fatalf("task %+v", task)
	}
}

func TestSendMidRun(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	f := &fakeLLM{steps: []step{
		func(context.Context, kernel.ChatRequest) (*kernel.ChatResponse, error) {
			close(inFlight)
			<-release
			return &kernel.ChatResponse{Message: kernel.ChatMessage{Content: "first draft"}}, nil
		},
		answer("revised", 0),
	}}
	r, _ := Start(context.Background(), baseCfg(f))
	<-inFlight
	if err := r.Send(context.Background(), &nexusv1.RunInput{QuestionId: "q1", Choice: "B"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	task := terminal(t, collect(t, r))
	if task.Summary != "revised" {
		t.Fatalf("task %+v", task)
	}
	reqs := f.requests()
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "user" || last.Content != "Answer to question q1: B" {
		t.Fatalf("last message %+v", last)
	}
	if reqs[1].Messages[len(reqs[1].Messages)-2].Content != "first draft" {
		t.Fatalf("assistant draft missing")
	}
}

func TestNoStream(t *testing.T) {
	f := &fakeLLM{steps: []step{answer("whole answer", 0)}}
	cfg := baseCfg(f)
	cfg.NoStream = true
	r, _ := Start(context.Background(), cfg)
	evs := collect(t, r)
	var outs []string
	for _, ev := range evs {
		if o := ev.GetRunOutput(); o != nil && !o.IsFinal {
			outs = append(outs, o.Text)
		}
	}
	if len(outs) != 1 || outs[0] != "whole answer" {
		t.Fatalf("outputs %v", outs)
	}
}

func TestStartValidation(t *testing.T) {
	if _, err := Start(context.Background(), Config{Model: "x"}); err == nil {
		t.Fatal("expected error without LLM")
	}
	if _, err := Start(context.Background(), Config{LLM: &fakeLLM{}}); err == nil {
		t.Fatal("expected error without model")
	}
}

func TestFormatInputAndJSONToStruct(t *testing.T) {
	if got := FormatInput(&nexusv1.RunInput{Text: "hello"}); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := FormatInput(&nexusv1.RunInput{QuestionId: "q", Text: "free"}); got != "Answer to question q: free" {
		t.Errorf("got %q", got)
	}
	if s := JSONToStruct(json.RawMessage(`[1,2]`)); s.Fields["value"].GetListValue() == nil {
		t.Errorf("array not wrapped: %v", s)
	}
	if s := JSONToStruct(json.RawMessage(`nope`)); s.Fields["raw"].GetStringValue() != "nope" {
		t.Errorf("raw not kept: %v", s)
	}
	if JSONToStruct(nil) != nil {
		t.Error("nil expected")
	}
}

func TestGatedToolApproveAndDeny(t *testing.T) {
	var invoked []string
	var mu sync.Mutex
	sendTool := kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "email.send", ActionClass: nexusv1.ActionClass_ACTION_CLASS_WRITE_EXTERNAL},
		F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			mu.Lock()
			invoked = append(invoked, c.ID)
			mu.Unlock()
			return kernel.ToolOutput(map[string]bool{"sent": true})
		},
	}
	f := &fakeLLM{steps: []step{
		callTools(kernel.LLMToolCall{ID: "c1", Name: "email.send", Arguments: `{"to":"a@b.c"}`}),
		callTools(kernel.LLMToolCall{ID: "c2", Name: "email.send", Arguments: `{"to":"x@y.z"}`}),
		answer("done", 0),
	}}
	cfg := baseCfg(f)
	cfg.Tools = []kernel.Tool{sendTool}
	cfg.Agent = &nexusv1.Agent{Id: "assistant", Name: "Assistant"}
	r, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var evs []*nexusv1.Event
	decisions := []bool{false, true}
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				break loop
			}
			evs = append(evs, ev)
			if a := ev.GetApproval(); a != nil {
				approve := decisions[0]
				decisions = decisions[1:]
				if a.Payload.Fields["tool"].GetStringValue() != "email.send" || a.Level != nexusv1.TrustLevel_TRUST_LEVEL_APPROVE ||
					a.Title != "Assistant wants to use email.send" || a.AgentId != "assistant" {
					t.Errorf("approval %v", a)
				}
				go func() {
					_ = r.Resolve(context.Background(), &nexusv1.ApprovalResolution{
						Approval: &nexusv1.Approval{Id: a.Id},
						Decision: &nexusv1.SignedApproval{ApprovalId: a.Id, Approve: approve, ActionSha256: a.PayloadHash},
					})
				}()
			}
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	last := evs[len(evs)-1].GetTaskUpdated().GetTask()
	if last.GetState() != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("task %v", last)
	}
	if len(decisions) != 0 {
		t.Fatalf("approvals left unasked: %v", decisions)
	}
	mu.Lock()
	if len(invoked) != 1 || invoked[0] != "c2" {
		t.Fatalf("invoked %v", invoked)
	}
	mu.Unlock()
	var states []nexusv1.TaskState
	for _, ev := range evs {
		if tu := ev.GetTaskUpdated(); tu != nil {
			states = append(states, tu.Task.State)
		}
	}
	want := []nexusv1.TaskState{nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL, nexusv1.TaskState_TASK_STATE_RUNNING,
		nexusv1.TaskState_TASK_STATE_WAITING_APPROVAL, nexusv1.TaskState_TASK_STATE_RUNNING, nexusv1.TaskState_TASK_STATE_SUCCEEDED}
	if len(states) != len(want) {
		t.Fatalf("states %v", states)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("states %v", states)
		}
	}
	// The model saw the denial as the first tool result.
	var tools []kernel.ChatMessage
	for _, m := range f.requests()[2].Messages {
		if m.Role == "tool" {
			tools = append(tools, m)
		}
	}
	if len(tools) != 2 || tools[0].Content != `{"error":"denied by user"}` || !strings.Contains(tools[1].Content, "sent") {
		t.Fatalf("tool messages %+v", tools)
	}
}

func TestGatedToolCancel(t *testing.T) {
	tool := kernel.ToolFunc{
		S: kernel.ToolSpec{Name: "git.push", ActionClass: nexusv1.ActionClass_ACTION_CLASS_CODE_PUSH},
		F: func(context.Context, kernel.ToolCall) (kernel.ToolResult, error) {
			t.Error("tool invoked without approval")
			return kernel.ToolResult{}, nil
		},
	}
	f := &fakeLLM{steps: []step{callTools(kernel.LLMToolCall{ID: "c1", Name: "git.push", Arguments: `{}`})}}
	cfg := baseCfg(f)
	cfg.Tools = []kernel.Tool{tool}
	r, _ := Start(context.Background(), cfg)
	for ev := range r.Events() {
		if ev.GetApproval() != nil {
			_ = r.Cancel(context.Background())
		}
		if IsTerminal(ev) {
			if st := ev.GetTaskUpdated().GetTask().GetState(); st != nexusv1.TaskState_TASK_STATE_CANCELLED {
				t.Fatalf("state %s", st)
			}
		}
	}
}

func TestAgentPolicyTightensTool(t *testing.T) {
	f := &fakeLLM{steps: []step{callTools(kernel.LLMToolCall{ID: "c1", Name: "memory.search", Arguments: `{"q":"x"}`}), answer("ok", 0)}}
	cfg := baseCfg(f)
	cfg.Tools = []kernel.Tool{echoTool}
	cfg.Agent = &nexusv1.Agent{Policy: map[string]nexusv1.TrustLevel{"read": nexusv1.TrustLevel_TRUST_LEVEL_APPROVE}}
	r, _ := Start(context.Background(), cfg)
	asked := false
	for ev := range r.Events() {
		if a := ev.GetApproval(); a != nil {
			asked = true
			go func() {
				_ = r.Resolve(context.Background(), &nexusv1.ApprovalResolution{Approval: &nexusv1.Approval{Id: a.Id, State: nexusv1.ApprovalState_APPROVAL_STATE_APPROVED}})
			}()
		}
	}
	if !asked {
		t.Fatal("agent policy read: approve did not gate memory.search")
	}
}
