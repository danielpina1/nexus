package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Step kinds.
const (
	KindAgent    = "agent"
	KindTool     = "tool"
	KindIf       = "if"
	KindForeach  = "foreach"
	KindParallel = "parallel"
	KindApprove  = "approve"
	KindAsk      = "ask"
	KindWait     = "wait"
	KindWorkflow = "workflow"
	KindGroup    = "group" // `do:` without foreach: a sequence of steps
)

// Trigger kinds wired to trigger sources. Other kinds (share, email,
// location, nfc, …) are parsed and kept; any trigger source whose name equals
// the kind fires them when Firing.Key equals the trigger value.
const (
	TriggerCron     = "cron"
	TriggerInterval = "interval"
	TriggerWebhook  = "webhook"
	TriggerEvent    = "event"
	TriggerIntent   = "intent"
)

var knownTriggerKinds = []string{"cron", "interval", "every", "webhook", "event", "node", "intent",
	"share", "email", "vault", "location", "nfc", "calendar", "price", "watch"}

// Workflow is a parsed workflows/*.md note.
type Workflow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Path        string    `json:"path,omitempty"`
	Enabled     bool      `json:"enabled"`
	DryRun      bool      `json:"dry_run,omitempty"` // runs default to dry run
	Triggers    []Trigger `json:"triggers,omitempty"`
	Inputs      []Input   `json:"inputs,omitempty"`
	Steps       []*Step   `json:"steps"`
	Body        string    `json:"-"`
	// Errors lists validation problems; a workflow with errors never runs.
	Errors []string `json:"errors,omitempty"`
}

// Valid reports whether the workflow parsed without errors.
func (w *Workflow) Valid() bool { return len(w.Errors) == 0 }

// Trigger is one entry of `triggers:`.
type Trigger struct {
	Kind string `json:"kind"`
	// Value is the kind's main value: cron expression, webhook id, event
	// topic, interval duration, or the raw value for other kinds.
	Value     string         `json:"value,omitempty"`
	TZ        string         `json:"tz,omitempty"`
	Every     Duration       `json:"every,omitempty"`
	SecretEnv string         `json:"secret_env,omitempty"`
	Where     string         `json:"where,omitempty"` // CEL filter over `trigger`/`event`
	Phrases   []string       `json:"phrases,omitempty"`
	Inputs    map[string]any `json:"inputs,omitempty"` // static/templated inputs for runs it starts
	DryRun    bool           `json:"dry_run,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
}

// Input declares a workflow input.
type Input struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"` // text (default) | number | bool | list | object
	Required    bool     `json:"required,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

// Step is one step. Exactly one kind applies; If on a non-"if" step is a
// guard (the step is skipped when it evaluates to false).
type Step struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind"`
	If   string `json:"if,omitempty"`

	Agent string `json:"agent,omitempty"`
	Task  string `json:"task,omitempty"`
	Model string `json:"model,omitempty"`

	Tool string         `json:"tool,omitempty"`
	With map[string]any `json:"with,omitempty"`

	Workflow string         `json:"workflow,omitempty"`
	Inputs   map[string]any `json:"inputs,omitempty"`

	Approve *ApproveSpec `json:"approve,omitempty"`
	Ask     *AskSpec     `json:"ask,omitempty"`
	Wait    *WaitSpec    `json:"wait,omitempty"`

	Foreach     string  `json:"foreach,omitempty"`
	As          string  `json:"as,omitempty"`
	Concurrency int     `json:"concurrency,omitempty"` // foreach `parallel: N`
	Do          []*Step `json:"do,omitempty"`          // foreach body, group, parallel branches
	Then        []*Step `json:"then,omitempty"`
	Else        []*Step `json:"else,omitempty"`

	OnError        string   `json:"on_error,omitempty"` // fail (default) | continue
	Retries        int      `json:"retries,omitempty"`
	Timeout        Duration `json:"timeout,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

// ApproveSpec is an approval card.
type ApproveSpec struct {
	Title       string         `json:"title"`
	Summary     string         `json:"summary,omitempty"`
	Preview     map[string]any `json:"preview,omitempty"`
	ActionClass string         `json:"action_class,omitempty"` // e.g. spend, write_external
	Level       string         `json:"level,omitempty"`        // approve | biometric
	Expires     Duration       `json:"expires,omitempty"`
}

// AskSpec is a question to the user.
type AskSpec struct {
	Question      string   `json:"question"`
	Choices       []string `json:"choices,omitempty"`
	AllowFreeText bool     `json:"allow_free_text,omitempty"`
	Default       string   `json:"default,omitempty"` // used in dry runs
}

// WaitSpec waits for a duration, an event topic or a condition.
type WaitSpec struct {
	Duration Duration `json:"duration,omitempty"`
	Event    string   `json:"event,omitempty"` // topic pattern, e.g. node.online, task.*
	Where    string   `json:"where,omitempty"` // CEL over `event` for Event
	Until    string   `json:"until,omitempty"` // CEL condition, re-evaluated while waiting
	Timeout  Duration `json:"timeout,omitempty"`
}

// Duration is a time.Duration that marshals as a string ("10m").
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 != nil {
			return err
		}
		*d = Duration(n)
		return nil
	}
	v, err := parseDuration(s)
	*d = Duration(v)
	return err
}

// parseDuration accepts Go durations plus "<n>d" days.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && fmt.Sprintf("%dd", n) == s {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

// ---------------------------------------------------------------- parsing

type rawWorkflow struct {
	Type        string    `yaml:"type"`
	ID          string    `yaml:"id"`
	Name        string    `yaml:"name"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Enabled     *bool     `yaml:"enabled"`
	DryRun      bool      `yaml:"dry_run"`
	Triggers    yaml.Node `yaml:"triggers"`
	Inputs      yaml.Node `yaml:"inputs"`
	Steps       yaml.Node `yaml:"steps"`
}

