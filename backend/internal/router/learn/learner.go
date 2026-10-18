// Package learn is the learning stage of the Nexus router (docs/ROUTING.md).
//
// Module "router.learn" (hub, default enabled). Requires store and router
// (whose router_decisions / router_corrections it learns from). Provides
// SvcLearn (*Learner), which the router calls for every non-trivial input.
// Optional services, resolved per call: agents, teams, workflows (the
// candidates), llm (embeddings), vault (policies/models.md), events (the
// outcome feedback loop).
//
// Stages, combined into one calibrated confidence:
//
//	A  retrieval   embeddings of candidate descriptions + `examples:` and of
//	               past successful requests (kNN, cosine)
//	B  classifier  online multinomial logistic regression over the request
//	               embedding + rule features, trained from decisions labelled
//	               by corrections and outcomes
//	   rules       keyword overlap (same signal as the router's keyword stage)
//	   LLM         the router's LLM classifier, only called when A+B are
//	               unsure or disagree
//	C  bandit      per request cluster, Thompson sampling picks the runtime
//	               (claude-code / codex / ollama …) of the chosen agent:
//	               route.params["runtime_hint"]
//
// Config (nexus.yaml, top-level key = module name):
//
//	router.learn:
//	  enabled: true
//	  embed_model: embed      # LLM alias; "hash" = built-in hashing embedder
//	  accept: 0.75            # calibrated confidence that skips the LLM
//	  no_fit: 0.3             # below: offer "Create a new agent for this"
//	  min_examples: 20        # classifier weight ramps up to this many labels
//	  k: 7                    # neighbours for kNN
//	  cost_scale_eur: 0.5     # bandit cost penalty scale
//	  latency_scale: 10m      # bandit latency penalty scale
//	  rebuild_every: 6h
package learn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// SvcLearn is the service name of *Learner.
const SvcLearn = "router.learn"

// Config is the "router.learn" config section.
type Config struct {
	Enabled      *bool         `yaml:"enabled"`
	EmbedModel   string        `yaml:"embed_model"`
	Accept       float64       `yaml:"accept"`
	NoFit        float64       `yaml:"no_fit"`
	MinExamples  int           `yaml:"min_examples"`
	K            int           `yaml:"k"`
	CostScaleEUR float64       `yaml:"cost_scale_eur"`
	LatencyScale time.Duration `yaml:"latency_scale"`
	RebuildEvery time.Duration `yaml:"rebuild_every"`
	SyncEvery    time.Duration `yaml:"sync_every"`
	EmbedTimeout time.Duration `yaml:"embed_timeout"`
	Seed         uint64        `yaml:"seed"`
}

func (c *Config) defaults() {
	if c.EmbedModel == "" {
		c.EmbedModel = "embed"
	}
	if c.Accept <= 0 {
		c.Accept = 0.75
	}
	if c.NoFit <= 0 {
		c.NoFit = 0.3
	}
	if c.MinExamples <= 0 {
		c.MinExamples = 20
	}
	if c.K <= 0 {
		c.K = 7
	}
	if c.CostScaleEUR <= 0 {
		c.CostScaleEUR = 0.5
	}
	if c.LatencyScale <= 0 {
		c.LatencyScale = 10 * time.Minute
	}
	if c.RebuildEvery <= 0 {
		c.RebuildEvery = 6 * time.Hour
	}
	if c.SyncEvery <= 0 {
		c.SyncEvery = 30 * time.Second
	}
	if c.EmbedTimeout <= 0 {
		c.EmbedTimeout = 10 * time.Second
	}
	if c.Seed == 0 {
		c.Seed = 42
	}
}

// IsEnabled reports whether the stage runs (default true).
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Learner implements the learning stage.
type Learner struct {
	h     kernel.Host
	log   *slog.Logger
	db    *sql.DB
	cfg   Config
	cache *vectorCache
	prefs prefsCache
	now   func() time.Time

	mu      sync.RWMutex
	emb     Embedder // active embedding space
	sc      *scorer  // swapped on rebuild
	cents   [][]float32
	bandit  *Bandit
	candSig string

	rebuildMu sync.Mutex
	// flags of in-flight tasks (escalated / approval denied), by task id.
	flagMu sync.Mutex
	flags  map[string]*taskFlags

	stop   context.CancelFunc
	wg     sync.WaitGroup
	random *rand.Rand
}

