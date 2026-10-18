package command

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// countingRouter fails the test when chat calls the router.
type countingRouter struct {
	kernel.Router
	calls int
}

func (c *countingRouter) Route(ctx context.Context, in string, a []*nexusv1.Attachment, cc *nexusv1.CommandContext) (*nexusv1.Route, error) {
	c.calls++
	return c.Router.Route(ctx, in, a, cc)
}

func chatFixture(t *testing.T, o opts) (*fixture, *countingRouter) {
	t.Helper()
	f := newFixture(t, o)
	f.env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(
		taskstest.Agent("assistant", "Assistant", "general-purpose assistant", nexusv1.Tier_TIER_MEDIUM),
		taskstest.Agent("grocery", "Grocery", "buys groceries", nexusv1.Tier_TIER_MEDIUM),
		taskstest.Agent("coder", "Code fixer", "fixes failing tests", nexusv1.Tier_TIER_HEAVY),
	))
	cr := &countingRouter{Router: f.svc.router}
	f.svc.router = cr
	return f, cr
}

func chatReq(input string) *nexusv1.ExecuteRequest {
	return &nexusv1.ExecuteRequest{Input: input, Context: &nexusv1.CommandContext{Source: SourceChat, ConversationId: "conv-1"}}
}

func TestChatGoesToAssistantWithoutRouter(t *testing.T) {
	f, cr := chatFixture(t, opts{})
	// "buy groceries" would match the router's grocery rule; chat ignores it.
	resp := f.exec(t, chatReq("What can you do? Also, buy groceries"))
	if resp.Task == nil || resp.Task.AgentId != "assistant" || resp.Task.Source != SourceChat {
		t.Fatalf("resp = %+v", resp)
	}
	if cr.calls != 0 {
		t.Fatalf("router called %d times", cr.calls)
	}
	ctx := f.disp.LastAssignment().Context.Fields
	if ctx[ParamConversationID].GetStringValue() != "conv-1" || !ctx[ParamChat].GetBoolValue() {
		t.Fatalf("context = %v", ctx)
	}
	st := ctx[ParamNexusStatus].GetStringValue()
	for _, want := range []string{"Agents (3", "- assistant: general-purpose assistant", "- grocery: buys groceries", "Running tasks"} {
		if !strings.Contains(st, want) {
			t.Errorf("status lacks %q:\n%s", want, st)
		}
	}
	if len(st) > maxStatusChars+10 {
		t.Errorf("status too long: %d", len(st))
	}
}

func TestChatMention(t *testing.T) {
	f, cr := chatFixture(t, opts{})
	resp := f.exec(t, chatReq("@coder fix the flaky test"))
	if resp.Task == nil || resp.Task.AgentId != "coder" || resp.Task.Prompt != "fix the flaky test" {
		t.Fatalf("resp = %+v", resp)
	}
	_, err := f.cli.Execute(context.Background(), connect.NewRequest(chatReq("@nobody hi")))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown agent err = %v", err)
	}
	if cr.calls != 0 {
		t.Fatalf("router called %d times", cr.calls)
	}
}

func TestChatSearchAndRemember(t *testing.T) {
	f, cr := chatFixture(t, opts{vault: true})
	f.vault.hits = []*nexusv1.SearchHit{{Note: &nexusv1.NoteRef{Id: "p", Path: "people/ana.md", Title: "Ana"}, Snippet: "May 3"}}
	resp := f.exec(t, chatReq("/search Ana's birthday"))
	if resp.Route.Kind != kindSearch || resp.Task != nil || len(resp.Sources) != 1 {
		t.Fatalf("search resp = %+v", resp)
	}
	for _, in := range []string{"/remember the wifi is hunter2", "remember that the wifi is hunter2", "Remember the wifi is hunter2"} {
		resp = f.exec(t, chatReq(in))
		if resp.Route.Kind != kindCapture || resp.Task != nil {
			t.Fatalf("%q resp = %+v", in, resp)
		}
		if got := f.vault.captured[len(f.vault.captured)-1].Text; got != "the wifi is hunter2" {
			t.Fatalf("%q captured %q", in, got)
		}
	}
	// A question that merely mentions searching still goes to the assistant.
	resp = f.exec(t, chatReq("can you search my notes for Ana?"))
	if resp.Task == nil || resp.Task.AgentId != "assistant" {
		t.Fatalf("resp = %+v", resp)
	}
	if cr.calls != 0 {
		t.Fatalf("router called %d times", cr.calls)
	}
}

// The command bar keeps using the router.
func TestCommandBarStillRouted(t *testing.T) {
	f, cr := chatFixture(t, opts{})
	resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "buy groceries", Context: &nexusv1.CommandContext{Source: "command-bar"}})
	if cr.calls != 1 || resp.Task == nil || resp.Task.AgentId != "grocery" {
		t.Fatalf("calls %d resp %+v", cr.calls, resp)
	}
	if f.disp.LastAssignment().Context.Fields[ParamNexusStatus] != nil {
		t.Fatal("status injected outside chat")
	}
}