type rawStep struct {
	ID             string         `yaml:"id"`
	Name           string         `yaml:"name"`
	If             string         `yaml:"if"`
	Agent          string         `yaml:"agent"`
	Task           string         `yaml:"task"`
	Model          string         `yaml:"model"`
	Tool           string         `yaml:"tool"`
	With           map[string]any `yaml:"with"`
	Workflow       string         `yaml:"workflow"`
	Inputs         map[string]any `yaml:"inputs"`
	Approve        yaml.Node      `yaml:"approve"`
	Ask            yaml.Node      `yaml:"ask"`
	Choices        []string       `yaml:"choices"`
	AllowFreeText  *bool          `yaml:"allow_free_text"`
	Default        string         `yaml:"default"`
	Wait           yaml.Node      `yaml:"wait"`
	Foreach        string         `yaml:"foreach"`
	As             string         `yaml:"as"`
	Parallel       yaml.Node      `yaml:"parallel"`
	Do             []yaml.Node    `yaml:"do"`
	Then           []yaml.Node    `yaml:"then"`
	Else           []yaml.Node    `yaml:"else"`
	OnError        string         `yaml:"on_error"`
	Retries        int            `yaml:"retries"`
	Timeout        string         `yaml:"timeout"`
	IdempotencyKey string         `yaml:"idempotency_key"`
}

var reIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Parse parses a workflow note. It never fails: problems are listed in
// Workflow.Errors (and the workflow is then not runnable).
func Parse(p string, src []byte) *Workflow {
	w := &Workflow{Path: p, Enabled: true}
	fm, body, _, ok := note.SplitFrontmatter(src)
	w.Body = body
	if !ok {
		w.Errors = append(w.Errors, "missing frontmatter")
		w.ID = strings.TrimSuffix(path.Base(p), path.Ext(p))
		return w
	}
	var raw rawWorkflow
	if err := yaml.Unmarshal([]byte(fm), &raw); err != nil {
		w.Errors = append(w.Errors, "frontmatter: "+err.Error())
		w.ID = strings.TrimSuffix(path.Base(p), path.Ext(p))
		return w
	}
	w.ID = strings.TrimSpace(raw.ID)
	if w.ID == "" {
		w.ID = strings.TrimSuffix(path.Base(p), path.Ext(p))
	}
	if raw.Type != "" && raw.Type != "workflow" {
		w.Errors = append(w.Errors, fmt.Sprintf("type is %q, want workflow", raw.Type))
	}
	w.Name = firstNonEmpty(raw.Name, raw.Title, w.ID)
	w.Description = raw.Description
	if raw.Enabled != nil {
		w.Enabled = *raw.Enabled
	}
	w.DryRun = raw.DryRun

	p2 := &parser{w: w, ids: map[string]bool{}}
	p2.triggers(&raw.Triggers)
	p2.inputs(&raw.Inputs)
	if raw.Steps.Kind == 0 {
		p2.errf("no steps")
	} else if raw.Steps.Kind != yaml.SequenceNode {
		p2.errf("steps must be a list")
	} else {
		w.Steps = p2.steps(raw.Steps.Content, "")
		if len(w.Steps) == 0 && len(w.Errors) == 0 {
			p2.errf("no steps")
		}
	}
	if len(w.Errors) == 0 {
		validateExprs(w)
	}
	return w
}

