package webtools_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/workflow"
)

const vaultTemplate = "../../../vault-template"

func loadWorkflow(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vaultTemplate, "workflows", name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	w := workflow.Parse("workflows/"+name+".md", b)
	if !w.Valid() {
		t.Fatalf("%s invalid: %v", name, w.Errors)
	}
	return w
}

func findStep(steps []*workflow.Step, id string) *workflow.Step {
	for _, s := range steps {
		if s.ID == id {
			return s
		}
		if f := findStep(s.Do, id); f != nil {
			return f
		}
	}
	return nil
}

func TestResearchAgents(t *testing.T) {
	for _, id := range []string{"researcher", "research-lead", "fact-checker", "news-briefer", "product-researcher"} {
		p := "agents/" + id + ".md"
		b, err := os.ReadFile(filepath.Join(vaultTemplate, p))
		if err != nil {
			t.Fatal(err)
		}
		a, body := agentdef.Parse(p, b)
		if len(a.Errors) > 0 || a.Id != id || a.Runtime != "openai" || body == "" {
			t.Errorf("%s: id=%s runtime=%s errors=%v", id, a.Id, a.Runtime, a.Errors)
		}
		tools := strings.Join(a.Tools, ",")
		if id != "research-lead" && !strings.Contains(tools, "web.search") {
			t.Errorf("%s: tools = %s", id, tools)
		}
	}
}

