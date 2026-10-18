package learn

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Stage names used in router_learn_scores.stages and in blends.
const (
	StageRules      = "rules"
	StageDesc       = "description"
	StageKNN        = "knn"
	StageClassifier = "classifier"
	StageLLM        = "llm"
)

// indexed is an example with its embedding.
type indexed struct {
	Example
	vec []float32
	// aux caches the candidate features used for training steps (they only
	// depend on the text and the candidate set, fixed during a training).
	aux map[string]Aux
}

// neighbor is one kNN hit.
type neighbor struct {
	ex  *indexed
	sim float64
}

// scorer computes per-stage candidate distributions. It is immutable
// while in use (the learner swaps whole scorers on rebuild).
type scorer struct {
	cfg      Config
	cands    []Candidate
	ids      []string
	byID     map[string]Candidate
	candVecs map[string][][]float32
	examples []*indexed
	clf      *Classifier
	labeled  int
	platt    Platt
}

// stageResult holds every stage's distribution for one request.
type stageResult struct {
	dists     map[string]map[string]float64 // stage → candidate → p
	weights   map[string]float64            // stage → blend weight
	descSim   map[string]float64
	kw        map[string]float64
	neighbors []neighbor
	blended   map[string]float64
	ranked    []string
	top       float64
	margin    float64
	// evidence is the best absolute similarity (description/example or a
	// past request): low for requests unlike anything known.
	evidence float64
}

// Conf is the calibrated confidence of a stage result.
func (p Platt) Conf(r *stageResult) float64 { return p.Apply(r.top, r.margin, r.evidence) }

const (
	descTemp   = 10.0
	knnMinSim  = 0.1
	knnEpsilon = 0.02
)

// score runs the rules, description, kNN and classifier stages. exclude
// skips examples of one decision (leave-one-out evaluation).
func (s *scorer) score(input string, q []float32, exclude string, useClf bool) *stageResult {
	r := &stageResult{dists: map[string]map[string]float64{}, weights: map[string]float64{},
		descSim: map[string]float64{}}
	if len(s.ids) == 0 {
		return r
	}
	// Rules: keyword overlap with ids, names, descriptions and examples.
	r.kw = keywordScores(input, s.cands)
	var kwMax, kwSum float64
	for _, v := range r.kw {
		kwMax = math.Max(kwMax, v)
		kwSum += v
	}
	if kwMax > 0 {
		d := map[string]float64{}
		for _, id := range s.ids {
			d[id] = (r.kw[id] + 0.01) / (kwSum + 0.01*float64(len(s.ids)))
		}
		r.dists[StageRules], r.weights[StageRules] = d, math.Min(1, 1.5*kwMax)
	}
	// Description / examples similarity.
	z := make([]float64, len(s.ids))
	for i, id := range s.ids {
		best := -1.0
		for _, v := range s.candVecs[id] {
			best = math.Max(best, dot(q, v))
		}
		if best < -0.5 {
			best = 0
		}
		r.descSim[id] = best
		z[i] = descTemp * best
	}
	p := softmax(z)
	d := map[string]float64{}
	for i, id := range s.ids {
		d[id] = p[i]
	}
	r.dists[StageDesc], r.weights[StageDesc] = d, 1
	for _, v := range r.descSim {
		r.evidence = math.Max(r.evidence, v)
	}
	// kNN over past requests.
	var hits []neighbor
	for _, e := range s.examples {
		if exclude != "" && e.DecisionID == exclude {
			continue
		}
		if _, ok := s.byID[e.Label]; !ok {
			continue
		}
		if sim := dot(q, e.vec); sim >= knnMinSim {
			hits = append(hits, neighbor{ex: e, sim: sim})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].sim > hits[j].sim })
	if len(hits) > s.cfg.K {
		hits = hits[:s.cfg.K]
	}
	r.neighbors = hits
	for _, h := range hits {
		if !h.ex.Negative {
			r.evidence = math.Max(r.evidence, h.sim)
		}
	}
	votes := map[string]float64{}
	var strength float64
	for _, h := range hits {
		w := h.ex.Weight * h.sim * h.sim
		if h.ex.Negative {
			votes[h.ex.Label] -= w
			continue
		}
		votes[h.ex.Label] += w
		strength += h.ex.Weight * h.sim
	}
	if strength > 0 {
		var total float64
		for _, id := range s.ids {
			total += math.Max(0, votes[id])
		}
		d := map[string]float64{}
		n := float64(len(s.ids))
		for _, id := range s.ids {
			d[id] = (math.Max(0, votes[id]) + knnEpsilon) / (total + knnEpsilon*n)
		}
		r.dists[StageKNN], r.weights[StageKNN] = d, 1.5*math.Min(1, strength/1.5)
	}
	// Classifier.
	if useClf && s.clf != nil && s.labeled > 0 {
		r.dists[StageClassifier] = s.clf.Predict(q, s.ids, s.aux(r))
		r.weights[StageClassifier] = 2 * math.Min(1, float64(s.labeled)/float64(s.cfg.MinExamples))
	}
	r.blend(s.ids)
	return r
}

