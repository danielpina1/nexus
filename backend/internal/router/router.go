// Package router classifies free-form input into a command kind and an agent
// (kernel.Router).
//
// Module "router" (hub, default enabled). Requires store (decision log).
// Provides kernel.SvcRouter (*Router). Optional services, resolved per call:
// "agents" (catalog for keyword matching and the LLM prompt) and "llm".
//
// Stages:
//  1. Rules: configured regex rules, URL domains, share types and capture
//     phrases ("remember…", "note…") return immediately with high confidence.
//     Keyword matching of the input against agent ids, names and descriptions
//     and a "question about my stuff" heuristic produce weaker candidates.
//  2. LLM classifier (when an LLM is available and no rule matched): the
//     catalog and stage-1 candidates are given to the model alias `model`
//     (default "fast-local"); if its confidence is below `threshold` the
//     `fallback_model` (default "smart") is asked. LLM failures fall back to
//     the stage-1 result.
//
// Every decision is logged to router_decisions; corrections to
// router_corrections (training data for a future fine-tuned router).
//
// Config (nexus.yaml):
//
//	router:
//	  threshold: 0.6
//	  model: fast-local
//	  fallback_model: smart
//	  rules:
//	    - {pattern: "(?i)\\b(groceries|compras)\\b", agent: grocery-continente}
//	    - {pattern: "(?i)^journal:", kind: capture}
//	  domains: {github.com: code-fixer}
//	  share_types: {image: receipt-scanner}
package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() { kernel.RegisterModule(func() kernel.Module { return &Module{} }) }

// Rule maps a regular expression to an agent and/or a kind.
type Rule struct {
	Pattern string `yaml:"pattern"`
	Agent   string `yaml:"agent"`
	Kind    string `yaml:"kind"` // search | capture | task | workflow (default task)
}

// Config is the "router" config section.
type Config struct {
	Threshold     float64           `yaml:"threshold"`
	Model         string            `yaml:"model"`
	FallbackModel string            `yaml:"fallback_model"`
	Rules         []Rule            `yaml:"rules"`
	Domains       map[string]string `yaml:"domains"`
	ShareTypes    map[string]string `yaml:"share_types"`
	LLMTimeout    time.Duration     `yaml:"llm_timeout"`
}

func (c *Config) defaults() {
	if c.Threshold <= 0 {
		c.Threshold = 0.6
	}
	if c.Model == "" {
		c.Model = "fast-local"
	}
	if c.FallbackModel == "" {
		c.FallbackModel = "smart"
	}
	if c.LLMTimeout <= 0 {
		c.LLMTimeout = 30 * time.Second
	}
}

var migrations = []string{
	`CREATE TABLE router_decisions (
		id              TEXT PRIMARY KEY,
		created_at      INTEGER NOT NULL,
		input           TEXT NOT NULL,
		context         TEXT NOT NULL DEFAULT '{}',
		stage           TEXT NOT NULL,
		kind            INTEGER NOT NULL,
		agent_id        TEXT NOT NULL DEFAULT '',
		confidence      REAL NOT NULL,
		reason          TEXT NOT NULL DEFAULT '',
		alternatives    TEXT NOT NULL DEFAULT '[]',
		model           TEXT NOT NULL DEFAULT '',
		latency_ms      INTEGER NOT NULL DEFAULT 0,
		task_id         TEXT NOT NULL DEFAULT '',
		corrected_agent TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX router_decisions_task ON router_decisions(task_id)`,
	`CREATE TABLE router_corrections (
		id          TEXT PRIMARY KEY,
		created_at  INTEGER NOT NULL,
		decision_id TEXT NOT NULL DEFAULT '',
		task_id     TEXT NOT NULL DEFAULT '',
		input       TEXT NOT NULL DEFAULT '',
		from_agent  TEXT NOT NULL DEFAULT '',
		to_agent    TEXT NOT NULL,
		comment     TEXT NOT NULL DEFAULT ''
	)`,
}

// Kind shorthands.
const (
	kindSearch   = nexusv1.CommandKind_COMMAND_KIND_SEARCH
	kindCapture  = nexusv1.CommandKind_COMMAND_KIND_CAPTURE
	kindTask     = nexusv1.CommandKind_COMMAND_KIND_TASK
	kindWorkflow = nexusv1.CommandKind_COMMAND_KIND_WORKFLOW
)

// Router implements kernel.Router.
type Router struct {
	h     kernel.Host
	log   *slog.Logger
	db    *sql.DB
	cfg   Config
	rules []compiledRule
	now   func() time.Time
}

