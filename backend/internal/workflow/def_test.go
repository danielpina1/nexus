package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const templateDir = "../../../vault-template/workflows"

func TestParseTemplates(t *testing.T) {
	ents, err := os.ReadDir(templateDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]*Workflow{}
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(templateDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		w := Parse("workflows/"+e.Name(), b)
		if !w.Valid() {
			t.Errorf("%s: invalid: %v", e.Name(), w.Errors)
		}
		seen[w.ID] = w
	}
	for _, id := range []string{"morning-brief", "groceries", "server-watchdog", "weekly-review"} {
		if seen[id] == nil {
			t.Errorf("template %s missing", id)
		}
	}

	mb := seen["morning-brief"]
	if mb.Enabled {
		t.Error("morning-brief should be disabled in the template")
	}
	if got := mb.StepIDs(); strings.Join(got, ",") != "ops,cal,brief,save,send" {
		t.Errorf("morning-brief steps = %v", got)
	}
	if mb.Triggers[0].Kind != TriggerCron || mb.Triggers[0].Value != "30 7 * * 1-5" || mb.Triggers[0].TZ != "Europe/Lisbon" {
		t.Errorf("morning-brief cron = %+v", mb.Triggers[0])
	}
	if mb.Triggers[1].Kind != TriggerIntent || len(mb.Triggers[1].Phrases) != 3 {
		t.Errorf("morning-brief intent = %+v", mb.Triggers[1])
	}
	if s := mb.Steps[3]; s.Kind != KindTool || s.Tool != "vault.daily_append" || s.With["text"] != "{{ steps.brief.output }}" {
		t.Errorf("save step = %+v", s)
	}
	if s := mb.Steps[2]; s.Kind != KindAgent || s.Model != "fast-local" {
		t.Errorf("brief step = %+v", s)
	}

	g := seen["groceries"]
	if len(g.Inputs) != 2 || g.Inputs[0].Name != "list" || g.Inputs[0].Type != "text" || g.Inputs[0].Required {
		t.Errorf("groceries inputs = %+v", g.Inputs)
	}
	if s := g.Steps[0]; s.Kind != KindAgent || s.If != "inputs.list == ''" {
		t.Errorf("usual step = %+v", s)
	}
	if g.Triggers[1].Kind != "share" {
		t.Errorf("share trigger = %+v", g.Triggers[1])
	}

	sw := seen["server-watchdog"]
	if sw.Triggers[0].Kind != TriggerEvent || sw.Triggers[0].Value != "node.offline" || sw.Triggers[0].Inputs["node"] == nil {
		t.Errorf("watchdog trigger = %+v", sw.Triggers[0])
	}
	wr := seen["weekly-review"]
	if wr.Triggers[0].Value != "0 18 * * 0" || wr.Triggers[0].TZ != "Europe/Lisbon" {
		t.Errorf("weekly-review cron = %+v", wr.Triggers[0])
	}
}