// auxOf returns (and caches) the shared classifier features of an example.
func (s *scorer) auxOf(e *indexed) map[string]Aux {
	if e.aux != nil {
		return e.aux
	}
	kw := keywordScores(e.Input, s.cands)
	a := make(map[string]Aux, len(s.ids))
	for _, id := range s.ids {
		best := -1.0
		for _, v := range s.candVecs[id] {
			best = math.Max(best, dot(e.vec, v))
		}
		if best < -0.5 {
			best = 0
		}
		a[id] = Aux{Rule: kw[id], Desc: best}
	}
	e.aux = a
	return a
}

func (s *scorer) aux(r *stageResult) map[string]Aux {
	a := make(map[string]Aux, len(s.ids))
	for _, id := range s.ids {
		a[id] = Aux{Rule: r.kw[id], Desc: r.descSim[id]}
	}
	return a
}

// addRoute adds a stage that is a single route (LLM): conf on its agent,
// the rest spread evenly.
func (r *stageResult) addRoute(stage string, ids []string, agent string, conf float64, weight float64) {
	if agent == "" || weight <= 0 {
		return
	}
	known := false
	for _, id := range ids {
		if id == agent {
			known = true
		}
	}
	if !known {
		return
	}
	conf = math.Max(0, math.Min(1, conf))
	d := map[string]float64{}
	rest := (1 - conf) / float64(len(ids))
	for _, id := range ids {
		d[id] = rest
	}
	d[agent] += conf
	r.dists[stage], r.weights[stage] = d, weight
	r.blend(ids)
}

// blend combines the stage distributions with their weights.
func (r *stageResult) blend(ids []string) {
	r.blended = map[string]float64{}
	var wsum float64
	for st, w := range r.weights {
		if w <= 0 {
			continue
		}
		wsum += w
		for id, p := range r.dists[st] {
			r.blended[id] += w * p
		}
	}
	if wsum > 0 {
		for id := range r.blended {
			r.blended[id] /= wsum
		}
	}
	r.ranked = append([]string(nil), ids...)
	sort.SliceStable(r.ranked, func(i, j int) bool { return r.blended[r.ranked[i]] > r.blended[r.ranked[j]] })
	r.top, r.margin = 0, 0
	if len(r.ranked) > 0 {
		r.top = r.blended[r.ranked[0]]
		r.margin = r.top
		if len(r.ranked) > 1 {
			r.margin = r.top - r.blended[r.ranked[1]]
		}
	}
}

func argmax(d map[string]float64) string {
	best, bi := math.Inf(-1), ""
	for _, k := range sortedKeys(d) {
		if d[k] > best {
			best, bi = d[k], k
		}
	}
	return bi
}

