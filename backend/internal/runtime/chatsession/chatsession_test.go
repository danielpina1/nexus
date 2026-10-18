package chatsession

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

func TestStoreKeepsLastTurns(t *testing.T) {
	s := Open(t.TempDir())
	k := Key("codex", "assistant", "c1")
	for i := 0; i < MaxTurns+3; i++ {
		if err := s.Record(k, "", "", "", Turn{User: strings.Repeat("x", i+1), Assistant: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(k, "sess", "/d", "m", Turn{}); err != nil {
		t.Fatal(err)
	}
	e := s.Get(k)
	if len(e.Turns) != MaxTurns || len(e.Turns[0].User) != 4 || e.SessionID != "sess" || e.Dir != "/d" || e.Model != "m" {
		t.Fatalf("entry %+v", e)
	}
	if err := s.Forget(k); err != nil || s.Get(k).SessionID != "" || len(s.Get(k).Turns) != MaxTurns {
		t.Fatalf("forget: %v %+v", err, s.Get(k))
	}
	if got := s.Get(Key("codex", "assistant", "other")); got.SessionID != "" || len(got.Turns) != 0 {
		t.Fatalf("other %+v", got)
	}
}

func TestPrompt(t *testing.T) {
	ctx, _ := structpb.NewStruct(map[string]any{"chat": true, "conversation_id": "c1", "nexus_status": "Agents (1)", "tier": "medium"})
	a := &nexusv1.RunAssignment{Prompt: "hi", Context: ctx}
	if !IsChat(a) || ConversationID(a) != "c1" {
		t.Fatal("chat context not detected")
	}
	p := Prompt(a, []Turn{{User: "a", Assistant: "b"}})
	for _, want := range []string{"<nexus_status>\nAgents (1)", "User: a\nYou: b", "</earlier_conversation>\n\nhi"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "medium") {
		t.Errorf("generic context leaked: %s", p)
	}
}