func TestParseKinds(t *testing.T) {
	src := `---
type: workflow
id: kinds
triggers:
  - interval: 15m
  - webhook: { id: gh, secret_env: GH_SECRET }
  - node: offline
  - nfc: tag-123
inputs:
  n: number
  mode: { type: text, default: fast, choices: [fast, slow] }
steps:
  - id: a
    tool: test.read
    with: { x: "{{ inputs.n }}" }
  - id: loop
    foreach: "[1, 2, 3]"
    as: num
    parallel: 2
    do:
      - id: inner
        tool: test.read
        with: { v: "{{ num * 2 }}" }
  - id: par
    parallel:
      - id: b1
        tool: test.read
      - id: b2
        do:
          - id: b2a
            tool: test.read
  - id: check
    if: "steps.a.output != ''"
    then:
      - id: yes
        tool: test.read
    else:
      - id: no
        tool: test.read
  - id: ok
    approve: { title: "Spend {{ inputs.n }}?", action_class: spend, preview: { amount: "{{ inputs.n }}" }, expires: 1h }
  - id: q
    ask: "Which?"
    choices: [a, b]
  - id: w1
    wait: 1s
  - id: w2
    wait: { event: node.online, where: "event.node_status.node_id == 'x'", timeout: 1d }
  - id: sub
    workflow: other
    inputs: { a: 1 }
    on_error: continue
    retries: 2
    timeout: 5m
---
`
	w := Parse("workflows/kinds.md", []byte(src))
	if !w.Valid() {
		t.Fatalf("errors: %v", w.Errors)
	}
	kinds := map[string]string{}
	walk(w.Steps, nil, func(s *Step, _ []string) { kinds[s.ID] = s.Kind })
	want := map[string]string{"a": KindTool, "loop": KindForeach, "inner": KindTool, "par": KindParallel, "b1": KindTool,
		"b2": KindGroup, "b2a": KindTool, "check": KindIf, "yes": KindTool, "no": KindTool, "ok": KindApprove,
		"q": KindAsk, "w1": KindWait, "w2": KindWait, "sub": KindWorkflow}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("%s kind = %q, want %q", id, kinds[id], k)
		}
	}
	if w.Steps[1].Concurrency != 2 || w.Steps[1].As != "num" {
		t.Errorf("foreach = %+v", w.Steps[1])
	}
	if w.Steps[6].Wait.Duration != Duration(time.Second) {
		t.Errorf("w1 = %+v", w.Steps[6].Wait)
	}
	if w.Steps[7].Wait.Timeout != Duration(24*time.Hour) || w.Steps[7].Wait.Event != "node.online" {
		t.Errorf("w2 = %+v", w.Steps[7].Wait)
	}
	if q := w.Steps[5].Ask; q.Question != "Which?" || len(q.Choices) != 2 || q.AllowFreeText {
		t.Errorf("ask = %+v", q)
	}
	if a := w.Steps[4].Approve; a.ActionClass != "spend" || a.Expires != Duration(time.Hour) || a.Preview["amount"] == nil {
		t.Errorf("approve = %+v", a)
	}
	if s := w.Steps[len(w.Steps)-1]; s.OnError != "continue" || s.Retries != 2 || s.Timeout != Duration(5*time.Minute) {
		t.Errorf("sub = %+v", s)
	}
	tr := w.Triggers
	if tr[0].Kind != TriggerInterval || tr[0].Every != Duration(15*time.Minute) {
		t.Errorf("interval = %+v", tr[0])
	}
	if tr[1].Kind != TriggerWebhook || tr[1].Value != "gh" || tr[1].SecretEnv != "GH_SECRET" {
		t.Errorf("webhook = %+v", tr[1])
	}
	if tr[2].Kind != TriggerEvent || tr[2].Value != "node.offline" {
		t.Errorf("node = %+v", tr[2])
	}
	if tr[3].Kind != "nfc" || tr[3].Value != "tag-123" {
		t.Errorf("nfc = %+v", tr[3])
	}
	if w.Inputs[1].Default != "fast" || len(w.Inputs[1].Choices) != 2 {
		t.Errorf("inputs = %+v", w.Inputs)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, steps, want string
	}{
		{"no kind", "  - id: a\n", "no step kind"},
		{"two kinds", "  - id: a\n    tool: x\n    agent: y\n    task: t\n", "more than one step kind"},
		{"dup id", "  - id: a\n    tool: x\n  - id: a\n    tool: y\n", "duplicate id"},
		{"typo", "  - id: a\n    tool: x\n    wiht: {}\n", "field wiht not found"},
		{"bad cel", "  - id: a\n    tool: x\n    with: { y: \"{{ inputs. }}\" }\n", "with"},
		{"bad if", "  - id: a\n    if: \"1 +\"\n    tool: x\n", "if"},
		{"agent no task", "  - id: a\n    agent: x\n", "needs a task"},
		{"missing id", "  - tool: x\n", "missing id"},
		{"wait both", "  - id: a\n    wait: { duration: 1s, event: x }\n", "exactly one"},
		{"foreach no do", "  - id: a\n    foreach: \"[1]\"\n", "needs do"},
		{"then no if", "  - id: a\n    then:\n      - id: b\n        tool: x\n", "without if"},
		{"bad class", "  - id: a\n    approve: { title: t, action_class: nope }\n", "unknown action_class"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Parse("workflows/x.md", []byte("---\ntype: workflow\nid: x\nsteps:\n"+c.steps+"---\n"))
			if w.Valid() || !strings.Contains(strings.Join(w.Errors, "; "), c.want) {
				t.Errorf("errors = %v, want %q", w.Errors, c.want)
			}
		})
	}
	if w := Parse("workflows/x.md", []byte("no frontmatter")); w.Valid() {
		t.Error("missing frontmatter accepted")
	}
	if w := Parse("workflows/x.md", []byte("---\ntype: agent\nid: x\nsteps:\n  - id: a\n    tool: x\n---\n")); w.Valid() {
		t.Error("wrong type accepted")
	}
}

func TestParseTeamStep(t *testing.T) {
	w := Parse("workflows/x.md", []byte(`---
type: workflow
id: x
steps:
  - id: a
    team: team:research-squad
    task: "Research {{ inputs.q }}"
    inputs: { q: "{{ inputs.q }}" }
  - id: b
    team: debate-panel
    prompt: Decide.
---
`))
	if !w.Valid() {
		t.Fatalf("errors: %v", w.Errors)
	}
	if a := w.Steps[0]; a.Kind != KindTeam || a.Team != "research-squad" || a.Inputs["q"] != "{{ inputs.q }}" {
		t.Fatalf("a = %+v", a)
	}
	if b := w.Steps[1]; b.Kind != KindTeam || b.Task != "Decide." {
		t.Fatalf("b = %+v", b)
	}
	bad := Parse("workflows/y.md", []byte(`---
id: y
steps:
  - id: a
    team: squad
  - id: b
    team: squad
    agent: helper
    task: x
  - id: c
    team: squad
    task: x
    model: smart
---
`))
	errs := strings.Join(bad.Errors, ";")
	for _, want := range []string{"team step needs a task", "more than one step kind: agent, team", "model does not apply to a team step"} {
		if !strings.Contains(errs, want) {
			t.Errorf("errors %q lack %q", errs, want)
		}
	}
}

func TestSquadReportTemplate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(templateDir, "squad-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	w := Parse("workflows/squad-report.md", b)
	if !w.Valid() {
		t.Fatalf("errors: %v", w.Errors)
	}
	if s := w.Steps[0]; s.Kind != KindTeam || s.Team != "research-squad" || s.Inputs["question"] == nil || time.Duration(s.Timeout) != 50*time.Minute {
		t.Fatalf("team step = %+v", s)
	}
}
