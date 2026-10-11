package openai

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/runtime/agentloop"
	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

type fakeLLM struct {
	provider  string
	providers map[string]bool
	gotModel  chan string
	gotPrompt chan string
}

func (f *fakeLLM) Chat(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	return f.ChatStream(ctx, req, nil)
}

func (f *fakeLLM) ChatStream(_ context.Context, req kernel.ChatRequest, onDelta func(string)) (*kernel.ChatResponse, error) {
	f.gotModel <- f.provider + ":" + req.Model
	f.gotPrompt <- req.Messages[len(req.Messages)-1].Content
	if onDelta != nil {
		onDelta("ok")
	}
	return &kernel.ChatResponse{Message: kernel.ChatMessage{Role: "assistant", Content: "ok"}}, nil
}

func (f *fakeLLM) Embed(context.Context, string, []string) ([][]float32, error) { return nil, nil }

func (f *fakeLLM) WithDefaultProvider(name string) kernel.LLM {
	cp := *f
	cp.provider = name
	return &cp
}

func (f *fakeLLM) HasProvider(name string) bool { return f.providers[name] }

func setup(t *testing.T, l kernel.LLM) *kerneltest.Host {
	t.Helper()
	h := kerneltest.New(t)
	h.SideValue = kernel.SideNode
	if l != nil {
		h.Provide(kernel.SvcLLM, l)
	}
	if err := (&Module{}).Init(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRuntimes(t *testing.T) {
	tests := []struct {
		runtime, agentModel, wantModel string
	}{
		{"openai", "best", ":best"},
		{"openai", "", ":smart"},
		{"ollama", "", "ollama:fast-local"},
		{"ollama", "llama3", "ollama:llama3"},
	}
	for _, tt := range tests {
		t.Run(tt.runtime+"/"+tt.agentModel, func(t *testing.T) {
			f := &fakeLLM{providers: map[string]bool{"ollama": true}, gotModel: make(chan string, 4), gotPrompt: make(chan string, 4)}
			h := setup(t, f)
			rt, ok := h.Runtimes().Get(tt.runtime)
			if !ok {
				t.Fatal("runtime not registered")
			}
			if v, ok := rt.Available(context.Background()); !ok || v != "native" {
				t.Fatalf("available %v %v", v, ok)
			}
			if !rt.Capabilities().Steering || !rt.Capabilities().Streaming {
				t.Error("capabilities")
			}
			run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: &nexusv1.RunAssignment{
				RunId: "run_1", TaskId: "tsk_1", Prompt: "hello",
				Agent: &nexusv1.Agent{Id: "a", Model: tt.agentModel, Limits: &nexusv1.AgentLimits{MaxRuntimeSeconds: 60}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			var last *nexusv1.Event
			timeout := time.After(5 * time.Second)
		loop:
			for {
				select {
				case ev, ok := <-run.Events():
					if !ok {
						break loop
					}
					last = ev
				case <-timeout:
					t.Fatal("timeout")
				}
			}
			if got := <-f.gotModel; got != tt.wantModel {
				t.Errorf("model %q want %q", got, tt.wantModel)
			}
			task := last.GetTaskUpdated().GetTask()
			if task.GetState() != nexusv1.TaskState_TASK_STATE_SUCCEEDED || task.GetSummary() != "ok" {
				t.Fatalf("task %v", task)
			}
		})
	}
}

func TestUnavailable(t *testing.T) {
	h := setup(t, nil)
	rt, _ := h.Runtimes().Get("openai")
	if _, ok := rt.Available(context.Background()); ok {
		t.Fatal("should be unavailable without LLM")
	}
	if _, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: &nexusv1.RunAssignment{}}); err == nil {
		t.Fatal("start should fail without LLM")
	}
	// ollama without an ollama provider.
	h2 := setup(t, &fakeLLM{providers: map[string]bool{}})
	rt2, _ := h2.Runtimes().Get("ollama")
	if _, ok := rt2.Available(context.Background()); ok {
		t.Fatal("ollama should be unavailable without provider")
	}
}

func TestBuildPrompt(t *testing.T) {
	ctxStruct, _ := structpb.NewStruct(map[string]any{"list": []any{"milk"}})
	p := agentloop.BuildPrompt(&nexusv1.RunAssignment{
		Prompt:  "buy groceries",
		Context: ctxStruct,
		Attachments: []*nexusv1.Attachment{
			{Name: "list.txt", MimeType: "text/plain", Data: []byte("eggs\nbread")},
			{Name: "photo.jpg", MimeType: "image/jpeg", Data: []byte{0xff, 0xd8}},
		},
	})
	for _, want := range []string{"buy groceries", `"milk"`, "list.txt", "eggs\nbread", "photo.jpg (image/jpeg)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "<attachment name=\"photo.jpg\"") {
		t.Error("binary attachment inlined")
	}
}
