package index

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ErrNotFound is returned for unknown notes.
var ErrNotFound = errors.New("index: note not found")

// Query is a search request.
type Query struct {
	Text  string
	Types []string
	Limit int
	// Since filters out notes last updated before this time (zero = no filter).
	Since time.Time
}

// Hit is a search result.
type Hit struct {
	Entry   *Entry
	Snippet string
	Score   float64
}

// Snippet highlight markers (Markdown bold).
const (
	HighlightStart = "**"
	HighlightEnd   = "**"
)

// Search runs a full-text query (FTS5 bm25 over title, aliases, body and
// tags, weighted 10/6/1/4), then applies boosts: exact title ×2, title
// contains the query ×1.5, all terms in title ×1.25, updated in the last
// 7 days ×1.2 (30 days ×1.1). All terms must match (prefix match on the last
// term); if that finds nothing, any term may match. When a VectorSearcher is
// configured, its hits are fused in with reciprocal rank fusion (see fuse).
// An empty query returns the most recently updated notes.
func (ix *Index) Search(ctx context.Context, q Query) ([]Hit, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	terms := tokenize(q.Text)
	if len(terms) == 0 {
		return ix.recent(ctx, q, limit)
	}
	fetch := limit * 3
	if fetch < 30 {
		fetch = 30
	}
	hits, err := ix.fts(ctx, matchExpr(terms, " "), q, fetch)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 && len(terms) > 1 {
		if hits, err = ix.fts(ctx, matchExpr(terms, " OR "), q, fetch); err != nil {
			return nil, err
		}
	}
	// Normalise text scores to [0,1].
	maxScore := 0.0
	for _, h := range hits {
		maxScore = math.Max(maxScore, h.Score)
	}
	for i := range hits {
		if maxScore > 0 {
			hits[i].Score /= maxScore
		}
	}
	ix.vmu.RLock()
	vec := ix.vec
	ix.vmu.RUnlock()
	if vec != nil {
		hits = ix.fuse(ctx, hits, vec, q, fetch)
	}
	now := time.Now()
	ql := strings.ToLower(strings.TrimSpace(q.Text))
	for i := range hits {
		h := &hits[i]
		title := strings.ToLower(h.Entry.Title)
		switch {
		case title == ql:
			h.Score *= 2
		case strings.Contains(title, ql):
			h.Score *= 1.5
		case containsAll(title, terms):
			h.Score *= 1.25
		}
		switch age := now.Sub(h.Entry.Updated); {
		case age < 7*24*time.Hour:
			h.Score *= 1.2
		case age < 30*24*time.Hour:
			h.Score *= 1.1
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (ix *Index) fts(ctx context.Context, match string, q Query, fetch int) ([]Hit, error) {
	sqlq := `SELECT ` + entryColsN + `,
		bm25(notes_fts, 10.0, 6.0, 1.0, 4.0), snippet(notes_fts, 2, ?, ?, '…', 16)
		FROM notes_fts JOIN notes n ON n.rowid = notes_fts.rowid
		WHERE notes_fts MATCH ?`
	args := []any{HighlightStart, HighlightEnd, match}
	sqlq, args = filters(sqlq, args, q)
	sqlq += ` ORDER BY 12 LIMIT ?`
	args = append(args, fetch)
	rows, err := ix.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var rank float64
		e, err := scanEntry(rows, &rank, &h.Snippet)
		if err != nil {
			return nil, err
		}
		h.Entry = e
		h.Score = -rank // bm25 is lower-is-better
		if h.Score <= 0 {
			h.Score = 1e-6
		}
		h.Snippet = cleanSnippet(h.Snippet, e.Title)
		out = append(out, h)
	}
	return out, rows.Err()
}

func filters(sqlq string, args []any, q Query) (string, []any) {
	if len(q.Types) > 0 {
		ph := make([]string, len(q.Types))
		for i, t := range q.Types {
			ph[i] = "?"
			args = append(args, t)
		}
		sqlq += ` AND n.type IN (` + strings.Join(ph, ",") + `)`
	}
	if !q.Since.IsZero() {
		sqlq += ` AND n.updated >= ?`
		args = append(args, q.Since.UnixMilli())
	}
	return sqlq, args
}

func (ix *Index) recent(ctx context.Context, q Query, limit int) ([]Hit, error) {
	sqlq := `SELECT ` + entryColsN + `, substr(f.body, 1, 200)
		FROM notes n JOIN notes_fts f ON f.rowid = n.rowid WHERE 1=1`
	sqlq, args := filters(sqlq, nil, q)
	sqlq += ` ORDER BY n.updated DESC, n.path LIMIT ?`
	args = append(args, limit)
	rows, err := ix.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var body string
		e, err := scanEntry(rows, &body)
		if err != nil {
			return nil, err
		}
		out = append(out, Hit{Entry: e, Snippet: cleanSnippet(body, e.Title), Score: 0})
	}
	return out, rows.Err()
}

// rrfK is the reciprocal-rank-fusion constant (Cormack et al. use 60).
const rrfK = 60.0

// fuse merges full-text and vector hits with reciprocal rank fusion:
// score = Σ 1/(rrfK + rank) over the lists a note appears in, normalised to
// [0,1]. Weak vector matches (more than 0.25 below the best one) are dropped
// so semantic search adds candidates without flooding the results.
func (ix *Index) fuse(ctx context.Context, hits []Hit, vec VectorSearcher, q Query, fetch int) []Hit {
	vhits, err := vec.SearchVectors(ctx, q.Text, fetch)
	if err != nil || len(vhits) == 0 {
		return hits
	}
	best := vhits[0].Score
	cut := math.Max(0.25, best-0.25)
	byPath := map[string]int{}
	fused := make([]float64, len(hits))
	for i := range hits {
		fused[i] = 1 / (rrfK + float64(i+1))
		byPath[hits[i].Entry.Path] = i
	}
	rank := 0
	for _, v := range vhits {
		if v.Score < cut {
			break
		}
		rank++
		r := 1 / (rrfK + float64(rank))
		if i, ok := byPath[v.Path]; ok {
			fused[i] += r
			continue
		}
		e, ok := ix.Get(ctx, v.Path)
		if !ok || !typeMatch(e.Type, q.Types) || (!q.Since.IsZero() && e.Updated.Before(q.Since)) {
			continue
		}
		snippet := v.Text
		if ix.isPrivate(e.Path) {
			snippet = ""
		}
		byPath[v.Path] = len(hits)
		hits = append(hits, Hit{Entry: e, Snippet: cleanSnippet(truncateRunes(snippet, 240), e.Title)})
		fused = append(fused, r)
	}
	maxF := 0.0
	for _, f := range fused {
		maxF = math.Max(maxF, f)
	}
	for i := range hits {
		hits[i].Score = fused[i] / maxF
	}
	return hits
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func typeMatch(t string, types []string) bool {
	if len(types) == 0 {
		return true
	}
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

func fill(e *Entry, aliases, tags string, updated int64) {
	_ = json.Unmarshal([]byte(aliases), &e.Aliases)
	_ = json.Unmarshal([]byte(tags), &e.Tags)
	e.Updated = time.UnixMilli(updated)
}

func cleanSnippet(s, title string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return title
	}
	return s
}

// tokenize splits a query into FTS-safe terms (letters/digits only).
func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func matchExpr(terms []string, sep string) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + t + `"`
		if i == len(terms)-1 && len([]rune(t)) >= 2 {
			parts[i] += "*"
		}
	}
	return strings.Join(parts, sep)
}

func containsAll(s string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(s, strings.ToLower(t)) {
			return false
		}
	}
	return true
}
