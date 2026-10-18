package vault

import (
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
)

// BacklinkInfo is a link (or unlinked mention) with its context line.
type BacklinkInfo struct {
	Source   *index.Entry
	Line     int
	Context  string
	Relation string
}

// Proto converts a backlink to its API form.
func (b BacklinkInfo) Proto() *nexusv1.Backlink {
	return &nexusv1.Backlink{Source: entryRef(b.Source), Line: int32(b.Line), Context: b.Context, Relation: b.Relation}
}

// lineCache reads (decrypted) note lines once.
type lineCache struct {
	v     *Vault
	ctx   context.Context
	lines map[string][]string
}

func (c *lineCache) get(rel string) []string {
	if c.lines == nil {
		c.lines = map[string][]string{}
	}
	if l, ok := c.lines[rel]; ok {
		return l
	}
	b, _, err := c.v.ReadRaw(c.ctx, rel)
	var ls []string
	if err == nil {
		ls = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	c.lines[rel] = ls
	return ls
}

func (c *lineCache) line(rel string, n int) string {
	ls := c.get(rel)
	if n <= 0 || n > len(ls) {
		return ""
	}
	return strings.TrimSpace(ls[n-1])
}

// Backlinks returns the notes linking to a note with the line of each
// link, and optionally unlinked mentions: lines in other notes that mention
// the note's title or an alias as plain text (link suggestions). Private
// (encrypted, non-searchable) notes are not scanned for mentions.
func (v *Vault) Backlinks(ctx context.Context, idOrPath string, includeUnlinked bool, limit int) ([]BacklinkInfo, []BacklinkInfo, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, nil, err
	}
	me, ok := v.ix.Get(ctx, rel)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
	}
	if limit <= 0 {
		limit = 200
	}
	edges, err := v.ix.Backlinks(ctx, rel)
	if err != nil {
		return nil, nil, err
	}
	lc := &lineCache{v: v, ctx: ctx}
	var links []BacklinkInfo
	linked := map[string]bool{rel: true}
	for _, e := range edges {
		src, ok := v.ix.Get(ctx, e.Src)
		if !ok {
			continue
		}
		linked[e.Src] = true
		ctxLine := ""
		if e.Line > 0 && !(v.crypt.covers(e.Src) && !v.crypt.searchable) {
			ctxLine = lc.line(e.Src, e.Line)
		} else if e.Line == 0 {
			ctxLine = e.Relation + ": [[" + e.Target + "]]"
		}
		links = append(links, BacklinkInfo{Source: src, Line: e.Line, Context: ctxLine, Relation: e.Relation})
		if len(links) >= limit {
			break
		}
	}
	if !includeUnlinked {
		return links, nil, nil
	}
	terms := uniqueStrings(append(append([]string{me.Title}, me.Aliases...), strings.TrimSuffix(path.Base(rel), ".md")), "")
	var mentions []BacklinkInfo
	seen := map[string]bool{}
	for _, term := range terms {
		if len([]rune(term)) < 3 {
			continue
		}
		re, err := regexp.Compile(`(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(term) + `($|[^\p{L}\p{N}])`)
		if err != nil {
			continue
		}
		hits, err := v.ix.Search(ctx, index.Query{Text: `"` + term + `"`, Limit: 100})
		if err != nil {
			continue
		}
		for _, h := range hits {
			src := h.Entry.Path
			if linked[src] || v.crypt.covers(src) && !v.crypt.searchable {
				continue
			}
			for i, l := range lc.get(src) {
				plain := reWikiFull.ReplaceAllString(l, "")
				plain = reMdFull.ReplaceAllString(plain, "")
				if !re.MatchString(plain) {
					continue
				}
				k := fmt.Sprintf("%s:%d", src, i+1)
				if seen[k] {
					continue
				}
				seen[k] = true
				mentions = append(mentions, BacklinkInfo{Source: h.Entry, Line: i + 1, Context: strings.TrimSpace(l), Relation: "mention"})
				if len(mentions) >= limit {
					return links, mentions, nil
				}
			}
		}
	}
	return links, mentions, nil
}

// RelatedNote is a Related result.
type RelatedNote struct {
	Entry   *index.Entry
	Score   float64
	Reasons []string
}

// Related ranks notes related to a note: direct links (both ways), shared
// link targets (co-citation, weighted by how rare the target is), shared
// sources (notes that link to both), shared tags, and embedding similarity
// when note vectors exist.
func (v *Vault) Related(ctx context.Context, idOrPath string, limit int) ([]RelatedNote, error) {
	rel, err := v.locate(ctx, idOrPath)
	if err != nil {
		return nil, err
	}
	me, ok := v.ix.Get(ctx, rel)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
	}
	if limit <= 0 {
		limit = 10
	}
	edges, err := v.ix.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*RelatedNote{}
	reasons := map[string]map[string]int{}
	add := func(p string, s float64, reason string) {
		if p == rel || p == "" {
			return
		}
		r := out[p]
		if r == nil {
			r = &RelatedNote{}
			out[p] = r
			reasons[p] = map[string]int{}
		}
		r.Score += s
		reasons[p][reason]++
	}
	indeg := map[string]int{}
	targetsOf := map[string]map[string]bool{}
	sourcesOf := map[string]map[string]bool{}
	for _, e := range edges {
		if e.Dst == "" || e.Src == e.Dst {
			continue
		}
		indeg[e.Dst]++
		if targetsOf[e.Src] == nil {
			targetsOf[e.Src] = map[string]bool{}
		}
		targetsOf[e.Src][e.Dst] = true
		if sourcesOf[e.Dst] == nil {
			sourcesOf[e.Dst] = map[string]bool{}
		}
		sourcesOf[e.Dst][e.Src] = true
	}
	for t := range targetsOf[rel] {
		add(t, 1, "links to")
	}
	for s := range sourcesOf[rel] {
		add(s, 1, "linked from")
	}
	for t := range targetsOf[rel] {
		w := 0.6 / math.Log2(2+float64(indeg[t]))
		for s := range sourcesOf[t] {
			add(s, w, "shared links")
		}
	}
	for s := range sourcesOf[rel] {
		for t := range targetsOf[s] {
			add(t, 0.3/math.Log2(2+float64(len(targetsOf[s]))), "co-cited")
		}
	}
	if len(me.Tags) > 0 {
		all, err := v.ix.ListByType(ctx, "")
		if err == nil {
			for _, e := range all {
				for _, t := range me.Tags {
					if index.HasAnyTag(e.Tags, []string{t}) {
						add(e.Path, 0.2, "shared tags")
					}
				}
			}
		}
	}
	if sims, err := v.ix.SimilarNotes(ctx, rel, 50); err == nil {
		for _, s := range sims {
			if s.Score >= 0.5 {
				add(s.Path, s.Score, "similar text")
			}
		}
	}
	var res []RelatedNote
	for p, r := range out {
		e, ok := v.ix.Get(ctx, p)
		if !ok {
			continue
		}
		r.Entry = e
		var rs []string
		for k, n := range reasons[p] {
			if n > 1 && (k == "shared links" || k == "shared tags" || k == "co-cited") {
				rs = append(rs, fmt.Sprintf("%s: %d", k, n))
			} else {
				rs = append(rs, k)
			}
		}
		sort.Strings(rs)
		r.Reasons = rs
		res = append(res, *r)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Score != res[j].Score {
			return res[i].Score > res[j].Score
		}
		return res[i].Entry.Path < res[j].Entry.Path
	})
	if len(res) > limit {
		res = res[:limit]
	}
	return res, nil
}

// GraphStats summarises the link graph.
type GraphStats struct {
	Notes, Edges, Dangling int
	Orphans                []*index.Entry
	Hubs                   []Hub
	TopDangling            []string
}

// Hub is a highly linked note.
type Hub struct {
	Entry   *index.Entry
	In, Out int
}

// Stats computes orphans (no links in or out), hubs (most links) and the
// most-linked missing notes.
func (v *Vault) Stats(ctx context.Context, limit int) (*GraphStats, error) {
	if limit <= 0 {
		limit = 20
	}
	all, err := v.ix.ListByType(ctx, "")
	if err != nil {
		return nil, err
	}
	edges, err := v.ix.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	in, out := map[string]int{}, map[string]int{}
	dang := map[string]int{}
	dangName := map[string]string{}
	st := &GraphStats{Notes: len(all)}
	for _, e := range edges {
		if e.Dst == "" {
			st.Dangling++
			k := index.NormalizeTarget(e.Target)
			dang[k]++
			dangName[k] = e.Target
			continue
		}
		if e.Src == e.Dst {
			continue
		}
		st.Edges++
		out[e.Src]++
		in[e.Dst]++
	}
	var hubs []Hub
	for _, e := range all {
		if in[e.Path]+out[e.Path] == 0 {
			st.Orphans = append(st.Orphans, e)
		} else {
			hubs = append(hubs, Hub{Entry: e, In: in[e.Path], Out: out[e.Path]})
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		a, b := hubs[i].In*2+hubs[i].Out, hubs[j].In*2+hubs[j].Out
		if a != b {
			return a > b
		}
		return hubs[i].Entry.Path < hubs[j].Entry.Path
	})
	if len(hubs) > limit {
		hubs = hubs[:limit]
	}
	st.Hubs = hubs
	if len(st.Orphans) > limit {
		st.Orphans = st.Orphans[:limit]
	}
	keys := make([]string, 0, len(dang))
	for k := range dang {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if dang[keys[i]] != dang[keys[j]] {
			return dang[keys[i]] > dang[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for i, k := range keys {
		if i >= limit {
			break
		}
		st.TopDangling = append(st.TopDangling, dangName[k])
	}
	return st, nil
}
