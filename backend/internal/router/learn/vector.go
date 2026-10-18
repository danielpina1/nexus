package learn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Embedder turns texts into L2-normalised vectors of one space. Vectors of
// different Model() values are never compared.
type Embedder interface {
	Model() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// llmEmbedder uses kernel.LLM.Embed with a model alias (default "embed",
// the local Ollama embedding model of policies/models.md).
type llmEmbedder struct {
	llm   kernel.LLM
	alias string
}

func (e *llmEmbedder) Model() string { return "llm:" + e.alias }

// ErrEmbedUnavailable is returned when the embedding provider is degraded
// or cooling down (kernel.LLMAvailability); the learner then falls back to
// the hashing embedder at once instead of waiting for the call to fail.
var ErrEmbedUnavailable = errors.New("learn: embedding provider degraded or cooling down")

func (e *llmEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !kernel.LLMAvailable(e.llm, e.alias) {
		return nil, fmt.Errorf("%s: %w", e.alias, ErrEmbedUnavailable)
	}
	vecs, err := e.llm.Embed(ctx, e.alias, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("learn: embed returned %d vectors for %d inputs", len(vecs), len(texts))
	}
	for i := range vecs {
		vecs[i] = normalize(vecs[i])
	}
	return vecs, nil
}

// HashEmbedder is a deterministic, dependency-free embedding (feature
// hashing of words, word bigrams and character trigrams). It is the
// fallback when no embedding model is reachable, and what tests use.
type HashEmbedder struct{ Dim int }

// Model implements Embedder.
func (h HashEmbedder) Model() string { return fmt.Sprintf("hash-v1-%d", h.dim()) }

func (h HashEmbedder) dim() int {
	if h.Dim <= 0 {
		return 512
	}
	return h.Dim
}

// Embed implements Embedder.
func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = h.vector(t)
	}
	return out, nil
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

func (h HashEmbedder) vector(text string) []float32 {
	d := h.dim()
	v := make([]float32, d)
	add := func(feat string, w float32) {
		f := fnv.New64a()
		_, _ = f.Write([]byte(feat))
		s := f.Sum64()
		idx := int(s % uint64(d))
		if (s>>63)&1 == 1 {
			w = -w
		}
		v[idx] += w
	}
	words := wordRe.FindAllString(strings.ToLower(text), -1)
	var kept []string
	for _, w := range words {
		if len([]rune(w)) < 2 || stopwords[w] {
			continue
		}
		kept = append(kept, w)
	}
	for i, w := range kept {
		add("w:"+stem(w), 1)
		if i > 0 {
			add("b:"+stem(kept[i-1])+"_"+stem(w), 0.5)
		}
		r := []rune("^" + w + "$")
		for j := 0; j+3 <= len(r); j++ {
			add("c:"+string(r[j:j+3]), 0.25)
		}
	}
	return normalize(v)
}

// stem is a tiny suffix stripper so "buying"/"buy"/"buys" share a feature.
func stem(w string) string {
	for _, suf := range []string{"ing", "ies", "es", "ed", "s"} {
		if len(w) > len(suf)+2 && strings.HasSuffix(w, suf) {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"you": true, "your": true, "please": true, "can": true, "into": true, "about": true, "to": true,
	"are": true, "was": true, "will": true, "all": true, "any": true, "its": true, "our": true,
	"of": true, "in": true, "on": true, "me": true, "my": true, "it": true, "is": true, "an": true,
	"de": true, "da": true, "do": true, "a": true, "o": true, "e": true, "um": true, "uma": true,
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(s))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

// dot is the cosine similarity of two normalised vectors (0 when the
// dimensions differ).
func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// ---------------------------------------------------------------- cache

// vectorCache memoises vectors in router_learn_vectors, keyed by
// sha256(model, text), so restarts and evals do not re-embed.
type vectorCache struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string][]float32
}

func newVectorCache(db *sql.DB) *vectorCache {
	return &vectorCache{db: db, mem: map[string][]float32{}}
}

func contentHash(model, text string) string {
	h := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(h[:])
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// Embed returns vectors for texts, embedding only the ones not cached.
// Hash embeddings are cheap and only kept in memory.
func (c *vectorCache) Embed(ctx context.Context, e Embedder, texts []string) ([][]float32, error) {
	model := e.Model()
	_, persistent := e.(*llmEmbedder)
	out := make([][]float32, len(texts))
	var missIdx []int
	var missTxt []string
	c.mu.Lock()
	for i, t := range texts {
		k := contentHash(model, t)
		if v, ok := c.mem[k]; ok {
			out[i] = v
			continue
		}
		if persistent && c.db != nil {
			var b []byte
			if err := c.db.QueryRowContext(ctx, `SELECT vec FROM router_learn_vectors WHERE hash = ?`, k).Scan(&b); err == nil {
				v := decodeVec(b)
				c.mem[k] = v
				out[i] = v
				continue
			}
		}
		missIdx = append(missIdx, i)
		missTxt = append(missTxt, t)
	}
	c.mu.Unlock()
	if len(missTxt) == 0 {
		return out, nil
	}
	const batch = 64
	for s := 0; s < len(missTxt); s += batch {
		end := min(s+batch, len(missTxt))
		vecs, err := e.Embed(ctx, missTxt[s:end])
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		for j, v := range vecs {
			i := missIdx[s+j]
			out[i] = v
			k := contentHash(model, missTxt[s+j])
			c.mem[k] = v
			if persistent && c.db != nil {
				_, _ = c.db.ExecContext(ctx, `INSERT OR REPLACE INTO router_learn_vectors (hash, model, dim, vec, created_at) VALUES (?, ?, ?, ?, ?)`,
					k, model, len(v), encodeVec(v), time.Now().UnixMilli())
			}
		}
		c.mu.Unlock()
	}
	return out, nil
}