type taskFlags struct{ escalated, denied bool }

// New migrates the learner's tables. Call Rebuild (or Start) before use;
// until then decisions use the description and rules stages only.
func New(h kernel.Host, st kernel.Store, cfg Config) (*Learner, error) {
	cfg.defaults()
	if err := st.Migrate("router.learn", migrations); err != nil {
		return nil, fmt.Errorf("router.learn: migrate: %w", err)
	}
	l := &Learner{h: h, log: h.Logger(), db: st.DB(), cfg: cfg, cache: newVectorCache(st.DB()), now: time.Now,
		bandit: NewBandit(cfg.Seed), flags: map[string]*taskFlags{},
		random: rand.New(rand.NewPCG(cfg.Seed, 1))}
	l.emb = l.primaryEmbedder()
	l.sc = &scorer{cfg: cfg, byID: map[string]Candidate{}, candVecs: map[string][][]float32{}, platt: DefaultPlatt()}
	return l, nil
}

// SetEmbedder forces an embedding space (tests, eval).
func (l *Learner) SetEmbedder(e Embedder) {
	l.mu.Lock()
	l.emb = e
	l.mu.Unlock()
}

// SetNow overrides the clock (tests).
func (l *Learner) SetNow(f func() time.Time) { l.now = f }

func (l *Learner) primaryEmbedder() Embedder {
	if strings.EqualFold(l.cfg.EmbedModel, "hash") {
		return HashEmbedder{}
	}
	if llm, err := kernel.Resolve[kernel.LLM](l.h, kernel.SvcLLM); err == nil {
		return &llmEmbedder{llm: llm, alias: l.cfg.EmbedModel}
	}
	return HashEmbedder{}
}

func (l *Learner) embedder() Embedder {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.emb
}

// embed embeds texts in the active space; when the embedding model fails
// the learner switches to the hashing space for good (until restart) and
// rebuilds, so vectors of different spaces are never mixed.
func (l *Learner) embed(ctx context.Context, texts []string) ([][]float32, Embedder, error) {
	e := l.embedder()
	ectx, cancel := context.WithTimeout(ctx, l.cfg.EmbedTimeout)
	defer cancel()
	vecs, err := l.cache.Embed(ectx, e, texts)
	if err == nil {
		return vecs, e, nil
	}
	if _, isHash := e.(HashEmbedder); isHash {
		return nil, e, err
	}
	l.log.Warn("router.learn: embedding model failed; switching to the built-in hashing embedder", "model", e.Model(), "err", err)
	l.SetEmbedder(HashEmbedder{})
	if rerr := l.Rebuild(ctx); rerr != nil {
		l.log.Warn("router.learn: rebuild after embedder switch", "err", rerr)
	}
	h := HashEmbedder{}
	vecs, err = l.cache.Embed(ctx, h, texts)
	return vecs, h, err
}

// ---------------------------------------------------------------- decide

// Query is one routing request handed over by the router.
type Query struct {
	Input       string
	Attachments []*nexusv1.Attachment
	Context     *nexusv1.CommandContext
	// Rule is the router's rule/keyword decision and RuleStage its stage
	// name; Strong means a rule matched (the learner only adds a runtime hint).
	Rule      *nexusv1.Route
	RuleStage string
	Strong    bool
	// Agents are the enabled agents (nil: read from the catalog).
	Agents []*nexusv1.Agent
	// LLM runs the router's LLM classifier; nil when no LLM is available.
	LLM func(ctx context.Context) (route *nexusv1.Route, model string, err error)
}

// Decision is the learner's result.
type Decision struct {
	Route *nexusv1.Route
	Stage string
	Model string

	model       string // embedding space
	cluster     int
	chosen      string
	confidence  float64
	stages      stageScores
	blended     map[string]float64
	usedLLM     bool
	runtimeHint string
	noFit       bool
}

// Confidence is the calibrated confidence of the chosen candidate.
func (d *Decision) Confidence() float64 { return d.confidence }

// RuntimeHint is the bandit's runtime choice ("" when none).
func (d *Decision) RuntimeHint() string { return d.runtimeHint }

// NoFit reports whether no candidate fits the request well.
func (d *Decision) NoFit() bool { return d.noFit }

// Route params set by the learner.
const (
	ParamRuntimeHint = "runtime_hint"
	ParamNoFit       = "no_fit"
)