type parser struct {
	w   *Workflow
	ids map[string]bool
}

func (p *parser) errf(format string, a ...any) {
	p.w.Errors = append(p.w.Errors, fmt.Sprintf(format, a...))
}

func (p *parser) triggers(n *yaml.Node) {
	if n.Kind == 0 {
		return
	}
	if n.Kind != yaml.SequenceNode {
		p.errf("triggers must be a list")
		return
	}
	for i, item := range n.Content {
		var m map[string]any
		if err := item.Decode(&m); err != nil || m == nil {
			p.errf("trigger %d: must be a mapping", i+1)
			continue
		}
		m = note.Normalize(m).(map[string]any)
		t := Trigger{Raw: m}
		for _, k := range knownTriggerKinds {
			if _, ok := m[k]; ok {
				t.Kind = k
				break
			}
		}
		if t.Kind == "" {
			// Unknown kind: first key that is not a modifier.
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !isTriggerModifier(k) {
					t.Kind = k
					break
				}
			}
		}
		if t.Kind == "" {
			p.errf("trigger %d: no kind", i+1)
			continue
		}
		v := m[t.Kind]
		t.TZ = str(m["tz"])
		t.SecretEnv = str(m["secret_env"])
		t.Where = str(m["where"])
		t.DryRun, _ = m["dry_run"].(bool)
		if in, ok := m["inputs"].(map[string]any); ok {
			t.Inputs = in
		}
		switch t.Kind {
		case "cron":
			t.Value = str(v)
			if t.Value == "" {
				p.errf("trigger %d: empty cron expression", i+1)
			}
		case "interval", "every":
			t.Kind = TriggerInterval
			t.Value = str(v)
			d, err := parseDuration(t.Value)
			if err != nil || d <= 0 {
				p.errf("trigger %d: invalid interval %q", i+1, t.Value)
			}
			t.Every = Duration(d)
		case "webhook":
			if mm, ok := v.(map[string]any); ok {
				t.Value = str(mm["id"])
				if s := str(mm["secret_env"]); s != "" {
					t.SecretEnv = s
				}
			} else {
				t.Value = str(v)
			}
			if t.Value == "" {
				t.Value = p.w.ID
			}
		case "event", "node":
			// `node: offline` is shorthand for `event: node.offline`.
			t.Value = str(v)
			if t.Kind == "node" && !strings.HasPrefix(t.Value, "node.") {
				t.Value = "node." + t.Value
			}
			t.Kind = TriggerEvent
			if t.Value == "" {
				p.errf("trigger %d: empty event topic", i+1)
			}
		case "intent":
			t.Phrases = note.StringList(v)
			if len(t.Phrases) == 0 {
				p.errf("trigger %d: intent needs at least one phrase", i+1)
			}
		default:
			if s, ok := v.(string); ok {
				t.Value = s
			}
		}
		p.w.Triggers = append(p.w.Triggers, t)
	}
}

func isTriggerModifier(k string) bool {
	switch k {
	case "tz", "secret_env", "where", "inputs", "dry_run", "id", "name":
		return true
	}
	return false
}

func (p *parser) inputs(n *yaml.Node) {
	if n.Kind == 0 {
		return
	}
	if n.Kind != yaml.MappingNode {
		p.errf("inputs must be a mapping")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value
		val := n.Content[i+1]
		in := Input{Name: name, Type: "text"}
		switch val.Kind {
		case yaml.ScalarNode:
			if val.Value != "" && val.Tag != "!!null" {
				in.Type = val.Value
			}
		case yaml.MappingNode:
			var m map[string]any
			if err := val.Decode(&m); err != nil {
				p.errf("input %s: %v", name, err)
				continue
			}
			m = note.Normalize(m).(map[string]any)
			if t := str(m["type"]); t != "" {
				in.Type = t
			}
			in.Required, _ = m["required"].(bool)
			in.Description = str(m["description"])
			in.Default = m["default"]
			in.Choices = note.StringList(m["choices"])
		default:
			p.errf("input %s: must be a type name or a mapping", name)
			continue
		}
		if !reIdent.MatchString(name) {
			p.errf("input %q: name must be an identifier", name)
		}
		p.w.Inputs = append(p.w.Inputs, in)
	}
}

