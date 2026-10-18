package command

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/router"
	"github.com/danielpina1/nexus/backend/internal/tasks"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

type fakeVault struct {
	hits     []*nexusv1.SearchHit
	captured []*nexusv1.CaptureRequest
}

func (v *fakeVault) Read(context.Context, string) (*nexusv1.Note, error) {
	return nil, errors.New("no")
}
func (v *fakeVault) Write(context.Context, string, string, string) (*nexusv1.Note, error) {
	return nil, errors.New("no")
}
func (v *fakeVault) Search(_ context.Context, q string, _ []string, limit int) ([]*nexusv1.SearchHit, error) {
	return v.hits, nil
}
func (v *fakeVault) Graph(context.Context, *nexusv1.GraphRequest) (*nexusv1.GraphResponse, error) {
	return nil, errors.New("no")
}
func (v *fakeVault) Capture(_ context.Context, r *nexusv1.CaptureRequest) (*nexusv1.NoteRef, error) {
	v.captured = append(v.captured, r)
	return &nexusv1.NoteRef{Id: "n1", Path: "inbox/2026-10-11.md", Title: "Capture"}, nil
}
func (v *fakeVault) Root() string { return "" }

type fixture struct {
	env   *taskstest.Env
	svc   *Service
	disp  *taskstest.Dispatcher
	vault *fakeVault
	llm   *taskstest.LLM
	cli   nexusv1connect.CommandServiceClient
}

type opts struct{ vault, llm bool }

func newFixture(t *testing.T, o opts) *fixture {
	t.Helper()
	f := &fixture{env: taskstest.New(t), disp: &taskstest.Dispatcher{}}
	h := f.env.Host
	h.ConfigYAML = "rules:\n  - {pattern: '(?i)groceries', agent: grocery}\n"
	h.Provide(kernel.SvcDispatcher, f.disp)
	h.Provide(kernel.SvcAgents, taskstest.NewCatalog(
		taskstest.Agent("grocery", "Grocery", "buys groceries", nexusv1.Tier_TIER_MEDIUM),
		taskstest.Agent("coder", "Code fixer", "fixes failing tests", nexusv1.Tier_TIER_HEAVY),
	))
	if o.vault {
		f.vault = &fakeVault{}
		h.Provide(kernel.SvcVault, f.vault)
	}
	if o.llm {
		f.llm = &taskstest.LLM{}
		h.Provide(kernel.SvcLLM, f.llm)
	}
	ctx := context.Background()
	for _, m := range []kernel.Module{&tasks.Module{}, &router.Module{}} {
		if err := m.Init(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	h.ConfigYAML = ""
	m := &Module{}
	if err := m.Init(ctx, h); err != nil {
		t.Fatal(err)
	}
	f.svc = m.Service
	srv := httptest.NewServer(h.Mux)
	t.Cleanup(srv.Close)
	f.cli = nexusv1connect.NewCommandServiceClient(srv.Client(), srv.URL)
	return f
}

func (f *fixture) exec(t *testing.T, req *nexusv1.ExecuteRequest) *nexusv1.ExecuteResponse {
	t.Helper()
	resp, err := f.cli.Execute(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return resp.Msg
}

func TestPlan(t *testing.T) {
	f := newFixture(t, opts{})
	resp, err := f.cli.Plan(context.Background(), connect.NewRequest(&nexusv1.PlanRequest{Input: "buy groceries"}))
	if err != nil || resp.Msg.Route.AgentId != "grocery" {
		t.Fatalf("plan = %v, %v", resp, err)
	}
	if len(f.disp.Assigned) != 0 {
		t.Fatal("plan executed something")
	}
}

func TestExecuteTask(t *testing.T) {
	f := newFixture(t, opts{})
	resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "buy groceries for saturday", Context: &nexusv1.CommandContext{Source: "siri"}})
	if resp.Task == nil || resp.Task.AgentId != "grocery" || resp.Task.State != nexusv1.TaskState_TASK_STATE_ASSIGNED || resp.Task.Source != "siri" {
		t.Fatalf("resp = %+v", resp)
	}
	if p := f.disp.LastAssignment().Context.Fields; p["decision_id"] != nil {
		t.Fatal("decision_id leaked into task params")
	}
	var linked string
	if err := f.env.Store.DB().QueryRow(`SELECT task_id FROM router_decisions WHERE agent_id = 'grocery'`).Scan(&linked); err != nil || linked != resp.Task.Id {
		t.Fatalf("decision link = %q, %v", linked, err)
	}
}

func TestExecuteLowConfidenceAsks(t *testing.T) {
	f := newFixture(t, opts{})
	resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "hello there"})
	if resp.Task != nil || resp.Question == "" || len(resp.Choices) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	// The client answers by forcing the route.
	resp = f.exec(t, &nexusv1.ExecuteRequest{Input: "hello there", Route: &nexusv1.Route{AgentId: resp.Choices[1]}})
	if resp.Task == nil || resp.Task.AgentId != "coder" {
		t.Fatalf("forced resp = %+v", resp)
	}
}

