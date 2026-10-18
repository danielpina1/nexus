package learn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/tasks/taskstest"
	"github.com/danielpina1/nexus/backend/kernel"
)

// accuracyWith trains on the first n examples of train and measures top-1
// accuracy and mean calibrated confidence on test.
func accuracyWith(t *testing.T, train_, test []Example, n int) (acc float64, sc *scorer) {
	t.Helper()
	ctx := context.Background()
	cfg := Config{}
	cfg.defaults()
	emb := HashEmbedder{}
	cache := newVectorCache(nil)
	cands := synthCandidates()
	var ctexts, owner []string
	for _, c := range cands {
		for _, x := range c.Texts() {
			ctexts, owner = append(ctexts, x), append(owner, c.ID)
		}
	}
	cv, _ := cache.Embed(ctx, emb, ctexts)
	candVecs := map[string][][]float32{}
	for i, v := range cv {
		candVecs[owner[i]] = append(candVecs[owner[i]], v)
	}
	sc = newScorer(cfg, cands, candVecs)
	exs := append(noteExamples(cands), train_[:n]...)
	idx := make([]*indexed, len(exs))
	for i := range exs {
		v, _ := cache.Embed(ctx, emb, []string{exs[i].Input})
		idx[i] = &indexed{Example: exs[i], vec: v[0]}
	}
	sc.platt = FitPlatt(train(sc, idx, emb.Model(), 512, 42, true), 30)
	hits := 0
	for _, e := range test {
		v, _ := cache.Embed(ctx, emb, []string{e.Input})
		r := sc.score(e.Input, v[0], "", true)
		if r.ranked[0] == e.Label {
			hits++
		}
	}
	return float64(hits) / float64(len(test)), sc
}

func TestAccuracyImprovesWithLabels(t *testing.T) {
	train_ := synthExamples(synthRequests(200, 1), "tr")
	test := synthExamples(synthRequests(150, 2), "te")
	var accs []float64
	for _, n := range []int{0, 10, 40, 200} {
		acc, _ := accuracyWith(t, train_, test, n)
		t.Logf("labels=%3d accuracy=%.3f", n, acc)
		accs = append(accs, acc)
	}
	if accs[3] < accs[0]+0.25 {
		t.Fatalf("accuracy did not improve enough with labels: %v", accs)
	}
	if accs[3] < 0.85 {
		t.Fatalf("accuracy with 200 labels = %.3f, want >= 0.85", accs[3])
	}
	if accs[2] < accs[1]-0.05 || accs[3] < accs[2]-0.05 {
		t.Fatalf("accuracy should grow with labels: %v", accs)
	}
}