// retrievalTop is the best candidate by description + kNN only.
func (r *stageResult) retrievalTop() string {
	c := map[string]float64{}
	for _, st := range []string{StageDesc, StageKNN} {
		for id, p := range r.dists[st] {
			c[id] += r.weights[st] * p
		}
	}
	return argmax(c)
}

// disagree reports whether a trained classifier and retrieval pick
// different candidates.
func (r *stageResult) disagree() bool {
	clf, ok := r.dists[StageClassifier]
	if !ok || r.weights[StageClassifier] < 1 {
		return false
	}
	return argmax(clf) != r.retrievalTop()
}

// ---------------------------------------------------------------- keywords

var tokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

var kwStop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"use": true, "you": true, "your": true, "please": true, "can": true, "into": true, "about": true,
	"are": true, "was": true, "will": true, "all": true, "any": true, "its": true, "our": true,
	"agent": true, "when": true, "what": true, "make": true, "get": true, "has": true, "have": true,
}

func kwTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		if len([]rune(w)) >= 3 && !kwStop[w] {
			out[stem(w)] = true
		}
	}
	return out
}

// keywordScores mirrors the router's keyword stage (id/name words 0.3,
// description and example words 0.15, capped at 0.85).
func keywordScores(input string, cands []Candidate) map[string]float64 {
	in := kwTokens(input)
	out := map[string]float64{}
	for _, c := range cands {
		name := kwTokens(strings.NewReplacer("-", " ", "_", " ", ":", " ").Replace(c.ID) + " " + c.Name)
		desc := kwTokens(c.Description + " " + strings.Join(c.Examples, " "))
		var s float64
		for w := range in {
			switch {
			case name[w]:
				s += 0.3
			case desc[w]:
				s += 0.15
			}
		}
		if s > 0 {
			out[c.ID] = math.Min(s, 0.85)
		}
	}
	return out
}

// ---------------------------------------------------------------- explain

// explain renders the evidence for the chosen candidate, e.g.
// "matched 3 similar past requests (bought groceries ×2, compared prices) + classifier 0.82".
func (r *stageResult) explain(chosen string) string {
	var parts []string
	type grp struct {
		text string
		n    int
	}
	var groups []*grp
	idx := map[string]*grp{}
	n := 0
	for _, h := range r.neighbors {
		if h.ex.Label != chosen || h.ex.Negative {
			continue
		}
		n++
		t := shortText(h.ex.Input, 5)
		if g, ok := idx[t]; ok {
			g.n++
			continue
		}
		g := &grp{text: t, n: 1}
		idx[t] = g
		groups = append(groups, g)
	}
	if n > 0 {
		var ss []string
		for i, g := range groups {
			if i == 3 {
				ss = append(ss, "…")
				break
			}
			if g.n > 1 {
				ss = append(ss, fmt.Sprintf("%s ×%d", g.text, g.n))
			} else {
				ss = append(ss, g.text)
			}
		}
		noun := "requests"
		if n == 1 {
			noun = "request"
		}
		parts = append(parts, fmt.Sprintf("matched %d similar past %s (%s)", n, noun, strings.Join(ss, ", ")))
	}
	if d, ok := r.dists[StageDesc]; ok && n == 0 {
		parts = append(parts, fmt.Sprintf("description match %.2f", r.descSim[chosen]))
		_ = d
	}
	if kw := r.kw[chosen]; kw > 0 && r.weights[StageRules] > 0 {
		parts = append(parts, fmt.Sprintf("keywords %.2f", kw))
	}
	if d, ok := r.dists[StageClassifier]; ok {
		parts = append(parts, fmt.Sprintf("classifier %.2f", d[chosen]))
	}
	if d, ok := r.dists[StageLLM]; ok {
		parts = append(parts, fmt.Sprintf("LLM %.2f", d[chosen]))
	}
	return strings.Join(parts, " + ")
}

func shortText(s string, words int) string {
	f := strings.Fields(strings.ToLower(s))
	if len(f) > words {
		f = append(f[:words], "…")
	}
	return strings.Join(f, " ")
}