type compiledRule struct {
	re    *regexp.Regexp
	agent string
	kind  nexusv1.CommandKind
}

var _ kernel.Router = (*Router)(nil)

// New migrates the store and compiles the configured rules.
func New(h kernel.Host, st kernel.Store, cfg Config) (*Router, error) {
	cfg.defaults()
	if err := st.Migrate("router", migrations); err != nil {
		return nil, fmt.Errorf("router: migrate: %w", err)
	}
	r := &Router{h: h, log: h.Logger(), db: st.DB(), cfg: cfg, now: time.Now}
	for i, rule := range cfg.Rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("router: rule %d: %w", i, err)
		}
		kind := kindTask
		if rule.Kind != "" {
			k, ok := ParseKind(rule.Kind)
			if !ok {
				return nil, fmt.Errorf("router: rule %d: unknown kind %q", i, rule.Kind)
			}
			kind = k
		}
		if kind == kindTask && rule.Agent == "" {
			return nil, fmt.Errorf("router: rule %d: task rules need an agent", i)
		}
		r.rules = append(r.rules, compiledRule{re: re, agent: rule.Agent, kind: kind})
	}
	return r, nil
}

// ParseKind accepts "search", "SEARCH" or "COMMAND_KIND_SEARCH".
func ParseKind(s string) (nexusv1.CommandKind, bool) {
	n := strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(n, "COMMAND_KIND_") {
		n = "COMMAND_KIND_" + n
	}
	v, ok := nexusv1.CommandKind_value[n]
	if !ok || v == 0 {
		return 0, false
	}
	return nexusv1.CommandKind(v), true
}

func kindName(k nexusv1.CommandKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "COMMAND_KIND_"))
}

// decision is a route plus logging metadata.
type decision struct {
	route *nexusv1.Route
	stage string
	model string
}

// Route classifies input. The returned route's params carry "decision_id"
// (the router_decisions row) so callers can link the task and corrections.
func (r *Router) Route(ctx context.Context, input string, attachments []*nexusv1.Attachment, c *nexusv1.CommandContext) (*nexusv1.Route, error) {
	start := r.now()
	agents := r.listAgents(ctx)

	d, strong := r.rulesStage(input, attachments, c, agents)
	if !strong {
		if llm, ok := lookupLLM(r.h); ok {
			if ld, err := r.llmStage(ctx, llm, input, attachments, c, agents, d.route); err != nil {
				r.log.Warn("router LLM stage failed; using rules result", "err", err)
			} else {
				d = ld
			}
		}
	}
	route := d.route
	if route.Kind == nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
		route.Kind = kindTask
	}
	if route.Kind == kindTask && route.AgentId != "" && route.Tier == nexusv1.Tier_TIER_UNSPECIFIED {
		if a := findAgent(agents, route.AgentId); a != nil {
			route.Tier = a.Tier
		}
	}
	route.Alternatives = dedupeExcept(route.Alternatives, route.AgentId)

	id, err := r.logDecision(ctx, input, c, d, r.now().Sub(start))
	if err != nil {
		r.log.Error("log router decision", "err", err)
	} else {
		if route.Params == nil {
			route.Params = &structpb.Struct{Fields: map[string]*structpb.Value{}}
		}
		route.Params.Fields["decision_id"] = structpb.NewStringValue(id)
	}
	return route, nil
}

func lookupLLM(h kernel.Host) (kernel.LLM, bool) {
	l, err := kernel.Resolve[kernel.LLM](h, kernel.SvcLLM)
	return l, err == nil
}