func (p *parser) steps(nodes []*yaml.Node, where string) []*Step {
	var out []*Step
	for i, n := range nodes {
		if s := p.step(n, fmt.Sprintf("%sstep %d", where, i+1)); s != nil {
			out = append(out, s)
		}
	}
	return out
}

func ptrs(ns []yaml.Node) []*yaml.Node {
	out := make([]*yaml.Node, len(ns))
	for i := range ns {
		out[i] = &ns[i]
	}
	return out
}

func (p *parser) step(n *yaml.Node, where string) *Step {
	// Strict decoding catches typos (`agnet:`).
	b, err := yaml.Marshal(n)
	if err != nil {
		p.errf("%s: %v", where, err)
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var r rawStep
	if err := dec.Decode(&r); err != nil {
		p.errf("%s: %v", where, err)
		return nil
	}
	s := &Step{ID: strings.TrimSpace(r.ID), Name: r.Name, If: stripBraces(r.If)}
	if s.ID == "" {
		p.errf("%s: missing id", where)
		return nil
	}
	where = fmt.Sprintf("step %q", s.ID)
	if !reStepID.MatchString(s.ID) {
		p.errf("%s: id must contain only letters, digits, '_' or '-'", where)
	}
	if p.ids[s.ID] {
		p.errf("%s: duplicate id", where)
	}
	p.ids[s.ID] = true

	var kinds []string
	add := func(k string, present bool) {
		if present {
			kinds = append(kinds, k)
		}
	}
	add(KindAgent, r.Agent != "")
	add(KindTool, r.Tool != "")
	add(KindWorkflow, r.Workflow != "")
	add(KindApprove, r.Approve.Kind != 0)
	add(KindAsk, r.Ask.Kind != 0)
	add(KindWait, r.Wait.Kind != 0)
	add(KindForeach, r.Foreach != "")
	add(KindParallel, r.Foreach == "" && r.Parallel.Kind == yaml.SequenceNode)
	add(KindIf, len(r.Then) > 0 || len(r.Else) > 0)
	add(KindGroup, r.Foreach == "" && len(r.Do) > 0)
	switch len(kinds) {
	case 0:
		p.errf("%s: no step kind (agent, tool, if/then, foreach, parallel, approve, ask, wait, workflow or do)", where)
		return nil
	case 1:
		s.Kind = kinds[0]
	default:
		p.errf("%s: more than one step kind: %s", where, strings.Join(kinds, ", "))
		return nil
	}
	if s.Kind == KindIf && s.If == "" {
		p.errf("%s: then/else without if", where)
	}

	s.OnError = strings.ToLower(strings.TrimSpace(r.OnError))
	switch s.OnError {
	case "", "fail", "continue":
	default:
		p.errf("%s: on_error must be fail or continue", where)
	}
	if r.Retries < 0 || r.Retries > 10 {
		p.errf("%s: retries must be between 0 and 10", where)
	}
	s.Retries = r.Retries
	if r.Timeout != "" {
		d, err := parseDuration(r.Timeout)
		if err != nil || d <= 0 {
			p.errf("%s: invalid timeout %q", where, r.Timeout)
		}
		s.Timeout = Duration(d)
	}
	s.IdempotencyKey = r.IdempotencyKey

	switch s.Kind {
	case KindAgent:
		s.Agent, s.Task, s.Model = r.Agent, r.Task, r.Model
		if strings.TrimSpace(s.Task) == "" {
			p.errf("%s: agent step needs a task", where)
		}
	case KindTool:
		s.Tool = r.Tool
		s.With = normMap(r.With)
	case KindWorkflow:
		s.Workflow = r.Workflow
		s.Inputs = normMap(r.Inputs)
	case KindApprove:
		s.Approve = p.approve(&r.Approve, where)
	case KindAsk:
		s.Ask = p.ask(&r, where)
	case KindWait:
		s.Wait = p.wait(&r.Wait, where)
	case KindForeach:
		s.Foreach = stripBraces(r.Foreach)
		s.As = firstNonEmpty(strings.TrimSpace(r.As), "item")
		if !reIdent.MatchString(s.As) {
			p.errf("%s: as must be an identifier", where)
		}
		s.Concurrency = 1
		if r.Parallel.Kind == yaml.ScalarNode {
			var n int
			if err := r.Parallel.Decode(&n); err != nil || n < 1 || n > 32 {
				p.errf("%s: parallel must be a number between 1 and 32", where)
			} else {
				s.Concurrency = n
			}
		}
		if len(r.Do) == 0 {
			p.errf("%s: foreach needs do: steps", where)
		}
		s.Do = p.steps(ptrs(r.Do), where+" ")
	case KindParallel:
		s.Do = p.steps(r.Parallel.Content, where+" ")
	case KindIf:
		s.Then = p.steps(ptrs(r.Then), where+" then ")
		s.Else = p.steps(ptrs(r.Else), where+" else ")
	case KindGroup:
		s.Do = p.steps(ptrs(r.Do), where+" ")
	}
	if s.Kind != KindForeach && r.Parallel.Kind == yaml.ScalarNode {
		p.errf("%s: parallel: N only applies to foreach", where)
	}
	if s.Kind != KindForeach && s.Kind != KindGroup && len(r.Do) > 0 {
		p.errf("%s: do: only applies to foreach or a group", where)
	}
	if s.Kind != KindAsk && (len(r.Choices) > 0 || r.AllowFreeText != nil) {
		p.errf("%s: choices/allow_free_text only apply to ask", where)
	}
	return s
}

var reStepID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (p *parser) approve(n *yaml.Node, where string) *ApproveSpec {
	a := &ApproveSpec{}
	switch n.Kind {
	case yaml.ScalarNode:
		a.Title = n.Value
	case yaml.MappingNode:
		var m map[string]any
		if err := n.Decode(&m); err != nil {
			p.errf("%s: approve: %v", where, err)
			return a
		}
		m = note.Normalize(m).(map[string]any)
		a.Title = str(m["title"])
		a.Summary = str(m["summary"])
		if pv, ok := m["preview"].(map[string]any); ok {
			a.Preview = pv
		} else if m["preview"] != nil {
			a.Preview = map[string]any{"preview": m["preview"]}
		}
		a.ActionClass = str(m["action_class"])
		a.Level = str(m["level"])
		if e := str(m["expires"]); e != "" {
			d, err := parseDuration(e)
			if err != nil {
				p.errf("%s: approve.expires: %v", where, err)
			}
			a.Expires = Duration(d)
		}
		if a.ActionClass != "" {
			if _, ok := actionClass(a.ActionClass); !ok {
				p.errf("%s: unknown action_class %q", where, a.ActionClass)
			}
		}
		switch strings.ToLower(a.Level) {
		case "", "approve", "biometric":
		default:
			p.errf("%s: approve.level must be approve or biometric", where)
		}
	default:
		p.errf("%s: approve must be a title or a mapping", where)
	}
	if strings.TrimSpace(a.Title) == "" {
		p.errf("%s: approve needs a title", where)
	}
	return a
}

func (p *parser) ask(r *rawStep, where string) *AskSpec {
	a := &AskSpec{Choices: r.Choices, Default: r.Default}
	switch r.Ask.Kind {
	case yaml.ScalarNode:
		a.Question = r.Ask.Value
	case yaml.MappingNode:
		var m map[string]any
		if err := r.Ask.Decode(&m); err != nil {
			p.errf("%s: ask: %v", where, err)
			return a
		}
		m = note.Normalize(m).(map[string]any)
		a.Question = str(m["question"])
		if c := note.StringList(m["choices"]); len(c) > 0 {
			a.Choices = c
		}
		if d := str(m["default"]); d != "" {
			a.Default = d
		}
		if b, ok := m["allow_free_text"].(bool); ok {
			r.AllowFreeText = &b
		}
	default:
		p.errf("%s: ask must be a question or a mapping", where)
	}
	a.AllowFreeText = len(a.Choices) == 0
	if r.AllowFreeText != nil {
		a.AllowFreeText = *r.AllowFreeText
	}
	if strings.TrimSpace(a.Question) == "" {
		p.errf("%s: ask needs a question", where)
	}
	return a
}

func (p *parser) wait(n *yaml.Node, where string) *WaitSpec {
	w := &WaitSpec{}
	dur := func(field, s string) Duration {
		d, err := parseDuration(s)
		if err != nil || d < 0 {
			p.errf("%s: wait.%s: invalid duration %q", where, field, s)
		}
		return Duration(d)
	}
	switch n.Kind {
	case yaml.ScalarNode:
		w.Duration = dur("duration", n.Value)
	case yaml.MappingNode:
		var m map[string]any
		if err := n.Decode(&m); err != nil {
			p.errf("%s: wait: %v", where, err)
			return w
		}
		m = note.Normalize(m).(map[string]any)
		for k := range m {
			switch k {
			case "duration", "event", "where", "until", "timeout":
			default:
				p.errf("%s: wait: unknown field %q", where, k)
			}
		}
		if s := str(m["duration"]); s != "" {
			w.Duration = dur("duration", s)
		}
		w.Event = str(m["event"])
		w.Where = stripBraces(str(m["where"]))
		w.Until = stripBraces(str(m["until"]))
		if s := str(m["timeout"]); s != "" {
			w.Timeout = dur("timeout", s)
		}
	default:
		p.errf("%s: wait must be a duration or a mapping", where)
	}
	set := 0
	for _, b := range []bool{w.Duration > 0, w.Event != "", w.Until != ""} {
		if b {
			set++
		}
	}
	if set != 1 {
		p.errf("%s: wait needs exactly one of duration, event or until", where)
	}
	return w
}

// ---------------------------------------------------------------- helpers

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func normMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return note.Normalize(m).(map[string]any)
}

