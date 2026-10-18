package learn

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danielpina1/nexus/backend/internal/vault/agentdef"
	"github.com/danielpina1/nexus/backend/internal/vault/teamdef"
	"github.com/danielpina1/nexus/backend/internal/workflow"
	"github.com/danielpina1/nexus/backend/kernel"
)

// EvalReport is the result of an offline evaluation.
type EvalReport struct {
	Model      string
	Candidates int
	Examples   int // all examples used for training (incl. note examples)
	Tested     int // confirmed examples evaluated
	Folds      int
	Accuracy   float64
	Top2       float64
	// ECE is the expected calibration error of the calibrated confidence
	// (10 bins); Brier its mean squared error.
	ECE   float64
	Brier float64
	Bins  []CalBin
	// StageAccuracy is the top-1 accuracy of each stage alone.
	StageAccuracy map[string]float64
	// LoggedAccuracy is how often the logged decision was right (successes
	// vs corrections among the tested examples).
	LoggedAccuracy float64
	// AskRate is the share of requests whose confidence is below accept
	// (the user would be asked or the LLM consulted), and AcceptedAccuracy
	// the accuracy of the others.
	AskRate          float64
	AcceptedAccuracy float64
}

// CalBin is one reliability-diagram bin.
type CalBin struct {
	Lo, Hi   float64
	N        int
	MeanConf float64
	Accuracy float64
}

