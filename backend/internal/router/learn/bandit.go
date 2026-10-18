package learn

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
)

// Bandit chooses a runtime (claude-code / codex / ollama …) for an agent per
// request cluster with Thompson sampling over Beta posteriors of a reward in
// [0, 1] (success minus cost, latency and correction penalties). Every
// update also feeds a global (cluster −1) posterior used while a cluster has
// fewer than minClusterN observations. Arms keep a small prior bonus in the
// order of the policy list so the policy's preference holds without data.
type Bandit struct {
	mu    sync.Mutex
	stats map[string]*ArmStats
	rng   *rand.Rand
}

// ArmStats is a Beta(A, B) posterior (prior 1, 1) with an observation count.
type ArmStats struct {
	A, B float64
	N    int
}

const minClusterN = 3

// NewBandit returns an empty bandit; seed makes sampling reproducible.
func NewBandit(seed uint64) *Bandit {
	return &Bandit{stats: map[string]*ArmStats{}, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func armKey(cluster int, agent, arm string) string { return fmt.Sprintf("%d|%s|%s", cluster, agent, arm) }

func (b *Bandit) get(cluster int, agent, arm string) *ArmStats {
	k := armKey(cluster, agent, arm)
	s, ok := b.stats[k]
	if !ok {
		s = &ArmStats{A: 1, B: 1}
		b.stats[k] = s
	}
	return s
}

// Update records a reward in [0, 1] for (cluster, agent, arm).
func (b *Bandit) Update(cluster int, agent, arm string, reward float64) {
	if arm == "" || agent == "" {
		return
	}
	reward = math.Max(0, math.Min(1, reward))
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range uniqueInts(cluster, -1) {
		s := b.get(c, agent, arm)
		s.A += reward
		s.B += 1 - reward
		s.N++
	}
}

func uniqueInts(a, b int) []int {
	if a == b {
		return []int{a}
	}
	return []int{a, b}
}

// ArmChoice is the bandit's pick with the sampled values.
type ArmChoice struct {
	Arm     string
	Samples map[string]float64
	Means   map[string]float64
	N       int // observations behind the chosen arm's posterior
	Pooled  bool
}

// Choose samples every arm and returns the best. arms is the policy order.
func (b *Bandit) Choose(cluster int, agent string, arms []string) ArmChoice {
	out := ArmChoice{Samples: map[string]float64{}, Means: map[string]float64{}}
	if len(arms) == 0 {
		return out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	best := math.Inf(-1)
	for i, arm := range arms {
		s := b.get(cluster, agent, arm)
		pooled := false
		if s.N < minClusterN {
			s = b.get(-1, agent, arm)
			pooled = true
		}
		bonus := 0.5 * float64(len(arms)-i) / float64(len(arms))
		a, bb := s.A+bonus, s.B
		v := betaSample(b.rng, a, bb)
		out.Samples[arm] = v
		out.Means[arm] = a / (a + bb)
		if v > best {
			best, out.Arm, out.N, out.Pooled = v, arm, s.N, pooled
		}
	}
	return out
}

// Snapshot returns the posteriors (for tests and the eval report).
func (b *Bandit) Snapshot() map[string]ArmStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]ArmStats, len(b.stats))
	for k, v := range b.stats {
		out[k] = *v
	}
	return out
}

// betaSample draws from Beta(a, b) via two Gamma draws.
func betaSample(r *rand.Rand, a, b float64) float64 {
	x := gammaSample(r, a)
	y := gammaSample(r, b)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// gammaSample draws from Gamma(k, 1) (Marsaglia & Tsang).
func gammaSample(r *rand.Rand, k float64) float64 {
	if k < 1 {
		u := r.Float64()
		return gammaSample(r, k+1) * math.Pow(u, 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// ---------------------------------------------------------------- clusters

// KMeans clusters normalised vectors (spherical k-means, k-means++ seeding)
// and returns the centroids.
func KMeans(vecs [][]float32, k int, seed uint64) [][]float32 {
	if len(vecs) == 0 || k <= 0 {
		return nil
	}
	if k > len(vecs) {
		k = len(vecs)
	}
	r := rand.New(rand.NewPCG(seed, 7))
	cents := [][]float32{vecs[r.IntN(len(vecs))]}
	for len(cents) < k {
		d := make([]float64, len(vecs))
		var sum float64
		for i, v := range vecs {
			best := math.Inf(1)
			for _, c := range cents {
				best = math.Min(best, 1-dot(v, c))
			}
			d[i] = best * best
			sum += d[i]
		}
		if sum == 0 {
			break
		}
		t := r.Float64() * sum
		idx := len(vecs) - 1
		for i, x := range d {
			t -= x
			if t <= 0 {
				idx = i
				break
			}
		}
		cents = append(cents, vecs[idx])
	}
	dim := len(vecs[0])
	for it := 0; it < 15; it++ {
		sums := make([][]float64, len(cents))
		for i := range sums {
			sums[i] = make([]float64, dim)
		}
		for _, v := range vecs {
			c := Nearest(cents, v)
			for j, x := range v {
				sums[c][j] += float64(x)
			}
		}
		for i, s := range sums {
			nc := make([]float32, dim)
			for j, x := range s {
				nc[j] = float32(x)
			}
			if isZero(nc) {
				continue
			}
			cents[i] = normalize(nc)
		}
	}
	return cents
}

func isZero(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

// Nearest returns the index of the most similar centroid (0 without any).
func Nearest(cents [][]float32, v []float32) int {
	best, bi := math.Inf(-1), 0
	for i, c := range cents {
		if s := dot(v, c); s > best {
			best, bi = s, i
		}
	}
	return bi
}

// sortedKeys returns map keys sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