// stripBraces turns "{{ expr }}" into "expr" (conditions may be written
// either way).
func stripBraces(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{{") && strings.HasSuffix(t, "}}") && strings.Count(t, "{{") == 1 {
		return strings.TrimSpace(t[2 : len(t)-2])
	}
	return t
}

// walk calls fn for every step (depth first, including nested ones) with the
// iteration variables in scope.
func walk(steps []*Step, scope []string, fn func(s *Step, scope []string)) {
	for _, s := range steps {
		fn(s, scope)
		inner := scope
		if s.Kind == KindForeach {
			inner = append(append([]string{}, scope...), s.As)
		}
		walk(s.Do, inner, fn)
		walk(s.Then, scope, fn)
		walk(s.Else, scope, fn)
	}
}

// StepIDs returns every step id of the workflow (nested ones included).
func (w *Workflow) StepIDs() []string {
	var out []string
	walk(w.Steps, nil, func(s *Step, _ []string) { out = append(out, s.ID) })
	return out
}

// validateExprs compiles every CEL expression and template.
func validateExprs(w *Workflow) {
	add := func(where string, err error) {
		if err != nil {
			w.Errors = append(w.Errors, fmt.Sprintf("%s: %v", where, err))
		}
	}
	walk(w.Steps, nil, func(s *Step, scope []string) {
		where := fmt.Sprintf("step %q", s.ID)
		if s.If != "" {
			add(where+" if", checkExpr(s.If, scope))
		}
		if s.Foreach != "" {
			add(where+" foreach", checkExpr(s.Foreach, scope))
		}
		if s.Kind == KindForeach {
			scope = append(append([]string{}, scope...), s.As)
		}
		add(where+" task", checkTemplate(s.Task, scope))
		add(where+" with", checkTemplateValue(s.With, scope))
		add(where+" inputs", checkTemplateValue(s.Inputs, scope))
		add(where+" idempotency_key", checkTemplate(s.IdempotencyKey, scope))
		if a := s.Approve; a != nil {
			add(where+" approve.title", checkTemplate(a.Title, scope))
			add(where+" approve.summary", checkTemplate(a.Summary, scope))
			add(where+" approve.preview", checkTemplateValue(a.Preview, scope))
		}
		if a := s.Ask; a != nil {
			add(where+" ask", checkTemplate(a.Question, scope))
		}
		if wt := s.Wait; wt != nil {
			if wt.Until != "" {
				add(where+" wait.until", checkExpr(wt.Until, scope))
			}
			if wt.Where != "" {
				add(where+" wait.where", checkExpr(wt.Where, append(append([]string{}, scope...), "event")))
			}
		}
	})
	for i, t := range w.Triggers {
		if t.Where != "" {
			add(fmt.Sprintf("trigger %d where", i+1), checkExpr(t.Where, []string{"event"}))
		}
		add(fmt.Sprintf("trigger %d inputs", i+1), checkTemplateValue(t.Inputs, []string{"event"}))
	}
}