func TestOfflineEvalCalibration(t *testing.T) {
	hist := synthExamples(synthRequests(150, 3), "d")
	// A few corrections and failures, like real logs.
	hist = append(hist, Example{DecisionID: "neg1", Input: "nginx is down again", Label: "coder", Weight: wFailure, Negative: true})
	rep, err := EvaluateExamples(context.Background(), HashEmbedder{}, synthCandidates(), hist, Config{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	rep.Print(&b)
	t.Log("\n" + b.String())
	if rep.Tested != 150 || rep.Folds != 150 {
		t.Fatalf("tested %d folds %d, want leave-one-out over 150", rep.Tested, rep.Folds)
	}
	if rep.Accuracy < 0.85 || rep.Top2 < rep.Accuracy {
		t.Fatalf("accuracy %.3f top2 %.3f", rep.Accuracy, rep.Top2)
	}
	if rep.ECE > 0.15 {
		t.Fatalf("ECE %.3f, want <= 0.15", rep.ECE)
	}
	if rep.StageAccuracy[StageDesc] >= rep.Accuracy {
		t.Fatalf("the description stage alone (%.3f) should be worse than the blend (%.3f)", rep.StageAccuracy[StageDesc], rep.Accuracy)
	}
}

func TestCalibrationSeparatesRightFromWrong(t *testing.T) {
	train_ := synthExamples(synthRequests(120, 4), "tr")
	test := synthExamples(synthRequests(200, 5), "te")
	_, sc := accuracyWith(t, train_, test, 120)
	ctx := context.Background()
	var right, wrong []float64
	cache := newVectorCache(nil)
	for _, e := range test {
		v, _ := cache.Embed(ctx, HashEmbedder{}, []string{e.Input})
		r := sc.score(e.Input, v[0], "", true)
		c := sc.platt.Conf(r)
		if r.ranked[0] == e.Label {
			right = append(right, c)
		} else {
			wrong = append(wrong, c)
		}
	}
	// Unrelated input gets low confidence.
	v, _ := cache.Embed(ctx, HashEmbedder{}, []string{"zxqv lorem ipsum wibble"})
	r := sc.score("zxqv lorem ipsum wibble", v[0], "", true)
	if c := sc.platt.Conf(r); c > 0.4 {
		t.Fatalf("gibberish confidence %.2f, want low", c)
	}
	if mean(right) < 0.7 {
		t.Fatalf("mean confidence of right answers %.2f", mean(right))
	}
	if len(wrong) > 0 && mean(wrong) >= mean(right) {
		t.Fatalf("wrong answers (%.2f) as confident as right ones (%.2f)", mean(wrong), mean(right))
	}
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func TestBanditConvergesToBestRuntime(t *testing.T) {
	b := NewBandit(7)
	arms := []string{"ollama", "codex", "claude-code"} // policy order puts the worst first
	p := map[string]float64{"ollama": 0.3, "codex": 0.55, "claude-code": 0.9}
	r := rand.New(rand.NewPCG(9, 9))
	best := 0
	for i := 0; i < 400; i++ {
		ch := b.Choose(0, "researcher", arms)
		reward := 0.0
		if r.Float64() < p[ch.Arm] {
			reward = 1
		}
		b.Update(0, "researcher", ch.Arm, reward)
		if i >= 300 && ch.Arm == "claude-code" {
			best++
		}
	}
	if best < 85 {
		t.Fatalf("best arm chosen %d/100 times in the last rounds", best)
	}
	// A new cluster with no data falls back to the pooled posterior.
	if ch := b.Choose(5, "researcher", arms); !ch.Pooled {
		t.Fatalf("empty cluster should use pooled stats")
	}
	// Without data the policy order wins most of the time.
	b2 := NewBandit(3)
	first := 0
	for i := 0; i < 200; i++ {
		if b2.Choose(0, "x", arms).Arm == "ollama" {
			first++
		}
	}
	if first < 70 {
		t.Fatalf("policy order should be preferred without data (%d/200)", first)
	}
}

func TestPlattFit(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var pairs []CalPair
	for i := 0; i < 2000; i++ {
		top := r.Float64()
		// true probability of being right = top (perfectly informative)
		pairs = append(pairs, CalPair{Top: top, Margin: top / 2, Evidence: 0.5, Correct: r.Float64() < top})
	}
	p := FitPlatt(pairs, 30)
	for _, x := range []float64{0.2, 0.5, 0.8} {
		if got := p.Apply(x, x/2, 0.5); math.Abs(got-x) > 0.1 {
			t.Fatalf("calibrated(%.1f) = %.2f", x, got)
		}
	}
	if FitPlatt(pairs[:5], 30) != DefaultPlatt() {
		t.Fatalf("too few pairs must keep defaults")
	}
}

func TestKMeansAndClassifierPersistence(t *testing.T) {
	ctx := context.Background()
	reqs := synthRequests(90, 6)
	var vecs [][]float32
	for _, q := range reqs {
		v, _ := HashEmbedder{}.Embed(ctx, []string{q.input})
		vecs = append(vecs, v[0])
	}
	cents := KMeans(vecs, 6, 1)
	if len(cents) != 6 {
		t.Fatalf("centroids %d", len(cents))
	}
	c := NewClassifier("hash", 512)
	cands := []string{"a", "b"}
	for i := 0; i < 50; i++ {
		c.Update(vecs[0], cands, nil, "a", 1, false)
		c.Update(vecs[1], cands, nil, "b", 1, false)
	}
	b, _ := c.Marshal()
	c2, err := UnmarshalClassifier(b)
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := c.Predict(vecs[0], cands, nil), c2.Predict(vecs[0], cands, nil)
	if p1["a"] < 0.8 || math.Abs(p1["a"]-p2["a"]) > 1e-9 {
		t.Fatalf("persisted classifier differs: %v vs %v", p1, p2)
	}
	// Weak negatives push a class down.
	before := c.Predict(vecs[0], cands, nil)["a"]
	for i := 0; i < 20; i++ {
		c.Update(vecs[0], cands, nil, "a", 0.3, true)
	}
	if after := c.Predict(vecs[0], cands, nil)["a"]; after >= before {
		t.Fatalf("negative updates did not lower p(a): %.3f → %.3f", before, after)
	}
}

// ---------------------------------------------------------------- end to end

const routerDDL = `CREATE TABLE router_decisions (
	id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, input TEXT NOT NULL, context TEXT NOT NULL DEFAULT '{}',
	stage TEXT NOT NULL, kind INTEGER NOT NULL, agent_id TEXT NOT NULL DEFAULT '', confidence REAL NOT NULL,
	reason TEXT NOT NULL DEFAULT '', alternatives TEXT NOT NULL DEFAULT '[]', model TEXT NOT NULL DEFAULT '',
	latency_ms INTEGER NOT NULL DEFAULT 0, task_id TEXT NOT NULL DEFAULT '', corrected_agent TEXT NOT NULL DEFAULT '');
CREATE TABLE router_corrections (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, decision_id TEXT NOT NULL DEFAULT '',
	task_id TEXT NOT NULL DEFAULT '', input TEXT NOT NULL DEFAULT '', from_agent TEXT NOT NULL DEFAULT '', to_agent TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '');`

// mdCatalog is an agent catalog that returns markdown (for `examples:`).
type mdCatalog struct {
	*taskstest.Catalog
	md map[string]string
}

func (c *mdCatalog) GetAgent(ctx context.Context, id string) (*nexusv1.Agent, string, string, error) {
	a, instr, _, err := c.Catalog.GetAgent(ctx, id)
	return a, instr, c.md[id], err
}

type fixture struct {
	env *taskstest.Env
	l   *Learner
	llm *countingLLM
}

type countingLLM struct {
	mu    sync.Mutex
	calls int
	route *nexusv1.Route
}

func (c *countingLLM) fn(context.Context) (*nexusv1.Route, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.route == nil {
		return nil, "", errors.New("no route")
	}
	return proto.Clone(c.route).(*nexusv1.Route), "fast-local", nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	env := taskstest.New(t)
	if _, err := env.Store.DB().Exec(routerDDL); err != nil {
		t.Fatal(err)
	}
	cat := &mdCatalog{Catalog: taskstest.NewCatalog(), md: map[string]string{}}
	for _, a := range synthAgents {
		ag := taskstest.Agent(a.id, a.name, a.desc, nexusv1.Tier_TIER_MEDIUM)
		ag.Runtime = "auto"
		cat.Add(ag, "x")
		cat.md[a.id] = fmt.Sprintf("---\nid: %s\nexamples: [%q]\n---\n", a.id, a.examples[0])
	}
	env.Host.Provide(kernel.SvcAgents, kernel.AgentCatalog(cat))
	env.Host.ConfigYAML = "embed_model: hash\nseed: 5\n"
	m := &Module{}
	if err := m.Init(context.Background(), env.Host); err != nil {
		t.Fatal(err)
	}
	return &fixture{env: env, l: m.Learner, llm: &countingLLM{}}
}

// seed inserts n decisions with successful outcomes.
func (f *fixture) seed(t *testing.T, reqs []synthReq) {
	t.Helper()
	db := f.env.Store.DB()
	for i, q := range reqs {
		id := fmt.Sprintf("rtd_%04d", i)
		if _, err := db.Exec(`INSERT INTO router_decisions (id, created_at, input, stage, kind, agent_id, confidence, task_id)
			VALUES (?, ?, ?, 'keyword', 3, ?, 0.5, ?)`, id, 1000+i, q.input, q.label, "tsk_"+id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO router_learn_outcomes (decision_id, task_id, created_at, state, runtime, reward)
			VALUES (?, ?, ?, 'succeeded', 'codex', 0.9)`, id, "tsk_"+id, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) decide(t *testing.T, input string) *Decision {
	t.Helper()
	d, err := f.l.Decide(context.Background(), Query{Input: input, Rule: &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK},
		RuleStage: "default", LLM: f.llm.fn})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDecideLearnsFromHistoryAndExplains(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seed(t, synthRequests(150, 21))
	if err := f.l.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	st := f.l.Stats()
	if st.Labeled != 150 || st.Model != "hash-v1-512" || st.Clusters == 0 {
		t.Fatalf("stats %+v", st)
	}
	d := f.decide(t, "we are out of milk and detergent")
	if d.Route.AgentId != "grocery" {
		t.Fatalf("routed to %q (%s)", d.Route.AgentId, d.Route.Reason)
	}
	if !strings.Contains(d.Route.Reason, "similar past request") || !strings.Contains(d.Route.Reason, "classifier") {
		t.Fatalf("reason %q", d.Route.Reason)
	}
	if d.Route.Confidence < 0.75 || f.llm.calls != 0 {
		t.Fatalf("confident decision should skip the LLM: conf %.2f calls %d", d.Route.Confidence, f.llm.calls)
	}
	if h := d.Route.GetParams().GetFields()[ParamRuntimeHint].GetStringValue(); h == "" {
		t.Fatalf("no runtime hint: %v", d.Route.Params)
	}
	// Scores are logged per stage.
	f.l.Record(ctx, "rtd_new", d)
	var stages string
	if err := f.env.Store.DB().QueryRow(`SELECT stages FROM router_learn_scores WHERE decision_id = 'rtd_new'`).Scan(&stages); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{StageDesc, StageKNN, StageClassifier} {
		if !strings.Contains(stages, st) {
			t.Fatalf("stage %s missing in %s", st, stages)
		}
	}
	// Gibberish: unsure → LLM consulted; still no fit → no_fit flag.
	f.llm.route = &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, Confidence: 0.2}
	g := f.decide(t, "qwzx flarb snorkel parliament harmonica")
	if f.llm.calls != 1 {
		t.Fatalf("LLM calls %d, want 1 for an unsure request", f.llm.calls)
	}
	if !g.NoFit() || !g.Route.GetParams().GetFields()[ParamNoFit].GetBoolValue() {
		t.Fatalf("gibberish should be no-fit: conf %.2f %s", g.Confidence(), g.Route.Reason)
	}
	// The LLM can override the kind (search).
	f.llm.route = &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_SEARCH, Confidence: 0.9, Reason: "question"}
	s := f.decide(t, "when did I last renew my passport")
	if s.Route.Kind != nexusv1.CommandKind_COMMAND_KIND_SEARCH || s.Stage != "llm" {
		t.Fatalf("LLM search route not kept: %v %s", s.Route.Kind, s.Stage)
	}
}

func TestColdStartUsesExamplesAndDescriptions(t *testing.T) {
	f := newFixture(t)
	if err := f.l.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := f.decide(t, "please restart the service, it hangs")
	if d.Route.AgentId != "ops" {
		t.Fatalf("cold start picked %q: %s", d.Route.AgentId, d.Route.Reason)
	}
	// Strong rule matches pass through with only a runtime hint.
	sd, err := f.l.Decide(context.Background(), Query{Input: "do the groceries", Strong: true, RuleStage: "rule",
		Rule: &nexusv1.Route{Kind: nexusv1.CommandKind_COMMAND_KIND_TASK, AgentId: "grocery", Confidence: 0.95, Reason: "matched rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if sd.Route.AgentId != "grocery" || sd.Stage != "rule" || sd.RuntimeHint() == "" {
		t.Fatalf("strong rule changed: %+v hint %q", sd.Route, sd.RuntimeHint())
	}
}

func TestFeedbackLoopFromEvents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.l.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	db := f.env.Store.DB()
	// A routed decision whose task succeeds on codex.
	d := f.decide(t, "nginx is down again")
	if _, err := db.Exec(`INSERT INTO router_decisions (id, created_at, input, stage, kind, agent_id, confidence, task_id)
		VALUES ('rtd_a', 1, 'nginx is down again', 'learned', 3, ?, 0.8, 'tsk_a')`, d.Route.AgentId); err != nil {
		t.Fatal(err)
	}
	f.l.Record(ctx, "rtd_a", d)
	start := time.Unix(1000, 0)
	task := &nexusv1.Task{Id: "tsk_a", AgentId: d.Route.AgentId, State: nexusv1.TaskState_TASK_STATE_ESCALATING}
	f.l.HandleEvent(ctx, &nexusv1.Event{TaskId: "tsk_a", Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: task}}})
	done := proto.Clone(task).(*nexusv1.Task)
	done.State = nexusv1.TaskState_TASK_STATE_SUCCEEDED
	done.CreatedAt, done.UpdatedAt = timestamppb.New(start), timestamppb.New(start.Add(2*time.Minute))
	done.Cost = &nexusv1.Cost{Amount: &nexusv1.Money{Currency: "EUR", MinorUnits: 10}, Runtime: "codex"}
	f.l.HandleEvent(ctx, &nexusv1.Event{TaskId: "tsk_a", Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: done}}})
	var state, runtime string
	var esc int
	var reward float64
	if err := db.QueryRow(`SELECT state, escalated, runtime, reward FROM router_learn_outcomes WHERE decision_id = 'rtd_a'`).
		Scan(&state, &esc, &runtime, &reward); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || esc != 1 || runtime != "codex" || reward <= 0 || reward >= 1 {
		t.Fatalf("outcome %s esc=%d %s reward=%.2f", state, esc, runtime, reward)
	}
	if st := f.l.Stats(); st.Labeled != 1 {
		t.Fatalf("labeled %d after one success", st.Labeled)
	}
	// A correction is picked up by the sync and learned once.
	if _, err := db.Exec(`INSERT INTO router_decisions (id, created_at, input, stage, kind, agent_id, confidence, task_id, corrected_agent)
		VALUES ('rtd_b', 2, 'log this taxi fatura', 'learned', 3, 'ops', 0.5, 'tsk_b', 'receipts')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO router_corrections (id, created_at, decision_id, to_agent) VALUES ('c1', 5, 'rtd_b', 'receipts')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.l.SyncCorrections(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := f.l.Stats(); st.Labeled != 2 {
		t.Fatalf("labeled %d after a correction (synced twice)", st.Labeled)
	}
	// Its cancelled task does not count again.
	cancelled := &nexusv1.Task{Id: "tsk_b", State: nexusv1.TaskState_TASK_STATE_CANCELLED}
	f.l.HandleEvent(ctx, &nexusv1.Event{Body: &nexusv1.Event_TaskUpdated{TaskUpdated: &nexusv1.TaskUpdated{Task: cancelled}}})
	if st := f.l.Stats(); st.Labeled != 2 {
		t.Fatalf("labeled %d after the corrected task ended", st.Labeled)
	}
	// History reflects both.
	hist, err := LoadHistory(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, e := range hist {
		labels[fmt.Sprintf("%s/%s/%v", e.DecisionID, e.Label, e.Negative)] = true
	}
	for _, want := range []string{"rtd_a/" + d.Route.AgentId + "/false", "rtd_b/receipts/false", "rtd_b/ops/true"} {
		if !labels[want] {
			t.Fatalf("history missing %s: %v", want, labels)
		}
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seed(t, synthRequests(60, 31))
	if err := f.l.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	d1 := f.decide(t, "summarise studies about creatine worth it")
	// A second learner on the same store (a restart) loads the saved
	// weights at Start and rebuilds to the same model.
	l2, err := New(f.env.Host, f.env.Store, Config{EmbedModel: "hash", Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := loadState(ctx, f.env.Store.DB(), "classifier"); b == nil {
		t.Fatal("classifier weights not persisted")
	}
	if err := l2.Start(); err != nil {
		t.Fatal(err)
	}
	defer l2.Stop()
	if err := l2.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	d2, err := l2.Decide(ctx, Query{Input: "summarise studies about creatine worth it"})
	if err != nil {
		t.Fatal(err)
	}
	if d1.Route.AgentId != d2.Route.AgentId || math.Abs(d1.Confidence()-d2.Confidence()) > 1e-6 {
		t.Fatalf("restart changed the decision: %s %.4f vs %s %.4f", d1.Route.AgentId, d1.Confidence(), d2.Route.AgentId, d2.Confidence())
	}
	if l2.Stats().Labeled != 60 {
		t.Fatalf("labeled after restart %d", l2.Stats().Labeled)
	}
}

func TestEmbedderFallback(t *testing.T) {
	env := taskstest.New(t)
	if _, err := env.Store.DB().Exec(routerDDL); err != nil {
		t.Fatal(err)
	}
	env.Host.Provide(kernel.SvcAgents, kernel.AgentCatalog(taskstest.NewCatalog(
		taskstest.Agent("ops", "Ops", "Monitors servers and restarts services", nexusv1.Tier_TIER_SIMPLE))))
	env.Host.Provide(kernel.SvcLLM, &taskstest.LLM{}) // Embed always fails
	l, err := New(env.Host, env.Store, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if l.embedder().Model() != "llm:embed" {
		t.Fatalf("primary embedder %s", l.embedder().Model())
	}
	d, err := l.Decide(context.Background(), Query{Input: "restart nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Route.AgentId != "ops" || l.embedder().Model() != "hash-v1-512" {
		t.Fatalf("fallback: %s %s", d.Route.AgentId, l.embedder().Model())
	}
}
