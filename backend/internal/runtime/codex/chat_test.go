package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// Codex chat runs carry the conversation's earlier turns in the prompt.
func TestChatHistoryInPrompt(t *testing.T) {
	rt := newRuntime(t, Config{})
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	send := func(runID, prompt string) {
		ctx, _ := structpb.NewStruct(map[string]any{"chat": true, "conversation_id": "c1", "nexus_status": "Agents (1)"})
		a := &nexusv1.RunAssignment{RunId: runID, TaskId: "t" + runID, Prompt: prompt, Context: ctx, Agent: &nexusv1.Agent{Id: "assistant"}}
		run, err := rt.Start(context.Background(), kernel.RunSpec{Assignment: a, Workdir: t.TempDir(), Env: map[string]string{"FAKE_STDIN_FILE": stdinFile}})
		if err != nil {
			t.Fatal(err)
		}
		if task := last(t, collect(t, run)); task.State != nexusv1.TaskState_TASK_STATE_SUCCEEDED {
			t.Fatalf("task %v", task)
		}
	}
	send("1", "My name is Ana")
	in, _ := os.ReadFile(stdinFile)
	if strings.Contains(string(in), "earlier_conversation") || !strings.Contains(string(in), "<nexus_status>") {
		t.Fatalf("first prompt %s", in)
	}
	send("2", "What is my name?")
	in, _ = os.ReadFile(stdinFile)
	if !strings.Contains(string(in), "User: My name is Ana\nYou: Created c.txt.") || !strings.HasSuffix(strings.TrimSpace(string(in)), "What is my name?") {
		t.Fatalf("second prompt %s", in)
	}
}
