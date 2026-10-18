package learn

import (
	"encoding/json"
	"math"
)

// Classifier is an online multinomial logistic regression over the request
// embedding plus two shared, candidate-specific features:
//
//	logit_c = W_c · x + b_c + Alpha·rule_c + Beta·desc_c
//
// rule_c is the rule/keyword stage's probability for candidate c and desc_c
// the similarity of the request to c's description and examples. The shared
// weights let the model rank candidates it has never seen a label for, and
// the per-class weights learn the owner's habits. Classes are added as new
// labels appear; prediction is a softmax over the current candidates only.
type Classifier struct {
	Dim     int                  `json:"dim"`
	Model   string               `json:"model"` // embedding space
	W       map[string][]float64 `json:"w"`
	B       map[string]float64   `json:"b"`
	Alpha   float64              `json:"alpha"`
	Beta    float64              `json:"beta"`
	Steps   int                  `json:"steps"`
	Labeled int                  `json:"labeled"` // examples with weight >= 1 seen
	// LR and L2 are training hyper-parameters.
	LR float64 `json:"lr"`
	L2 float64 `json:"l2"`
}

// NewClassifier returns an untrained classifier for an embedding space.
func NewClassifier(model string, dim int) *Classifier {
	return &Classifier{Dim: dim, Model: model, W: map[string][]float64{}, B: map[string]float64{},
		Alpha: 1, Beta: 4, LR: 0.5, L2: 1e-4}
}

// Aux holds the shared per-candidate features.
type Aux struct{ Rule, Desc float64 }

func (c *Classifier) logits(x []float32, cands []string, aux map[string]Aux) []float64 {
	z := make([]float64, len(cands))
	for i, k := range cands {
		a := aux[k]
		s := c.B[k] + c.Alpha*a.Rule + c.Beta*a.Desc
		if w, ok := c.W[k]; ok && len(x) == len(w) {
			for j, xv := range x {
				s += w[j] * float64(xv)
			}
		}
		z[i] = s
	}
	return z
}

func softmax(z []float64) []float64 {
	if len(z) == 0 {
		return nil
	}
	m := z[0]
	for _, v := range z {
		m = math.Max(m, v)
	}
	p := make([]float64, len(z))
	var s float64
	for i, v := range z {
		p[i] = math.Exp(v - m)
		s += p[i]
	}
	for i := range p {
		p[i] /= s
	}
	return p
}

// Predict returns a probability per candidate.
func (c *Classifier) Predict(x []float32, cands []string, aux map[string]Aux) map[string]float64 {
	p := softmax(c.logits(x, cands, aux))
	out := make(map[string]float64, len(cands))
	for i, k := range cands {
		out[k] = p[i]
	}
	return out
}

// Update takes one SGD step. A positive example raises p(label); a negative
// (weak) example lowers p(label) (loss −w·log(1−p_label)). The label is
// added to cands when missing.
func (c *Classifier) Update(x []float32, cands []string, aux map[string]Aux, label string, weight float64, negative bool) {
	if label == "" || weight <= 0 || len(x) != c.Dim {
		return
	}
	found := false
	for _, k := range cands {
		if k == label {
			found = true
			break
		}
	}
	if !found {
		cands = append(append([]string(nil), cands...), label)
	}
	p := softmax(c.logits(x, cands, aux))
	li := -1
	for i, k := range cands {
		if k == label {
			li = i
		}
	}
	// g_k = dLoss/dz_k
	g := make([]float64, len(cands))
	if !negative {
		for i := range cands {
			g[i] = p[i]
		}
		g[li] -= 1
	} else {
		pl := math.Min(p[li], 1-1e-6)
		for i := range cands {
			if i == li {
				g[i] = pl
			} else {
				g[i] = -p[i] * pl / (1 - pl)
			}
		}
	}
	c.Steps++
	lr := c.LR * weight / math.Sqrt(1+float64(c.Steps)/100)
	var gA, gB float64
	for i, k := range cands {
		if g[i] == 0 {
			continue
		}
		w, ok := c.W[k]
		if !ok {
			w = make([]float64, c.Dim)
			c.W[k] = w
		}
		for j, xv := range x {
			w[j] -= lr * (g[i]*float64(xv) + c.L2*w[j])
		}
		c.B[k] -= lr * g[i]
		a := aux[k]
		gA += g[i] * a.Rule
		gB += g[i] * a.Desc
	}
	// The shared weights move slower: they encode priors that should hold
	// for every candidate.
	c.Alpha -= 0.2 * lr * gA
	c.Beta -= 0.2 * lr * gB
	if !negative && weight >= 1 {
		c.Labeled++
	}
}

// Marshal / Unmarshal persist the weights.
func (c *Classifier) Marshal() ([]byte, error) { return json.Marshal(c) }

// UnmarshalClassifier restores weights saved with Marshal.
func UnmarshalClassifier(b []byte) (*Classifier, error) {
	c := &Classifier{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.W == nil {
		c.W = map[string][]float64{}
	}
	if c.B == nil {
		c.B = map[string]float64{}
	}
	return c, nil
}

// ---------------------------------------------------------------- calibration

// Platt maps blended evidence to a calibrated probability that the top
// candidate is right: σ(A·top + B·margin + C).
type Platt struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
	C float64 `json:"c"`
	N int     `json:"n"` // pairs fitted (0 = hand-tuned defaults)
}

// DefaultPlatt is the hand-tuned mapping used until enough outcomes exist.
func DefaultPlatt() Platt { return Platt{A: 5, B: 3, C: -3} }

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// Apply returns the calibrated confidence.
func (p Platt) Apply(top, margin float64) float64 { return sigmoid(p.A*top + p.B*margin + p.C) }

// CalPair is one observation for calibration.
type CalPair struct {
	Top, Margin float64
	Correct     bool
}

// FitPlatt fits the mapping by gradient descent on log loss, starting from
// the defaults and shrunk towards them (a weak prior) so a few pairs cannot
// produce extreme parameters. Fewer than min pairs keep the defaults.
func FitPlatt(pairs []CalPair, minPairs int) Platt {
	p := DefaultPlatt()
	if len(pairs) < minPairs {
		return p
	}
	d := DefaultPlatt()
	const lr, prior = 0.5, 0.01
	for it := 0; it < 300; it++ {
		var ga, gb, gc float64
		for _, q := range pairs {
			y := 0.0
			if q.Correct {
				y = 1
			}
			e := p.Apply(q.Top, q.Margin) - y
			ga += e * q.Top
			gb += e * q.Margin
			gc += e
		}
		n := float64(len(pairs))
		p.A -= lr * (ga/n + prior*(p.A-d.A))
		p.B -= lr * (gb/n + prior*(p.B-d.B))
		p.C -= lr * (gc/n + prior*(p.C-d.C))
	}
	p.N = len(pairs)
	return p
}
