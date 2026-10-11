package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

const testConfig = `
rules:
  - {pattern: "(?i)\\b(groceries|compras)\\b", agent: grocery}
  - {pattern: "(?i)^journal:", kind: capture}
domains:
  github.com: coder
share_types:
  image: receipts
`

type fixture struct {
	env *taskstest.Env
	r   *Router
	llm *taskstest.LLM
}

func newFixture(t *testing.T, withLLM bool) *fixture {
	t.Helper()
	f := &fixture{env: taskstest.New(t)}
	f.env.Host.ConfigYAML = testConfig
	f.env.Host.Provide(kernel.SvcAgents, taskstest.NewCatalog(
		taskstest.Agent("grocery", "Grocery (Continente)", "Buys groceries from a shopping list", nexusv1.Tier_TIER_MEDIUM),
		taskstest.Agent("coder", "Code fixer", "Fixes failing tests and bugs in repositories", nexusv1.Tier_TIER_HEAVY),
		taskstest.Agent("receipts", "Receipt scanner", "Extracts expenses from receipt photos", nexusv1.Tier_TIER_SIMPLE),
		taskstest.Agent("travel", "Travel planner", "Plans trips, flights and hotels", nexusv1.Tier_TIER_MEDIUM),
	))
	if withLLM {
		f.llm = &taskstest.LLM{}
		f.env.Host.Provide(kernel.SvcLLM, f.llm)
	}
	m := &Module{}
	if err := m.Init(context.Background(), f.env.Host); err != nil {
		t.Fatal(err)
	}
	f.r = m.Router
	return f
}

func (f *fixture) route(t *testing.T, input string, c *nexusv1.CommandContext, atts ...*nexusv1.Attachment) *nexusv1.Route {
	t.Helper()
	r, err := f.r.Route(context.Background(), input, atts, c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRulesStage(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		ctx     *nexusv1.CommandContext
		att     *nexusv1.Attachment
		kind    nexusv1.CommandKind
		agent   string
		minConf float32
	}{
		{name: "regex rule", input: "do the groceries for saturday", kind: kindTask, agent: "grocery", minConf: 0.9},
		{name: "kind-only rule", input: "journal: great day", kind: kindCapture, minConf: 0.9},
		{name: "domain in text", input: "look at https://github.com/foo/bar/pull/1", kind: kindTask, agent: "coder", minConf: 0.9},
		{name: "subdomain attachment", input: "check this", att: &nexusv1.Attachment{Url: "https://gist.github.com/x"}, kind: kindTask, agent: "coder", minConf: 0.9},
		{name: "share type", input: "", ctx: &nexusv1.CommandContext{ShareType: "image"}, kind: kindTask, agent: "receipts", minConf: 0.8},
		{name: "capture phrase", input: "Remember that Ana's birthday is May 3", kind: kindCapture, minConf: 0.9},
		{name: "search heuristic", input: "when did I last renew my passport?", kind: kindSearch, minConf: 0.6},
		{name: "keyword name match", input: "plan a travel to Lisbon with flights and hotels", kind: kindTask, agent: "travel", minConf: 0.6},
		{name: "weak keyword", input: "something about bugs", kind: kindTask, agent: "coder"},
		{name: "nothing", input: "hello there", kind: kindTask},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, false)
			var atts []*nexusv1.Attachment
			if c.att != nil {
				atts = append(atts, c.att)
			}
			r := f.route(t, c.input, c.ctx, atts...)
			if r.Kind != c.kind || r.AgentId != c.agent || r.Confidence < c.minConf {
				t.Fatalf("route = %+v", r)
			}
			if r.Params.GetFields()["decision_id"].GetStringValue() == "" {
				t.Fatal("decision id missing")
			}
		})
	}
}

func TestTierFilled(t *testing.T) {
	f := newFixture(t, false)
	if r := f.route(t, "buy groceries", nil); r.Tier != nexusv1.Tier_TIER_MEDIUM {
		t.Fatalf("tier = %s", r.Tier)
	}
}