// Decide scores the candidates and returns the routing decision.
func (l *Learner) Decide(ctx context.Context, q Query) (*Decision, error) {
	rule := q.Rule
	if rule == nil {
		rule = &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK}
	}
	cands := l.candidates(ctx, q.Agents)
	if q.Strong && (rule.Kind != nexusv1.CommandKind_COMMAND_KIND_TASK || rule.AgentId == "") {
		return &Decision{Route: rule, Stage: q.RuleStage, cluster: -1}, nil
	}
	if len(cands) == 0 {
		return nil, errors.New("router.learn: no candidates")
	}
	vecs, emb, err := l.embed(ctx, []string{q.Input})
	if err != nil {
		return nil, err
	}
	qv := vecs[0]
	sc, err := l.scorerFor(ctx, cands, emb)
	if err != nil {
		return nil, err
	}
	d := &Decision{model: emb.Model(), cluster: l.cluster(qv)}

	if q.Strong {
		d.Route, d.Stage, d.chosen = proto.Clone(rule).(*nexusv1.Route), q.RuleStage, rule.AgentId
		d.confidence = float64(rule.Confidence)
		l.addHint(d, sc, qv)
		return d, nil
	}

	// Scoring reads the classifier and the kNN set, which online learning
	// updates under the write lock.
	l.mu.RLock()
	sc = l.sc
	res := sc.score(q.Input, qv, "", true)
	l.mu.RUnlock()
	conf := sc.platt.Conf(res)
	unsure := conf < l.cfg.Accept || res.disagree()
	d.Stage = "learned"
	if unsure && q.LLM != nil {
		lr, model, err := q.LLM(ctx)
		switch {
		case err != nil:
			l.log.Warn("router.learn: LLM stage failed; using learned scores", "err", err)
		case lr != nil && lr.Kind != nexusv1.CommandKind_COMMAND_KIND_TASK && lr.Kind != nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED:
			d.Route, d.Stage, d.Model, d.usedLLM = lr, "llm", model, true
			d.confidence = float64(lr.Confidence)
			d.stages, d.blended = res.stageMap(), res.blended
			return d, nil
		case lr != nil:
			res.addRoute(StageLLM, sc.ids, lr.AgentId, float64(lr.Confidence), 2)
			conf = sc.platt.Conf(res)
			d.Stage, d.Model, d.usedLLM = "learned+llm", model, true
		}
	} else if unsure && rule.Kind != nexusv1.CommandKind_COMMAND_KIND_TASK && rule.Kind != nexusv1.CommandKind_COMMAND_KIND_UNSPECIFIED {
		// The rules leaned to search/capture and nothing contradicts them strongly.
		d.Route, d.Stage, d.confidence = proto.Clone(rule).(*nexusv1.Route), q.RuleStage, float64(rule.Confidence)
		d.stages, d.blended = res.stageMap(), res.blended
		return d, nil
	}
	d.stages, d.blended, d.confidence = res.stageMap(), res.blended, conf
	if len(res.ranked) == 0 {
		return nil, errors.New("router.learn: nothing ranked")
	}
	d.chosen = res.ranked[0]
	c := sc.byID[d.chosen]
	route := &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, Confidence: float32(conf), Tier: c.Tier}
	switch c.Kind {
	case KindWorkflow:
		route.Kind, route.WorkflowId, route.Tier = nexusv1.CommandKind_COMMAND_KIND_WORKFLOW, strings.TrimPrefix(c.ID, WorkflowPrefix), 0
	case KindTeam:
		route.AgentId, route.TeamId = c.ID, strings.TrimPrefix(c.ID, TeamPrefix)
	default:
		route.AgentId = c.ID
	}
	for _, id := range res.ranked[1:] {
		if len(route.Alternatives) == 3 {
			break
		}
		if sc.byID[id].Kind != KindWorkflow && res.blended[id] > 0.02 {
			route.Alternatives = append(route.Alternatives, id)
		}
	}
	route.Reason = res.explain(d.chosen)
	if route.Reason == "" {
		route.Reason = "learned router"
	}
	d.Route = route
	strongNeighbor := false
	for _, n := range res.neighbors {
		if n.ex.Confirmed() && n.sim >= 0.6 {
			strongNeighbor = true
		}
	}
	if conf < l.cfg.NoFit && !strongNeighbor {
		d.noFit = true
		setParam(route, ParamNoFit, structpb.NewBoolValue(true))
		route.Reason += fmt.Sprintf("; no good fit (confidence %.2f)", conf)
	}
	l.addHint(d, sc, qv)
	return d, nil
}