func TestDeepResearchWorkflow(t *testing.T) {
	w := loadWorkflow(t, "deep-research")
	if got := strings.Join(w.StepIDs(), ","); got != "plan,research,investigate,synth,check,revise,save,send" {
		t.Errorf("steps = %s", got)
	}
	if w.Triggers[0].Kind != workflow.TriggerIntent || len(w.Triggers[0].Phrases) < 5 {
		t.Errorf("triggers = %+v", w.Triggers)
	}
	if len(w.Inputs) != 2 || !w.Inputs[0].Required || w.Inputs[1].Default != "standard" {
		t.Errorf("inputs = %+v", w.Inputs)
	}
	research := findStep(w.Steps, "research")
	if research.Kind != workflow.KindForeach || research.Concurrency != 4 || research.As != "q" {
		t.Errorf("research = %+v", research)
	}

	now := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	plan := "Here is the plan:\nTITLE: Home batteries for a \"3-bed\" house\nSLUG: home-batteries-3-bed\nQ: What do batteries cost?\n  Q:  How efficient are they?\nnoise"
	vars := func(planOut string, steps map[string]any) workflow.Vars {
		all := map[string]any{
			"plan": map[string]any{"output": planOut, "result": map[string]any{}, "state": "succeeded"},
			"research": map[string]any{"output": []any{}, "state": "succeeded", "result": map[string]any{"count": 2, "items": []any{
				map[string]any{"investigate": map[string]any{"output": "### cost\nfindings A [1]", "state": "succeeded"}},
				map[string]any{"investigate": map[string]any{"output": "", "state": "failed"}},
			}}},
			"synth":  map[string]any{"output": "## Summary\nBatteries pay off in 8 years [1].\n\n## Key findings\n- x", "state": "succeeded"},
			"check":  map[string]any{"output": "VERDICT: issues\n| 1 | ... |", "state": "succeeded"},
			"revise": map[string]any{"output": "", "state": "skipped"},
			"save":   map[string]any{"output": map[string]any{"path": "research/2026-10-12-home-batteries-3-bed.md"}, "state": "succeeded"},
		}
		for k, v := range steps {
			all[k] = v
		}
		return workflow.Vars{
			"inputs": map[string]any{"question": "Should I buy a home battery?", "depth": "standard"},
			"steps":  all,
			"run":    map[string]any{"id": "wfr_01ABC", "dry_run": false},
			"now":    now,
		}
	}

	qs, err := workflow.Eval(research.Foreach, vars(plan, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(qs, []any{"What do batteries cost?", "How efficient are they?"}) {
		t.Errorf("sub-questions = %#v", qs)
	}
	qs, err = workflow.Eval(research.Foreach, vars("[dry run: agent research-lead]", nil))
	if err != nil || !reflect.DeepEqual(qs, []any{"Should I buy a home battery?"}) {
		t.Errorf("fallback sub-questions = %#v, %v", qs, err)
	}

	synth := findStep(w.Steps, "synth")
	task, err := workflow.RenderString(synth.Task, vars(plan, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "findings A [1]\n\n---\n\n") || !strings.HasPrefix(task, "SYNTHESISE") {
		t.Errorf("synth task = %q", task)
	}

	revise := findStep(w.Steps, "revise")
	if ok, err := workflow.EvalBool(revise.If, vars(plan, nil)); err != nil || !ok {
		t.Errorf("revise guard = %v, %v", ok, err)
	}
	check := findStep(w.Steps, "check")
	v := vars(plan, nil)
	v["inputs"] = map[string]any{"question": "q", "depth": "quick"}
	if ok, err := workflow.EvalBool(check.If, v); err != nil || ok {
		t.Errorf("check guard for quick = %v, %v", ok, err)
	}

	save := findStep(w.Steps, "save")
	out, err := workflow.RenderValue(save.With, vars(plan, nil))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["path"] != "research/2026-10-12-home-batteries-3-bed.md" {
		t.Errorf("path = %q", m["path"])
	}
	content := m["content"].(string)
	for _, want := range []string{"type: research\n", `title: "Home batteries for a \"3-bed\" house"`, `question: "Should I buy a home battery?"`,
		"workflow_run: wfr_01ABC", "## Summary\nBatteries pay off", "## Fact-check\n\nVERDICT: issues"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q:\n%s", want, content)
		}
	}
	// Revised report wins; bad slug falls back to the run id.
	out, err = workflow.RenderValue(save.With, vars("SLUG: Not A Slug!!", map[string]any{
		"revise": map[string]any{"output": "## Summary\nRevised.", "state": "succeeded"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	if m["path"] != "research/2026-10-12-report-wfr-01abc.md" || !strings.Contains(m["content"].(string), "Revised.") ||
		!strings.Contains(m["content"].(string), `title: "Should I buy a home battery?"`) {
		t.Errorf("fallback save = %v", m)
	}

	send := findStep(w.Steps, "send")
	out, err = workflow.RenderValue(send.With, vars(plan, nil))
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	if m["title"] != `Research: Home batteries for a "3-bed" house` {
		t.Errorf("title = %q", m["title"])
	}
	if body := m["body"].(string); !strings.HasPrefix(body, "Batteries pay off in 8 years [1].") || strings.Contains(body, "Key findings") ||
		!strings.Contains(body, "[[research/2026-10-12-home-batteries-3-bed.md]]") {
		t.Errorf("body = %q", body)
	}
}

func TestQuickAnswerWorkflow(t *testing.T) {
	w := loadWorkflow(t, "quick-answer")
	if got := strings.Join(w.StepIDs(), ","); got != "answer,log" {
		t.Errorf("steps = %s", got)
	}
	if s := findStep(w.Steps, "answer"); s.Agent != "researcher" || s.Model != "smart" {
		t.Errorf("answer = %+v", s)
	}
}

func TestWatchTopicWorkflow(t *testing.T) {
	w := loadWorkflow(t, "watch-topic")
	if w.Enabled {
		t.Error("watch-topic should be disabled until the watchlist exists")
	}
	if w.Triggers[0].Kind != workflow.TriggerCron || w.Triggers[0].Value != "0 8 * * *" || w.Triggers[0].TZ != "Europe/Lisbon" {
		t.Errorf("cron = %+v", w.Triggers[0])
	}
	vars := func(scan string, dry bool) workflow.Vars {
		return workflow.Vars{
			"steps": map[string]any{
				"watchlist": map[string]any{"output": map[string]any{"body": "- Go releases"}, "state": "succeeded"},
				"scan":      map[string]any{"output": scan, "state": "succeeded"},
			},
			"run": map[string]any{"id": "wfr_1", "dry_run": dry},
		}
	}
	save, send := findStep(w.Steps, "save"), findStep(w.Steps, "send")
	notable := "NOTABLE: yes\nHEADLINE: Go 1.27 released\n### Go\n- **Go 1.27** — new release. [Go blog](https://go.dev/blog) (2026-10-12)"
	for _, c := range []struct {
		scan           string
		dry            bool
		save, sendWant bool
	}{
		{notable, false, true, true},
		{"NOTABLE: no\nHEADLINE: minor\n### Go\n- minor news", false, true, false},
		{"NOTABLE: no\nHEADLINE: No news\nNothing new.", false, false, false},
		{notable, true, false, false},
	} {
		if ok, err := workflow.EvalBool(save.If, vars(c.scan, c.dry)); err != nil || ok != c.save {
			t.Errorf("save guard (%q, dry=%v) = %v, %v", c.scan, c.dry, ok, err)
		}
		if ok, err := workflow.EvalBool(send.If, vars(c.scan, c.dry)); err != nil || ok != c.sendWant {
			t.Errorf("send guard (%q, dry=%v) = %v, %v", c.scan, c.dry, ok, err)
		}
	}
	out, err := workflow.RenderValue(send.With, vars(notable, false))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["title"] != "Go 1.27 released" || strings.Contains(m["body"].(string), "NOTABLE") || !strings.HasPrefix(m["body"].(string), "### Go") {
		t.Errorf("send = %v", m)
	}
	scan := findStep(w.Steps, "scan")
	task, err := workflow.RenderString(scan.Task, vars(notable, false))
	if err != nil || !strings.Contains(task, "- Go releases") {
		t.Errorf("scan task = %q, %v", task, err)
	}
}