func TestLLMStage(t *testing.T) {
	t.Run("confident local model", func(t *testing.T) {
		f := newFixture(t, true)
		f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
			if !req.JSON || req.Model != "fast-local" || !strings.Contains(req.Messages[0].Content, "travel: Travel planner") {
				t.Errorf("unexpected request %+v", req)
			}
			return taskstest.Reply("```json\n{\"kind\":\"task\",\"agent\":\"travel\",\"confidence\":0.92,\"reason\":\"trip\",\"alternatives\":[\"ghost\",\"grocery\"]}\n```"), nil
		}
		r := f.route(t, "get me to Porto next week", nil)
		if r.AgentId != "travel" || r.Confidence < 0.9 || len(r.Alternatives) != 1 || r.Alternatives[0] != "grocery" || r.Tier != nexusv1.Tier_TIER_MEDIUM {
			t.Fatalf("route = %+v", r)
		}
		if f.llm.CallCount() != 1 {
			t.Fatalf("calls = %d", f.llm.CallCount())
		}
	})

	t.Run("low confidence falls back to smart", func(t *testing.T) {
		f := newFixture(t, true)
		f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
			if req.Model == "fast-local" {
				return taskstest.Reply(`{"kind":"task","agent":"coder","confidence":0.3}`), nil
			}
			return taskstest.Reply(`{"kind":"search","agent":"coder","confidence":0.8,"reason":"question"}`), nil
		}
		r := f.route(t, "what was decided in the meeting", nil)
		if r.Kind != kindSearch || r.AgentId != "" || f.llm.CallCount() != 2 {
			t.Fatalf("route = %+v, calls %d", r, f.llm.CallCount())
		}
	})

	t.Run("unknown agent is dropped", func(t *testing.T) {
		f := newFixture(t, true)
		f.llm.Fn = func(req kernel.ChatRequest) (*kernel.ChatResponse, error) {
			return taskstest.Reply(`{"kind":"task","agent":"ghost","confidence":0.99}`), nil
		}
		r := f.route(t, "do a thing", nil)
		if r.AgentId != "" || r.Confidence > 0.3 {
			t.Fatalf("route = %+v", r)
		}
	})

	t.Run("llm error uses rules result", func(t *testing.T) {
		f := newFixture(t, true)
		f.llm.Fn = func(kernel.ChatRequest) (*kernel.ChatResponse, error) { return nil, errors.New("down") }
		r := f.route(t, "fix the failing tests", nil)
		if r.AgentId != "coder" {
			t.Fatalf("route = %+v", r)
		}
	})

	t.Run("strong rule skips llm", func(t *testing.T) {
		f := newFixture(t, true)
		f.route(t, "groceries please", nil)
		if f.llm.CallCount() != 0 {
			t.Fatal("LLM called despite rule match")
		}
	})
}

func TestDecisionLogAndCorrection(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	r := f.route(t, "fix the failing tests", &nexusv1.CommandContext{Source: "cli"})
	id := r.Params.Fields["decision_id"].GetStringValue()
	if err := f.r.LinkTask(ctx, id, "tsk_1"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.RecordCorrection(ctx, Correction{TaskID: "tsk_1", ToAgent: "travel", Comment: "no"}); err != nil {
		t.Fatal(err)
	}
	db := f.env.Store.DB()
	var stage, corrected, src string
	if err := db.QueryRow(`SELECT stage, corrected_agent, json_extract(context, '$.source') FROM router_decisions WHERE id = ?`, id).Scan(&stage, &corrected, &src); err != nil {
		t.Fatal(err)
	}
	if stage != "keyword" || corrected != "travel" || src != "cli" {
		t.Fatalf("decision row = %s %s %s", stage, corrected, src)
	}
	var from, to, input, dec string
	if err := db.QueryRow(`SELECT from_agent, to_agent, input, decision_id FROM router_corrections`).Scan(&from, &to, &input, &dec); err != nil {
		t.Fatal(err)
	}
	if from != "coder" || to != "travel" || input != "fix the failing tests" || dec != id {
		t.Fatalf("correction = %s %s %q %s", from, to, input, dec)
	}
	if err := f.r.RecordCorrection(ctx, Correction{TaskID: "tsk_1"}); err == nil {
		t.Fatal("correction without agent accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []string{
		"rules: [{pattern: '(', agent: x}]",
		"rules: [{pattern: 'x'}]",
		"rules: [{pattern: 'x', kind: dance}]",
	}
	for _, c := range cases {
		env := taskstest.New(t)
		env.Host.ConfigYAML = c
		if err := (&Module{}).Init(context.Background(), env.Host); err == nil {
			t.Errorf("config %q accepted", c)
		}
	}
}