func setParam(r *nexusv1.Route, k string, v *structpb.Value) {
	if r.Params == nil {
		r.Params = &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	if r.Params.Fields == nil {
		r.Params.Fields = map[string]*structpb.Value{}
	}
	r.Params.Fields[k] = v
}

func (r *stageResult) stageMap() stageScores {
	out := stageScores{}
	for st, d := range r.dists {
		m := map[string]float64{"_weight": round3(r.weights[st])}
		for id, p := range d {
			if p >= 0.01 {
				m[id] = round3(p)
			}
		}
		out[st] = m
	}
	return out
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// addHint asks the bandit for the chosen agent's runtime.
func (l *Learner) addHint(d *Decision, sc *scorer, qv []float32) {
	c, ok := sc.byID[d.chosen]
	if !ok || c.Kind != KindAgent || len(c.Runtimes) < 2 || c.HasInputSchema {
		return
	}
	ch := l.bandit.Choose(d.cluster, c.ID, c.Runtimes)
	if ch.Arm == "" {
		return
	}
	d.runtimeHint = ch.Arm
	setParam(d.Route, ParamRuntimeHint, structpb.NewStringValue(ch.Arm))
	if ch.N > 0 && d.Route.Reason != "" {
		d.Route.Reason += fmt.Sprintf("; runtime %s (%d past runs)", ch.Arm, ch.N)
	}
}

func (l *Learner) cluster(v []float32) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.cents) == 0 || len(l.cents[0]) != len(v) {
		return -1
	}
	return Nearest(l.cents, v)
}

// scorerFor returns the current scorer, refreshed when the candidates or
// the embedding space changed.
func (l *Learner) scorerFor(ctx context.Context, cands []Candidate, emb Embedder) (*scorer, error) {
	sig := candSignature(cands, emb.Model())
	l.mu.RLock()
	sc, same := l.sc, l.candSig == sig
	l.mu.RUnlock()
	if same {
		return sc, nil
	}
	vecs, err := l.candidateVectors(ctx, cands, emb)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ns := *l.sc
	ns.cands, ns.candVecs = cands, vecs
	ns.ids, ns.byID = nil, map[string]Candidate{}
	for _, c := range cands {
		ns.ids = append(ns.ids, c.ID)
		ns.byID[c.ID] = c
	}
	if ns.clf != nil && ns.clf.Model != emb.Model() {
		ns.clf, ns.labeled, ns.examples = nil, 0, nil
	}
	l.sc, l.candSig = &ns, sig
	return &ns, nil
}

func candSignature(cands []Candidate, model string) string {
	var b strings.Builder
	b.WriteString(model)
	for _, c := range cands {
		fmt.Fprintf(&b, "\x00%s\x01%s\x01%v\x01%v", c.ID, strings.Join(c.Texts(), "\x02"), c.Runtimes, c.HasInputSchema)
	}
	return contentHash("sig", b.String())
}

func (l *Learner) candidateVectors(ctx context.Context, cands []Candidate, emb Embedder) (map[string][][]float32, error) {
	var texts []string
	var owner []string
	for _, c := range cands {
		for _, t := range c.Texts() {
			texts = append(texts, t)
			owner = append(owner, c.ID)
		}
	}
	vecs, err := l.cache.Embed(ctx, emb, texts)
	if err != nil {
		return nil, err
	}
	out := map[string][][]float32{}
	for i, v := range vecs {
		out[owner[i]] = append(out[owner[i]], v)
	}
	return out, nil
}

// Record stores the stage scores of a logged decision.
func (l *Learner) Record(ctx context.Context, decisionID string, d *Decision) {
	if d == nil || decisionID == "" || (d.stages == nil && d.runtimeHint == "") {
		return
	}
	if err := l.saveScores(ctx, decisionID, d); err != nil {
		l.log.Warn("router.learn: save scores", "decision", decisionID, "err", err)
	}
}

// ---------------------------------------------------------------- rebuild