func (r *Router) listAgents(ctx context.Context) []*nexusv1.Agent {
	cat, err := kernel.Resolve[kernel.AgentCatalog](r.h, kernel.SvcAgents)
	if err != nil {
		return nil
	}
	list, err := cat.ListAgents(ctx, false)
	if err != nil {
		r.log.Warn("router: list agents", "err", err)
		return nil
	}
	var out []*nexusv1.Agent
	for _, a := range list {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}

func findAgent(agents []*nexusv1.Agent, id string) *nexusv1.Agent {
	for _, a := range agents {
		if a.Id == id {
			return a
		}
	}
	return nil
}

// ---------------------------------------------------------------- stage 1

var (
	captureRe = regexp.MustCompile(`(?i)^\s*(remember( that)?|note( that)?|note:|save this|jot down|lembra(-te)?( que)?|anota)\b`)
	searchRe  = regexp.MustCompile(`(?i)^\s*(what|when|where|who|which|how|did|do|does|is|are|have|has|find|show me)\b.*\b(my|i|me|mine|our)\b`)
	urlRe     = regexp.MustCompile(`https?://[^\s<>"]+`)
	wordRe    = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"use": true, "you": true, "your": true, "please": true, "can": true, "into": true, "about": true,
	"are": true, "was": true, "will": true, "all": true, "any": true, "its": true, "our": true,
	"agent": true, "when": true, "what": true, "make": true, "get": true, "has": true, "have": true,
}

// rulesStage returns the stage-1 decision and whether it is strong enough to
// skip the LLM.
func (r *Router) rulesStage(input string, atts []*nexusv1.Attachment, c *nexusv1.CommandContext, agents []*nexusv1.Agent) (decision, bool) {
	known := func(id string) bool { return agents == nil || findAgent(agents, id) != nil }

	for _, rule := range r.rules {
		if rule.re.MatchString(input) && (rule.agent == "" || known(rule.agent)) {
			return decision{stage: "rule", route: &nexusv1.Route{Kind: rule.kind, AgentId: rule.agent, Confidence: 0.95,
				Reason: "matched rule " + rule.re.String()}}, true
		}
	}
	for _, host := range urlHosts(input, atts) {
		for domain, agent := range r.cfg.Domains {
			if (host == domain || strings.HasSuffix(host, "."+domain)) && known(agent) {
				return decision{stage: "domain", route: &nexusv1.Route{Kind: kindTask, AgentId: agent, Confidence: 0.9,
					Reason: "link to " + domain}}, true
			}
		}
	}
	if st := c.GetShareType(); st != "" {
		if agent, ok := r.cfg.ShareTypes[st]; ok && known(agent) {
			return decision{stage: "share_type", route: &nexusv1.Route{Kind: kindTask, AgentId: agent, Confidence: 0.85,
				Reason: "shared " + st}}, true
		}
	}
	if captureRe.MatchString(input) {
		return decision{stage: "capture_phrase", route: &nexusv1.Route{Kind: kindCapture, Confidence: 0.9,
			Reason: "starts with a capture phrase"}}, true
	}

	scored := keywordScores(input, agents)
	if strings.HasSuffix(strings.TrimSpace(input), "?") && searchRe.MatchString(input) && (len(scored) == 0 || scored[0].score < 0.6) {
		return decision{stage: "search_heuristic", route: &nexusv1.Route{Kind: kindSearch, Confidence: 0.65,
			Reason: "question about your own data", Alternatives: ids(scored)}}, false
	}
	if len(scored) > 0 {
		best := scored[0]
		return decision{stage: "keyword", route: &nexusv1.Route{Kind: kindTask, AgentId: best.agent.Id,
			Confidence: float32(best.score), Reason: "keywords: " + strings.Join(best.words, ", "),
			Alternatives: ids(scored[1:])}}, false
	}
	return decision{stage: "default", route: &nexusv1.Route{Kind: kindTask, Reason: "no rule or keyword matched"}}, false
}

func urlHosts(input string, atts []*nexusv1.Attachment) []string {
	var raw []string
	raw = append(raw, urlRe.FindAllString(input, -1)...)
	for _, a := range atts {
		if a.GetUrl() != "" {
			raw = append(raw, a.Url)
		}
	}
	var hosts []string
	for _, s := range raw {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			hosts = append(hosts, strings.ToLower(u.Hostname()))
		}
	}
	return hosts
}

type scoredAgent struct {
	agent *nexusv1.Agent
	score float64
	words []string
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if len([]rune(w)) >= 3 && !stopwords[w] {
			out[w] = true
		}
	}
	return out
}

// keywordScores scores agents by input words found in their id/name (0.3
// each) and description (0.15 each), capped at 0.85, best first.
func keywordScores(input string, agents []*nexusv1.Agent) []scoredAgent {
	in := tokens(input)
	var out []scoredAgent
	for _, a := range agents {
		name := tokens(a.Id + " " + a.Name)
		desc := tokens(a.Description)
		s := scoredAgent{agent: a}
		for w := range in {
			switch {
			case name[w]:
				s.score += 0.3
				s.words = append(s.words, w)
			case desc[w]:
				s.score += 0.15
				s.words = append(s.words, w)
			}
		}
		if s.score > 0 {
			if s.score > 0.85 {
				s.score = 0.85
			}
			sort.Strings(s.words)
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].agent.Id < out[j].agent.Id
	})
	return out
}