func TestExecuteSearch(t *testing.T) {
	hits := []*nexusv1.SearchHit{{Note: &nexusv1.NoteRef{Id: "p", Path: "people/ana.md", Title: "Ana"}, Snippet: "Ana's birthday is May 3"}}
	search := &nexusv1.Route{Kind: kindSearch}

	t.Run("hits only", func(t *testing.T) {
		f := newFixture(t, opts{vault: true})
		f.vault.hits = hits
		resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "when is Ana's birthday?", Route: search})
		if len(resp.Sources) != 1 || resp.Answer != "" || resp.Task != nil {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("llm answer", func(t *testing.T) {
		f := newFixture(t, opts{vault: true, llm: true})
		f.vault.hits = hits
		f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
			if req.Model != "smart" || !strings.Contains(req.Messages[1].Content, "May 3") {
				t.Errorf("request = %+v", req)
			}
			return taskstest.Reply(" May 3 (from Ana) "), nil
		}
		resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "when is Ana's birthday?", Route: search})
		if resp.Answer != "May 3 (from Ana)" || len(resp.Sources) != 1 {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("routed by heuristic", func(t *testing.T) {
		f := newFixture(t, opts{vault: true})
		resp := f.exec(t, &nexusv1.ExecuteRequest{Input: "when did I renew my passport?"})
		if resp.Route.Kind != kindSearch || resp.Answer == "" {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("no vault", func(t *testing.T) {
		f := newFixture(t, opts{})
		_, err := f.cli.Execute(context.Background(), connect.NewRequest(&nexusv1.ExecuteRequest{Input: "x?", Route: search}))
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestExecuteCapture(t *testing.T) {
	f := newFixture(t, opts{vault: true})
	resp := f.exec(t, &nexusv1.ExecuteRequest{
		Input: "remember that the wifi password is hunter2", IdempotencyKey: "c1",
		Attachments: []*nexusv1.Attachment{{Url: "https://example.com/x"}},
		Context:     &nexusv1.CommandContext{Source: "share"},
	})
	if resp.Route.Kind != kindCapture || len(resp.Sources) != 1 || len(f.vault.captured) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	c := f.vault.captured[0]
	if c.Url != "https://example.com/x" || c.Source != "share" || c.IdempotencyKey != "c1" {
		t.Fatalf("capture = %+v", c)
	}
}

func TestExecuteValidation(t *testing.T) {
	f := newFixture(t, opts{})
	ctx := context.Background()
	if _, err := f.cli.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty err = %v", err)
	}
	_, err := f.cli.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "x", Route: &nexusv1.Route{Kind: kindWorkflow}}))
	if connect.CodeOf(err) != connect.CodeUnavailable { // no workflow module in this fixture
		t.Fatalf("workflow err = %v", err)
	}
}

func TestCorrectRoute(t *testing.T) {
	f := newFixture(t, opts{})
	ctx := context.Background()
	first := f.exec(t, &nexusv1.ExecuteRequest{Input: "buy groceries"}).Task
	resp, err := f.cli.CorrectRoute(ctx, connect.NewRequest(&nexusv1.CorrectRouteRequest{TaskId: first.Id, AgentId: "coder", Comment: "wrong"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Task.AgentId != "coder" || resp.Msg.Task.Id == first.Id {
		t.Fatalf("corrected = %+v", resp.Msg.Task)
	}
	var from, to string
	if err := f.env.Store.DB().QueryRow(`SELECT from_agent, to_agent FROM router_corrections WHERE task_id = ?`, first.Id).Scan(&from, &to); err != nil {
		t.Fatal(err)
	}
	if from != "grocery" || to != "coder" {
		t.Fatalf("correction %s → %s", from, to)
	}
	if _, err := f.cli.CorrectRoute(ctx, connect.NewRequest(&nexusv1.CorrectRouteRequest{TaskId: first.Id})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

// hangingLLM is a provider that is cooling down: calls hang until their
// context ends and Available reports false.
type hangingLLM struct{ calls int }

func (l *hangingLLM) Available(string) bool { return false }
func (l *hangingLLM) Chat(ctx context.Context, _ kernel.ChatRequest) (*kernel.ChatResponse, error) {
	l.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}
func (l *hangingLLM) ChatStream(ctx context.Context, req kernel.ChatRequest, _ func(string)) (*kernel.ChatResponse, error) {
	return l.Chat(ctx, req)
}
func (l *hangingLLM) Embed(ctx context.Context, _ string, _ []string) ([][]float32, error) {
	l.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

// The command bar answers at once when the LLM provider is down: routing
// uses the rules result and search returns the hits without an answer.
func TestCommandBarAnswersWhileProviderDown(t *testing.T) {
	f := newFixture(t, opts{vault: true})
	llm := &hangingLLM{}
	f.env.Host.Provide(kernel.SvcLLM, llm)
	f.vault.hits = []*nexusv1.SearchHit{{Note: &nexusv1.NoteRef{Id: "p", Path: "people/ana.md", Title: "Ana"}, Snippet: "Ana's birthday is May 3"}}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := f.cli.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "hello there"}))
	if err != nil {
		t.Fatalf("Execute while provider down: %v (after %v)", err, time.Since(start))
	}
	if resp.Msg.Question == "" && resp.Msg.Task == nil {
		t.Fatalf("resp = %+v", resp.Msg)
	}
	resp, err = f.cli.Execute(ctx, connect.NewRequest(&nexusv1.ExecuteRequest{Input: "when is Ana's birthday?", Route: &nexusv1.Route{Kind: kindSearch}}))
	if err != nil || len(resp.Msg.Sources) != 1 || resp.Msg.Answer != "" {
		t.Fatalf("search resp = %+v, %v", resp, err)
	}
	if took := time.Since(start); took > 2*time.Second || llm.calls != 0 {
		t.Fatalf("took %v with %d provider calls", took, llm.calls)
	}
}