// Rebuild retrains everything from history: the classifier (prequential
// pass, then extra epochs), calibration, clusters and bandit posteriors.
func (l *Learner) Rebuild(ctx context.Context) error {
	l.rebuildMu.Lock()
	defer l.rebuildMu.Unlock()
	emb := l.embedder()
	cands := l.candidates(ctx, nil)
	hist, err := LoadHistory(ctx, l.db)
	if err != nil {
		return err
	}
	exs := append(noteExamples(cands), hist...)
	texts := make([]string, len(exs))
	for i, e := range exs {
		texts[i] = e.Input
	}
	vecs, err := l.cache.Embed(ctx, emb, texts)
	if err != nil {
		return err
	}
	candVecs, err := l.candidateVectors(ctx, cands, emb)
	if err != nil {
		return err
	}
	sc := &scorer{cfg: l.cfg, cands: cands, candVecs: candVecs, byID: map[string]Candidate{}, platt: DefaultPlatt()}
	for _, c := range cands {
		sc.ids = append(sc.ids, c.ID)
		sc.byID[c.ID] = c
	}
	idx := make([]*indexed, len(exs))
	for i := range exs {
		idx[i] = &indexed{Example: exs[i], vec: vecs[i]}
	}
	dim := 0
	if len(candVecs) > 0 {
		for _, v := range candVecs {
			dim = len(v[0])
			break
		}
	} else if len(vecs) > 0 {
		dim = len(vecs[0])
	}
	pairs := train(sc, idx, emb.Model(), dim, l.cfg.Seed, true)
	sc.platt = FitPlatt(pairs, 30)

	// Clusters over real requests, then bandit posteriors from outcomes.
	var reqVecs [][]float32
	for _, e := range idx {
		if !e.Note {
			reqVecs = append(reqVecs, e.vec)
		}
	}
	k := max(1, min(8, len(reqVecs)/15))
	cents := KMeans(reqVecs, k, l.cfg.Seed)
	bandit := NewBandit(l.cfg.Seed)
	obs, err := loadBanditObs(ctx, l.db)
	if err != nil {
		return err
	}
	if len(obs) > 0 {
		ot := make([]string, len(obs))
		for i, o := range obs {
			ot[i] = o.input
		}
		ov, err := l.cache.Embed(ctx, emb, ot)
		if err != nil {
			return err
		}
		for i, o := range obs {
			c := -1
			if len(cents) > 0 {
				c = Nearest(cents, ov[i])
			}
			bandit.Update(c, o.agent, o.runtime, o.reward)
		}
	}

	l.mu.Lock()
	if l.emb.Model() == emb.Model() {
		l.sc, l.cents, l.bandit = sc, cents, bandit
		l.candSig = candSignature(cands, emb.Model())
	}
	l.mu.Unlock()
	if sc.clf != nil {
		if b, err := sc.clf.Marshal(); err == nil {
			_ = saveState(ctx, l.db, "classifier", b)
		}
	}
	l.log.Info("router.learn: rebuilt", "examples", len(idx), "labeled", sc.labeled, "candidates", len(cands),
		"clusters", len(cents), "bandit_obs", len(obs), "model", emb.Model(), "calibration_pairs", sc.platt.N)
	return nil
}

// train fits the scorer's classifier from examples in chronological order
// (prequential: with calibrate, each confirmed example is first predicted,
// giving honest calibration pairs, then learned), then runs 3 shuffled
// epochs. It sets sc.examples, sc.clf and sc.labeled and returns the
// calibration pairs.
func train(sc *scorer, idx []*indexed, model string, dim int, seed uint64, calibrate bool) []CalPair {
	sc.clf = NewClassifier(model, dim)
	sc.examples, sc.labeled = nil, 0
	var pairs []CalPair
	for _, e := range idx {
		if calibrate && e.Confirmed() && len(sc.examples) > 0 {
			r := sc.score(e.Input, e.vec, "", sc.labeled > 0)
			if len(r.ranked) > 0 {
				pairs = append(pairs, CalPair{Top: r.top, Margin: r.margin, Evidence: r.evidence, Correct: r.ranked[0] == e.Label})
			}
		}
		learnOne(sc, e)
	}
	r := rand.New(rand.NewPCG(seed, 3))
	if calibrate && len(pairs) > 0 && dim > 0 {
		// Background negatives: random directions are unrelated to every
		// candidate; the calibrated confidence must be low for them.
		for i := 0; i < max(10, len(pairs)/5); i++ {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(r.NormFloat64())
			}
			res := sc.score("", normalize(v), "", sc.labeled > 0)
			pairs = append(pairs, CalPair{Top: res.top, Margin: res.margin, Evidence: res.evidence})
		}
	}
	order := make([]*indexed, len(idx))
	copy(order, idx)
	for epoch := 0; epoch < 3; epoch++ {
		r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, e := range order {
			sc.clf.Update(e.vec, sc.ids, sc.auxOf(e), e.Label, e.Weight, e.Negative)
		}
	}
	sc.clf.Labeled = sc.labeled
	return pairs
}