func ids(s []scoredAgent) []string {
	out := make([]string, 0, len(s))
	for _, a := range s {
		out = append(out, a.agent.Id)
	}
	return out
}

func dedupeExcept(in []string, except string) []string {
	seen := map[string]bool{except: true, "": true}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- stage 2

const systemPrompt = `You are the command router of Nexus, a personal agent OS.
Classify the user's input into exactly one kind:
- "search": a question about the user's own notes, data, history or knowledge ("when did I…", "what is my…").
- "capture": the user wants something remembered or noted ("remember…", "note…", a fact, an idea).
- "task": the user wants something done by an agent.
- "workflow": the input explicitly starts a known workflow (rare).
For "task", choose the best agent id from the catalog (or "" if none fits) and up to 3 alternatives.
Respond with a single JSON object only:
{"kind":"search|capture|task|workflow","agent":"<agent id or empty>","confidence":0.0-1.0,"reason":"<short>","alternatives":["<agent id>"],"params":{}}`

type llmAnswer struct {
	Kind         string         `json:"kind"`
	Agent        string         `json:"agent"`
	Confidence   float64        `json:"confidence"`
	Reason       string         `json:"reason"`
	Alternatives []string       `json:"alternatives"`
	Params       map[string]any `json:"params"`
}

func (r *Router) llmStage(ctx context.Context, llm kernel.LLM, input string, atts []*nexusv1.Attachment, c *nexusv1.CommandContext,
	agents []*nexusv1.Agent, hint *nexusv1.Route) (decision, error) {
	msgs := []kernel.ChatMessage{
		{Role: "system", Content: systemPrompt + "\n\nAgent catalog:\n" + catalogText(agents)},
		{Role: "user", Content: userPrompt(input, atts, c, hint)},
	}
	d, err := r.ask(ctx, llm, r.cfg.Model, msgs, agents)
	if err == nil && d.route.Confidence >= float32(r.cfg.Threshold) {
		return d, nil
	}
	if r.cfg.FallbackModel == "" || r.cfg.FallbackModel == r.cfg.Model {
		return d, err
	}
	fd, ferr := r.ask(ctx, llm, r.cfg.FallbackModel, msgs, agents)
	if ferr != nil {
		if err != nil {
			return d, ferr
		}
		r.log.Warn("router fallback model failed", "model", r.cfg.FallbackModel, "err", ferr)
		return d, nil
	}
	return fd, nil
}

func (r *Router) ask(ctx context.Context, llm kernel.LLM, model string, msgs []kernel.ChatMessage, agents []*nexusv1.Agent) (decision, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.LLMTimeout)
	defer cancel()
	zero := 0.0
	resp, err := llm.Chat(ctx, kernel.ChatRequest{Model: model, Messages: msgs, JSON: true, Temperature: &zero, MaxTokens: 300})
	if err != nil {
		return decision{}, fmt.Errorf("%s: %w", model, err)
	}
	var ans llmAnswer
	if err := json.Unmarshal([]byte(extractJSON(resp.Message.Content)), &ans); err != nil {
		return decision{}, fmt.Errorf("%s: bad JSON answer: %w", model, err)
	}
	kind, ok := ParseKind(ans.Kind)
	if !ok {
		kind = kindTask
	}
	conf := clamp(ans.Confidence)
	agent := ans.Agent
	reason := ans.Reason
	if agent != "" && findAgent(agents, agent) == nil {
		reason = fmt.Sprintf("model chose unknown agent %q; %s", agent, reason)
		agent = ""
		if conf > 0.3 {
			conf = 0.3
		}
	}
	var alts []string
	for _, a := range ans.Alternatives {
		if findAgent(agents, a) != nil {
			alts = append(alts, a)
		}
	}
	route := &nexusv1.Route{Kind: kind, AgentId: agent, Confidence: float32(conf), Reason: reason, Alternatives: alts}
	if kind != kindTask {
		route.AgentId = ""
	}
	if len(ans.Params) > 0 {
		if p, err := structpb.NewStruct(ans.Params); err == nil {
			route.Params = p
		}
	}
	return decision{route: route, stage: "llm", model: model}, nil
}

