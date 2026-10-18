package command

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/architect"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
)

type fakeDrafter struct{ inputs []string }

func (f *fakeDrafter) DraftFromCommand(_ context.Context, input string, _ *nexusv1.CommandContext) (*nexusv1.AgentDraft, error) {
	f.inputs = append(f.inputs, input)
	return &nexusv1.AgentDraft{Id: "drf_1", State: nexusv1.DraftState_DRAFT_STATE_DRAFTING}, nil
}

// When the learning router finds no fitting agent, the choices include
// "Create a new agent for this"; picking it starts an architect draft.
func TestNoFitOffersArchitect(t *testing.T) {
	env := taskstest.New(t)
	params, _ := structpb.NewStruct(map[string]any{"no_fit": true})
	rt := &taskstest.Router{Result: &nexusv1.Route{Kind: kindTask, AgentId: "coder", Confidence: 0.1, Params: params}}
	env.Host.Provide("agents", taskstest.NewCatalog(taskstest.Agent("coder", "Code fixer", "fixes tests", nexusv1.Tier_TIER_HEAVY)))
	svc := New(env.Host, nil, rt, Config{})
	ctx := context.Background()

	// Without the architect module there is no such choice.
	resp, err := svc.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "water my plants"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Msg.Choices {
		if c == architect.ChoiceCreateAgent {
			t.Fatal("offered without the architect")
		}
	}
	d := &fakeDrafter{}
	env.Host.Provide(architect.SvcArchitect, d)
	resp, err = svc.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "water my plants"}))
	if err != nil {
		t.Fatal(err)
	}
	ch := resp.Msg.Choices
	if resp.Msg.Question == "" || len(ch) == 0 || ch[len(ch)-1] != architect.ChoiceCreateAgent {
		t.Fatalf("choices %v", ch)
	}
	resp, err = svc.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "water my plants",
		Route: &nexusv1.Route{Kind: kindTask, AgentId: architect.ChoiceCreateAgent}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.inputs) != 1 || d.inputs[0] != "water my plants" || resp.Msg.Task != nil ||
		resp.Msg.Route.GetParams().GetFields()["architect_draft_id"].GetStringValue() != "drf_1" {
		t.Fatalf("draft not started: %v %+v", d.inputs, resp.Msg)
	}
}