// learnOne applies one example to the scorer (classifier step + kNN set).
func learnOne(sc *scorer, e *indexed) {
	sc.clf.Update(e.vec, sc.ids, sc.auxOf(e), e.Label, e.Weight, e.Negative)
	sc.examples = append(sc.examples, e)
	if e.Confirmed() {
		sc.labeled++
	}
}

func noteExamples(cands []Candidate) []Example {
	var out []Example
	for _, c := range cands {
		for _, x := range c.Examples {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, Example{Input: x, Label: c.ID, Weight: wNoteExample, Note: true})
			}
		}
	}
	return out
}

type banditObs struct {
	input, agent, runtime string
	reward                float64
}

func loadBanditObs(ctx context.Context, db *sql.DB) ([]banditObs, error) {
	rows, err := db.QueryContext(ctx, `SELECT d.input, COALESCE(NULLIF(s.chosen, ''), d.agent_id), o.runtime, o.reward
		FROM router_learn_outcomes o
		JOIN router_decisions d ON d.id = o.decision_id
		LEFT JOIN router_learn_scores s ON s.decision_id = o.decision_id
		WHERE o.runtime != '' AND o.reward >= 0
		ORDER BY o.created_at`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []banditObs
	for rows.Next() {
		var o banditObs
		if err := rows.Scan(&o.input, &o.agent, &o.runtime, &o.reward); err != nil {
			return nil, err
		}
		if o.agent != "" {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- feedback

// Outcome is the result of the task a decision produced.
type Outcome struct {
	DecisionID string
	TaskID     string
	State      nexusv1.TaskState
	CostEUR    float64
	Latency    time.Duration
	Corrected  bool
	Denied     bool
	Escalated  bool
	// Runtime that ran the task ("" = unknown; the hint is assumed).
	Runtime string
}

// Reward is the bandit reward: success − cost − latency − correction
// penalties, clamped to [0, 1].
func (l *Learner) Reward(o Outcome) float64 {
	r := 0.0
	if o.State == nexusv1.TaskState_TASK_STATE_SUCCEEDED {
		r = 1
	}
	r -= math.Min(0.3, 0.3*o.CostEUR/l.cfg.CostScaleEUR)
	r -= math.Min(0.2, 0.2*o.Latency.Seconds()/l.cfg.LatencyScale.Seconds())
	if o.Corrected {
		r -= 0.5
	}
	if o.Denied || o.Escalated {
		r -= 0.2
	}
	return math.Max(0, math.Min(1, r))
}

func stateWord(s nexusv1.TaskState) string {
	switch s {
	case nexusv1.TaskState_TASK_STATE_SUCCEEDED:
		return stateSuccess
	case nexusv1.TaskState_TASK_STATE_FAILED:
		return stateFailed
	case nexusv1.TaskState_TASK_STATE_CANCELLED:
		return stateCanceled
	}
	return strings.ToLower(strings.TrimPrefix(s.String(), "TASK_STATE_"))
}

// RecordOutcome stores the outcome of a decision's task and updates the
// classifier, the kNN set and the bandit online. Only a decision's first
// outcome (or its correction, see SyncCorrections) is learned; later ones
// only update the stored row. Corrected decisions never reward a runtime.
func (l *Learner) RecordOutcome(ctx context.Context, o Outcome) error {
	if o.DecisionID == "" {
		return errors.New("router.learn: outcome needs a decision id")
	}
	var hint string
	var cluster int
	_ = l.db.QueryRowContext(ctx, `SELECT runtime_hint, cluster FROM router_learn_scores WHERE decision_id = ?`, o.DecisionID).Scan(&hint, &cluster)
	if o.Runtime == "" {
		o.Runtime = hint
	}
	var prev string
	var prevCorrected int
	err := l.db.QueryRowContext(ctx, `SELECT state, corrected FROM router_learn_outcomes WHERE decision_id = ?`, o.DecisionID).Scan(&prev, &prevCorrected)
	// Learned already: an earlier terminal outcome or the correction.
	learned := err == nil && (prev != "" || prevCorrected != 0)
	reward := -1.0
	if o.Runtime != "" && o.State != nexusv1.TaskState_TASK_STATE_CANCELLED {
		reward = l.Reward(o)
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO router_learn_outcomes
		(decision_id, task_id, created_at, state, cost_eur, latency_ms, corrected, denied, escalated, runtime, reward)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(decision_id) DO UPDATE SET task_id = excluded.task_id, state = excluded.state, cost_eur = excluded.cost_eur,
			latency_ms = excluded.latency_ms, corrected = MAX(corrected, excluded.corrected), denied = excluded.denied,
			escalated = excluded.escalated, runtime = excluded.runtime, reward = excluded.reward`,
		o.DecisionID, o.TaskID, l.now().UnixMilli(), stateWord(o.State), o.CostEUR, o.Latency.Milliseconds(),
		boolInt(o.Corrected), boolInt(o.Denied), boolInt(o.Escalated), o.Runtime, reward)
	if err != nil {
		return err
	}
	if learned {
		return nil
	}
	return l.learnDecision(ctx, o.DecisionID, reward, o.Runtime)
}

// learnDecision re-reads a decision (with its correction and outcome) and
// applies its examples online.
func (l *Learner) learnDecision(ctx context.Context, decisionID string, reward float64, runtime string) error {
	var h historyRow
	var denied, esc int
	err := l.db.QueryRowContext(ctx, `SELECT d.id, d.input, d.agent_id, d.corrected_agent, d.task_id, d.created_at, d.kind,
		COALESCE(s.chosen, ''), COALESCE(s.cluster, -1), COALESCE(o.state, ''), COALESCE(o.denied, 0), COALESCE(o.escalated, 0)
		FROM router_decisions d
		LEFT JOIN router_learn_scores s ON s.decision_id = d.id
		LEFT JOIN router_learn_outcomes o ON o.decision_id = d.id
		WHERE d.id = ?`, decisionID).Scan(&h.id, &h.input, &h.agent, &h.corrected, &h.taskID, &h.created, &h.kind,
		&h.chosen, new(int), &h.state, &denied, &esc)
	if err != nil {
		return fmt.Errorf("router.learn: decision %s: %w", decisionID, err)
	}
	h.denied, h.escalated = denied != 0, esc != 0
	exs := examplesOf(h)
	if len(exs) == 0 && reward < 0 {
		return nil
	}
	vecs, emb, err := l.embed(ctx, []string{h.input})
	if err != nil {
		return err
	}
	v := vecs[0]
	routed := h.chosen
	if routed == "" {
		routed = h.agent
	}
	l.mu.Lock()
	sc := l.sc
	if sc.clf == nil || sc.clf.Model != emb.Model() || sc.clf.Dim != len(v) {
		sc.clf = NewClassifier(emb.Model(), len(v))
	}
	for i := range exs {
		learnOne(sc, &indexed{Example: exs[i], vec: v})
	}
	sc.clf.Labeled = sc.labeled
	cl := -1
	if len(l.cents) > 0 && len(l.cents[0]) == len(v) {
		cl = Nearest(l.cents, v)
	}
	b := l.bandit
	clf := sc.clf
	l.mu.Unlock()
	if reward >= 0 && runtime != "" && routed != "" && h.corrected == "" {
		b.Update(cl, routed, runtime, reward)
	}
	if data, err := clf.Marshal(); err == nil {
		_ = saveState(ctx, l.db, "classifier", data)
	}
	return nil
}

// Stats summarises the learner (for logs and the eval command).
type Stats struct {
	Model      string
	Candidates int
	Examples   int
	Labeled    int
	Clusters   int
	Platt      Platt
}

// Stats returns a snapshot.
func (l *Learner) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return Stats{Model: l.emb.Model(), Candidates: len(l.sc.cands), Examples: len(l.sc.examples),
		Labeled: l.sc.labeled, Clusters: len(l.cents), Platt: l.sc.platt}
}

// Bandit returns the runtime bandit (tests).
func (l *Learner) Bandit() *Bandit {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.bandit
}

// rankedIDs is used by tests.
func rankedIDs(m map[string]float64) []string {
	ids := sortedKeys(m)
	sort.SliceStable(ids, func(i, j int) bool { return m[ids[i]] > m[ids[j]] })
	return ids
}
