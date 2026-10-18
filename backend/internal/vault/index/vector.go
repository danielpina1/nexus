package index

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"strings"
)

// Embedder turns texts into vectors. The vault module adapts kernel.LLM.Embed
// (model alias "embed") to this interface.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// VectorHit is a semantic match. Score is cosine similarity in [0,1].
type VectorHit struct {
	Path  string
	Text  string
	Score float64
}

// VectorSearcher is the extension point for semantic retrieval. Search fuses
// its results with full-text hits. Swap in sqlite-vec or an external store by
// implementing this interface and calling SetVectorSearcher.
type VectorSearcher interface {
	SearchVectors(ctx context.Context, query string, limit int) ([]VectorHit, error)
}

// SetVectorSearcher enables (or with nil disables) vector fusion in Search.
func (ix *Index) SetVectorSearcher(v VectorSearcher) {
	ix.vmu.Lock()
	ix.vec = v
	ix.vmu.Unlock()
}

// Vectors is a simple brute-force vector store in the index database:
// notes are chunked by paragraphs (~ChunkChars), embedded, and stored as
// float32 blobs; queries compute cosine similarity over all chunks. Good for
// personal vaults (thousands of notes); replace with sqlite-vec when needed.
type Vectors struct {
	ix         *Index
	emb        Embedder
	ChunkChars int
	BatchSize  int
}

// NewVectors creates a vector store over ix using emb.
func NewVectors(ix *Index, emb Embedder) *Vectors {
	return &Vectors{ix: ix, emb: emb, ChunkChars: 1200, BatchSize: 16}
}

// Refresh embeds every note whose content hash differs from its stored
// vectors. It returns the number of notes embedded.
func (v *Vectors) Refresh(ctx context.Context) (int, error) {
	rows, err := v.ix.db.QueryContext(ctx, `SELECT n.path, n.hash, n.title, f.body FROM notes n JOIN notes_fts f ON f.rowid = n.rowid
		WHERE NOT EXISTS (SELECT 1 FROM vectors v WHERE v.path = n.path AND v.hash = n.hash)`)
	if err != nil {
		return 0, err
	}
	type todo struct{ path, hash, title, body string }
	var work []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.path, &t.hash, &t.title, &t.body); err != nil {
			rows.Close()
			return 0, err
		}
		work = append(work, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, t := range work {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		chunks := Chunk(t.title+"\n\n"+t.body, v.ChunkChars)
		var vecs [][]float32
		for i := 0; i < len(chunks); i += v.BatchSize {
			j := min(i+v.BatchSize, len(chunks))
			out, err := v.emb.Embed(ctx, chunks[i:j])
			if err != nil {
				return done, err
			}
			vecs = append(vecs, out...)
		}
		if len(vecs) != len(chunks) {
			continue
		}
		v.ix.wmu.Lock()
		tx, err := v.ix.db.BeginTx(ctx, nil)
		if err != nil {
			v.ix.wmu.Unlock()
			return done, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM vectors WHERE path = ?`, t.path)
		for i := 0; err == nil && i < len(chunks); i++ {
			_, err = tx.ExecContext(ctx, `INSERT INTO vectors(path, chunk, hash, text, vec) VALUES(?,?,?,?,?)`,
				t.path, i, t.hash, chunks[i], encodeVec(vecs[i]))
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		v.ix.wmu.Unlock()
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// SearchVectors embeds the query and returns the best chunk per note.
func (v *Vectors) SearchVectors(ctx context.Context, query string, limit int) ([]VectorHit, error) {
	qv, err := v.emb.Embed(ctx, []string{query})
	if err != nil || len(qv) == 0 {
		return nil, err
	}
	rows, err := v.ix.db.QueryContext(ctx, `SELECT path, text, vec FROM vectors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	best := map[string]VectorHit{}
	for rows.Next() {
		var h VectorHit
		var blob []byte
		if err := rows.Scan(&h.Path, &h.Text, &blob); err != nil {
			return nil, err
		}
		h.Score = math.Max(0, cosine(qv[0], decodeVec(blob)))
		if b, ok := best[h.Path]; !ok || h.Score > b.Score {
			best[h.Path] = h
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]VectorHit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Chunk splits text on blank lines into chunks of at most ~max characters.
func Chunk(text string, max int) []string {
	if max <= 0 {
		max = 1200
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(text, "\n\n") {
		for len(para) > max {
			flush()
			cut := strings.LastIndexAny(para[:max], " \n")
			if cut <= 0 {
				cut = max
			}
			out = append(out, strings.TrimSpace(para[:cut]))
			para = para[cut:]
		}
		if cur.Len()+len(para) > max {
			flush()
		}
		cur.WriteString(para)
		cur.WriteString("\n\n")
	}
	flush()
	if len(out) == 0 {
		out = []string{strings.TrimSpace(text)}
	}
	return out
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
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

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// SimilarNotes compares a note's mean chunk vector with every other note's
// (cosine similarity, best first). It needs no embedder: it only uses the
// vectors already stored. Notes without vectors return nothing.
func (ix *Index) SimilarNotes(ctx context.Context, rel string, limit int) ([]VectorHit, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, vec FROM vectors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sums := map[string][]float64{}
	counts := map[string]int{}
	for rows.Next() {
		var p string
		var blob []byte
		if err := rows.Scan(&p, &blob); err != nil {
			return nil, err
		}
		vec := decodeVec(blob)
		s := sums[p]
		if s == nil {
			s = make([]float64, len(vec))
		}
		if len(s) != len(vec) {
			continue
		}
		for i, f := range vec {
			s[i] += float64(f)
		}
		sums[p] = s
		counts[p]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	me, ok := sums[rel]
	if !ok {
		return nil, nil
	}
	mean := func(s []float64, n int) []float32 {
		out := make([]float32, len(s))
		for i := range s {
			out[i] = float32(s[i] / float64(n))
		}
		return out
	}
	q := mean(me, counts[rel])
	var out []VectorHit
	for p, s := range sums {
		if p == rel {
			continue
		}
		out = append(out, VectorHit{Path: p, Score: math.Max(0, cosine(q, mean(s, counts[p])))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