// Evaluate replays the logged decisions in db: every confirmed example
// (a correction or a successful outcome) is predicted by a model trained
// on the others — leave-one-out up to 300 examples, else `folds`-fold —
// and accuracy, top-2 and calibration are reported.
func Evaluate(ctx context.Context, db *sql.DB, emb Embedder, cands []Candidate, cfg Config, folds int) (*EvalReport, error) {
	hist, err := LoadHistory(ctx, db)
	if err != nil {
		return nil, err
	}
	// Cached vectors of the hub's embedding model are reused (and new ones
	// stored) when the store has the learner's tables.
	cache := newVectorCache(nil)
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM router_learn_vectors LIMIT 1`); err == nil {
		cache = newVectorCache(db)
	}
	return evaluate(ctx, cache, emb, cands, hist, cfg, folds)
}

// EvaluateExamples evaluates on an explicit example list (synthetic data).
func EvaluateExamples(ctx context.Context, emb Embedder, cands []Candidate, hist []Example, cfg Config, folds int) (*EvalReport, error) {
	return evaluate(ctx, newVectorCache(nil), emb, cands, hist, cfg, folds)
}

func evaluate(ctx context.Context, cache *vectorCache, emb Embedder, cands []Candidate, hist []Example, cfg Config, folds int) (*EvalReport, error) {
	cfg.defaults()
	exs := append(noteExamples(cands), hist...)
	texts := make([]string, len(exs))
	for i, e := range exs {
		texts[i] = e.Input
	}
	vecs, err := cache.Embed(ctx, emb, texts)
	if err != nil {
		return nil, err
	}
	idx := make([]*indexed, len(exs))
	for i := range exs {
		idx[i] = &indexed{Example: exs[i], vec: vecs[i]}
	}
	var ctexts []string
	var owner []string
	for _, c := range cands {
		for _, t := range c.Texts() {
			ctexts = append(ctexts, t)
			owner = append(owner, c.ID)
		}
	}
	cv, err := cache.Embed(ctx, emb, ctexts)
	if err != nil {
		return nil, err
	}
	candVecs := map[string][][]float32{}
	for i, v := range cv {
		candVecs[owner[i]] = append(candVecs[owner[i]], v)
	}
	var test []int
	for i, e := range idx {
		if e.Confirmed() {
			test = append(test, i)
		}
	}
	rep := &EvalReport{Model: emb.Model(), Candidates: len(cands), Examples: len(idx), Tested: len(test),
		StageAccuracy: map[string]float64{}}
	if len(test) == 0 {
		return rep, nil
	}
	if folds <= 0 {
		folds = 10
	}
	if len(test) <= 300 {
		folds = len(test)
	}
	if folds > len(test) {
		folds = len(test)
	}
	rep.Folds = folds
	dim := 0
	if len(vecs) > 0 {
		dim = len(vecs[0])
	}
	type pred struct {
		conf    float64
		correct bool
		top2    bool
	}
	var preds []pred
	// Calibration is fitted once from a prequential pass over everything
	// (each pair is predicted before its example is learned, so it is honest).
	full := newScorer(cfg, cands, candVecs)
	platt := FitPlatt(train(full, idx, emb.Model(), dim, cfg.Seed, true), 30)
	stageHits := map[string]int{}
	stageN := map[string]int{}
	logged, loggedN := 0, 0
	for f := 0; f < folds; f++ {
		held := map[string]bool{}
		heldIdx := map[int]bool{}
		for j, ti := range test {
			if j%folds == f {
				heldIdx[ti] = true
				if id := idx[ti].DecisionID; id != "" {
					held[id] = true
				}
			}
		}
		var trainSet []*indexed
		for i, e := range idx {
			if heldIdx[i] || (e.DecisionID != "" && held[e.DecisionID]) {
				continue
			}
			trainSet = append(trainSet, e)
		}
		sc := newScorer(cfg, cands, candVecs)
		train(sc, trainSet, emb.Model(), dim, cfg.Seed, false)
		sc.platt = platt
		for ti := range heldIdx {
			e := idx[ti]
			r := sc.score(e.Input, e.vec, e.DecisionID, true)
			if len(r.ranked) == 0 {
				continue
			}
			conf := sc.platt.Conf(r)
			p := pred{conf: conf, correct: r.ranked[0] == e.Label}
			p.top2 = p.correct || (len(r.ranked) > 1 && r.ranked[1] == e.Label)
			preds = append(preds, p)
			for st, d := range r.dists {
				stageN[st]++
				if argmax(d) == e.Label {
					stageHits[st]++
				}
			}
			loggedN++
			if e.Weight == wSuccess {
				logged++
			}
		}
	}
	var acc, top2, brier, accepted, acceptedHit float64
	bins := make([]CalBin, 10)
	for i := range bins {
		bins[i].Lo, bins[i].Hi = float64(i)/10, float64(i+1)/10
	}
	for _, p := range preds {
		y := 0.0
		if p.correct {
			acc++
			y = 1
		}
		if p.top2 {
			top2++
		}
		brier += (p.conf - y) * (p.conf - y)
		b := min(int(p.conf*10), 9)
		bins[b].N++
		bins[b].MeanConf += p.conf
		bins[b].Accuracy += y
		if p.conf >= cfg.Accept {
			accepted++
			acceptedHit += y
		}
	}
	n := float64(len(preds))
	if n == 0 {
		return rep, nil
	}
	for i := range bins {
		if bins[i].N > 0 {
			bins[i].MeanConf /= float64(bins[i].N)
			bins[i].Accuracy /= float64(bins[i].N)
			rep.ECE += float64(bins[i].N) / n * math.Abs(bins[i].MeanConf-bins[i].Accuracy)
		}
	}
	rep.Bins = bins
	rep.Accuracy, rep.Top2, rep.Brier = acc/n, top2/n, brier/n
	rep.AskRate = 1 - accepted/n
	if accepted > 0 {
		rep.AcceptedAccuracy = acceptedHit / accepted
	}
	for st, k := range stageN {
		rep.StageAccuracy[st] = float64(stageHits[st]) / float64(k)
	}
	if loggedN > 0 {
		rep.LoggedAccuracy = float64(logged) / float64(loggedN)
	}
	return rep, nil
}

// Print writes a human-readable report.
func (r *EvalReport) Print(w io.Writer) {
	fmt.Fprintf(w, "Router offline evaluation (embedding %s)\n", r.Model)
	fmt.Fprintf(w, "  candidates %d, training examples %d, tested %d (", r.Candidates, r.Examples, r.Tested)
	if r.Folds == r.Tested {
		fmt.Fprint(w, "leave-one-out)\n")
	} else {
		fmt.Fprintf(w, "%d-fold)\n", r.Folds)
	}
	if r.Tested == 0 {
		fmt.Fprintln(w, "  no confirmed examples yet (corrections or successful routed tasks)")
		return
	}
	fmt.Fprintf(w, "  accuracy      %.3f\n  top-2         %.3f\n", r.Accuracy, r.Top2)
	fmt.Fprintf(w, "  logged router %.3f (decisions as made at the time)\n", r.LoggedAccuracy)
	fmt.Fprintf(w, "  ECE           %.3f\n  Brier         %.3f\n", r.ECE, r.Brier)
	fmt.Fprintf(w, "  ask/LLM rate  %.3f (confidence below accept); accuracy when confident %.3f\n", r.AskRate, r.AcceptedAccuracy)
	fmt.Fprintln(w, "  per stage:")
	for _, st := range sortedKeys(r.StageAccuracy) {
		fmt.Fprintf(w, "    %-12s %.3f\n", st, r.StageAccuracy[st])
	}
	fmt.Fprintln(w, "  calibration (confidence bin: n, mean confidence → accuracy):")
	for _, b := range r.Bins {
		if b.N > 0 {
			fmt.Fprintf(w, "    %.1f–%.1f: %4d  %.2f → %.2f\n", b.Lo, b.Hi, b.N, b.MeanConf, b.Accuracy)
		}
	}
}

// CandidatesFromVault reads agents/, teams/ and workflows/ of a vault
// directory (for the offline eval, which runs without the hub).
func CandidatesFromVault(root string) ([]Candidate, error) {
	prefs := map[string][]string{}
	for k, v := range DefaultRuntimePrefs {
		prefs[k] = v
	}
	var out []Candidate
	read := func(dir string, fn func(rel string, b []byte)) error {
		ents, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, e := range ents {
			if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".md") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				return err
			}
			fn(dir+"/"+e.Name(), b)
		}
		return nil
	}
	if err := read("agents", func(rel string, b []byte) {
		if a, _ := agentdef.Parse(rel, b); a.Enabled {
			out = append(out, agentCandidate(a, string(b), prefs))
		}
	}); err != nil {
		return nil, err
	}
	if err := read("teams", func(rel string, b []byte) {
		if t, _ := teamdef.Parse(rel, b); t.Enabled {
			out = append(out, Candidate{ID: TeamPrefix + t.Id, Kind: KindTeam, Name: t.Name, Description: t.Description, Examples: t.Triggers})
		}
	}); err != nil {
		return nil, err
	}
	if err := read("workflows", func(rel string, b []byte) {
		w := workflow.Parse(rel, b)
		if w.Enabled && w.Valid() {
			c := Candidate{ID: WorkflowPrefix + w.ID, Kind: KindWorkflow, Name: w.Name, Description: w.Description}
			for _, t := range w.Triggers {
				if t.Kind == workflow.TriggerIntent {
					c.Examples = append(c.Examples, t.Phrases...)
				}
			}
			out = append(out, c)
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// LLMEmbedder returns an Embedder over kernel.LLM.Embed with a model alias.
func LLMEmbedder(llm kernel.LLM, alias string) Embedder { return &llmEmbedder{llm: llm, alias: alias} }

func newScorer(cfg Config, cands []Candidate, candVecs map[string][][]float32) *scorer {
	sc := &scorer{cfg: cfg, cands: cands, candVecs: candVecs, byID: map[string]Candidate{}, platt: DefaultPlatt()}
	for _, c := range cands {
		sc.ids = append(sc.ids, c.ID)
		sc.byID[c.ID] = c
	}
	return sc
}