// extractJSON tolerates models wrapping the object in prose or code fences.
func extractJSON(s string) string {
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func clamp(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

func catalogText(agents []*nexusv1.Agent) string {
	if len(agents) == 0 {
		return "(no agents available)"
	}
	var b strings.Builder
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s: %s — %s (tier %s, runtime %s)\n", a.Id, a.Name, oneLine(a.Description),
			strings.ToLower(strings.TrimPrefix(a.Tier.String(), "TIER_")), a.Runtime)
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func userPrompt(input string, atts []*nexusv1.Attachment, c *nexusv1.CommandContext, hint *nexusv1.Route) string {
	var b strings.Builder
	b.WriteString("Input: ")
	b.WriteString(input)
	if c != nil {
		fmt.Fprintf(&b, "\nSource: %s", c.Source)
		if c.ShareType != "" {
			fmt.Fprintf(&b, "\nShared content type: %s", c.ShareType)
		}
		if c.Focus != "" {
			fmt.Fprintf(&b, "\nFocus mode: %s", c.Focus)
		}
	}
	for _, a := range atts {
		fmt.Fprintf(&b, "\nAttachment: %s (%s) %s", a.Name, a.MimeType, a.Url)
	}
	if hint != nil && (hint.AgentId != "" || hint.Kind != kindTask) {
		fmt.Fprintf(&b, "\nRule-stage guess: kind=%s agent=%s confidence=%.2f", kindName(hint.Kind), hint.AgentId, hint.Confidence)
	}
	return b.String()
}

// ---------------------------------------------------------------- logging

func (r *Router) logDecision(ctx context.Context, input string, c *nexusv1.CommandContext, d decision, latency time.Duration) (string, error) {
	id := "rtd_" + ulid.Make().String()
	cb, _ := json.Marshal(map[string]any{
		"source": c.GetSource(), "focus": c.GetFocus(), "share_type": c.GetShareType(),
	})
	alts, _ := json.Marshal(d.route.Alternatives)
	_, err := r.db.ExecContext(ctx, `INSERT INTO router_decisions
		(id, created_at, input, context, stage, kind, agent_id, confidence, reason, alternatives, model, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.now().UnixMilli(), input, string(cb), d.stage, int32(d.route.Kind), d.route.AgentId,
		float64(d.route.Confidence), d.route.Reason, string(alts), d.model, latency.Milliseconds())
	return id, err
}

// LinkTask records which task a decision produced.
func (r *Router) LinkTask(ctx context.Context, decisionID, taskID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE router_decisions SET task_id = ? WHERE id = ?`, taskID, decisionID)
	return err
}

// Correction is a user override of a routing decision.
type Correction struct {
	DecisionID string // optional; looked up from TaskID when empty
	TaskID     string
	Input      string // optional; taken from the decision when empty
	FromAgent  string // optional; taken from the decision when empty
	ToAgent    string
	Comment    string
}

// RecordCorrection stores a correction (training data) and marks the decision.
func (r *Router) RecordCorrection(ctx context.Context, c Correction) error {
	if c.ToAgent == "" {
		return fmt.Errorf("router: correction needs the chosen agent")
	}
	if c.DecisionID == "" && c.TaskID != "" {
		_ = r.db.QueryRowContext(ctx, `SELECT id FROM router_decisions WHERE task_id = ? ORDER BY id DESC LIMIT 1`, c.TaskID).Scan(&c.DecisionID)
	}
	if c.DecisionID != "" {
		var input, agent string
		if err := r.db.QueryRowContext(ctx, `SELECT input, agent_id FROM router_decisions WHERE id = ?`, c.DecisionID).Scan(&input, &agent); err == nil {
			if c.Input == "" {
				c.Input = input
			}
			if c.FromAgent == "" {
				c.FromAgent = agent
			}
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE router_decisions SET corrected_agent = ? WHERE id = ?`, c.ToAgent, c.DecisionID); err != nil {
			return err
		}
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO router_corrections (id, created_at, decision_id, task_id, input, from_agent, to_agent, comment)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"rtc_"+ulid.Make().String(), r.now().UnixMilli(), c.DecisionID, c.TaskID, c.Input, c.FromAgent, c.ToAgent, c.Comment)
	return err
}

// ---------------------------------------------------------------- module

// Module is the router module.
type Module struct {
	kernel.BaseModule
	Router *Router
}

func (*Module) Info() kernel.ModuleInfo {
	return kernel.ModuleInfo{
		Name:           "router",
		Description:    "Classifies commands (search / capture / task) and picks agents",
		Side:           kernel.SideHub,
		Requires:       []string{"store"},
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("router: config: %w", err)
	}
	st, err := kernel.Resolve[kernel.Store](h, kernel.SvcStore)
	if err != nil {
		return err
	}
	r, err := New(h, st, cfg)
	if err != nil {
		return err
	}
	m.Router = r
	h.Provide(kernel.SvcRouter, r)
	return nil
}
