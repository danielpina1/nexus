package index

import (
	"context"
	"path"
	"strings"
)

// Link resolution (Obsidian-compatible), applied to a normalised target t
// (lower case, no .md) written in note `from`:
//
//  1. t contains "/": the path relative to the linking note's folder, then
//     the path from the vault root, then any note whose path ends in "/"+t.
//  2. By file name (case-insensitive). Several matches: a note in the same
//     folder as `from` wins, else the shortest path, then alphabetical.
//  3. By alias (frontmatter aliases, case-insensitive), same tie-break.
//  4. By frontmatter id (case-insensitive), so [[01JABC...]] works.
//
// Anything else is a dangling link: kept in edges and reported with an
// "unresolved:<target>" node id in graphs.
type resolver struct {
	byPath  map[string]string   // lower path without .md → path
	byName  map[string][]string // lower basename → paths
	byAlias map[string][]string // lower alias → paths
	byID    map[string]string   // lower id → path
	entries map[string]*Entry   // path → entry
}

// UnresolvedPrefix marks graph nodes for dangling links.
const UnresolvedPrefix = "unresolved:"

func (ix *Index) resolver(ctx context.Context) (*resolver, error) {
	gen := ix.gen.Load()
	ix.rmu.Lock()
	defer ix.rmu.Unlock()
	if ix.res != nil && ix.resGen == gen {
		return ix.res, nil
	}
	all, err := ix.ListByType(ctx, "")
	if err != nil {
		return nil, err
	}
	r := &resolver{
		byPath: map[string]string{}, byName: map[string][]string{}, byAlias: map[string][]string{},
		byID: map[string]string{}, entries: map[string]*Entry{},
	}
	for _, e := range all {
		r.entries[e.Path] = e
		r.byPath[NormalizeTarget(e.Path)] = e.Path
		n := baseName(e.Path)
		r.byName[n] = append(r.byName[n], e.Path)
		for _, a := range e.Aliases {
			a = strings.ToLower(strings.TrimSpace(a))
			r.byAlias[a] = append(r.byAlias[a], e.Path)
		}
		if e.ID != "" {
			if _, dup := r.byID[strings.ToLower(e.ID)]; !dup {
				r.byID[strings.ToLower(e.ID)] = e.Path
			}
		}
	}
	for _, m := range []map[string][]string{r.byName, r.byAlias} {
		for _, ps := range m {
			sortPaths(ps)
		}
	}
	ix.res, ix.resGen = r, gen
	return r, nil
}

func pick(cands []string, from string) string {
	if len(cands) == 0 {
		return ""
	}
	dir := path.Dir(from)
	for _, c := range cands {
		if path.Dir(c) == dir {
			return c
		}
	}
	return cands[0]
}

func (r *resolver) resolve(t, from string) string {
	if t == "" {
		return ""
	}
	if strings.Contains(t, "/") {
		if from != "" {
			if p, ok := r.byPath[NormalizeTarget(path.Join(path.Dir(strings.ToLower(from)), t))]; ok {
				return p
			}
		}
		if p, ok := r.byPath[t]; ok {
			return p
		}
		var cands []string
		for k, p := range r.byPath {
			if strings.HasSuffix(k, "/"+t) {
				cands = append(cands, p)
			}
		}
		sortPaths(cands)
		if p := pick(cands, from); p != "" {
			return p
		}
	}
	if p := pick(r.byName[t], from); p != "" {
		return p
	}
	if p := pick(r.byAlias[t], from); p != "" {
		return p
	}
	return r.byID[t]
}

// Resolve resolves a link target as written in note `from` (may be empty).
func (ix *Index) Resolve(ctx context.Context, target, from string) (*Entry, bool) {
	r, err := ix.resolver(ctx)
	if err != nil {
		return nil, false
	}
	p := r.resolve(NormalizeTarget(target), from)
	if p == "" {
		return nil, false
	}
	return r.entries[p], true
}

// Edge is a link between notes. Dst is the resolved path, or "" for a
// dangling link (Target holds what was written).
type Edge struct {
	Src      string
	Dst      string
	Target   string
	Relation string
	Heading  string
	Embed    bool
	// Line is the 1-based line of the link in Src (0 = frontmatter).
	Line int
}

func (ix *Index) edges(ctx context.Context, where string, args ...any) ([]Edge, error) {
	r, err := ix.resolver(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT src, dst, relation, raw, heading, embed, line FROM edges `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		var dst string
		var embed int
		if err := rows.Scan(&e.Src, &dst, &e.Relation, &e.Target, &e.Heading, &embed, &e.Line); err != nil {
			return nil, err
		}
		e.Embed = embed == 1
		e.Dst = r.resolve(dst, e.Src)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Outlinks returns the links of a note, resolved.
func (ix *Index) Outlinks(ctx context.Context, rel string) ([]Edge, error) {
	return ix.edges(ctx, `WHERE src = ? ORDER BY rowid`, rel)
}

// Backlinks returns the links pointing at a note (any relation).
func (ix *Index) Backlinks(ctx context.Context, rel string) ([]Edge, error) {
	r, err := ix.resolver(ctx)
	if err != nil {
		return nil, err
	}
	e, ok := r.entries[rel]
	if !ok {
		return nil, nil
	}
	keys := map[string]bool{baseName(rel): true}
	np := NormalizeTarget(rel)
	for {
		keys[np] = true
		i := strings.Index(np, "/")
		if i < 0 {
			break
		}
		np = np[i+1:]
	}
	for _, a := range e.Aliases {
		keys[strings.ToLower(strings.TrimSpace(a))] = true
	}
	if e.ID != "" {
		keys[strings.ToLower(e.ID)] = true
	}
	var args []any
	var ph []string
	for k := range keys {
		args = append(args, k)
		ph = append(ph, "?")
	}
	// Relative links ("../x") are rare; they are only caught when the last
	// segment matches, which the keys above already cover for most cases.
	cands, err := ix.edges(ctx, `WHERE dst IN (`+strings.Join(ph, ",")+`) OR dst LIKE '%../%' ORDER BY src, rowid`, args...)
	if err != nil {
		return nil, err
	}
	var out []Edge
	for _, c := range cands {
		if c.Dst == rel && c.Src != rel {
			out = append(out, c)
		}
	}
	return out, nil
}

// AllEdges returns every edge, resolved.
func (ix *Index) AllEdges(ctx context.Context) ([]Edge, error) {
	return ix.edges(ctx, `ORDER BY src, rowid`)
}

// GraphResult is a subgraph. Nodes holds indexed notes; Dangling holds the
// targets of unresolved links that appear in Edges (Dst == "").
type GraphResult struct {
	Nodes    []*Entry
	Edges    []Edge
	Dangling []string
}

// Graph returns the neighbourhood of center (a path) up to depth hops,
// following links in both directions. An empty center returns the whole
// graph. types filters nodes (the center is always kept); limit caps nodes.
// Dangling links are included only when no type filter is given.
func (ix *Index) Graph(ctx context.Context, center string, depth int, types []string, limit int) (*GraphResult, error) {
	if limit <= 0 {
		limit = 200
	}
	if depth <= 0 {
		depth = 1
	}
	r, err := ix.resolver(ctx)
	if err != nil {
		return nil, err
	}
	typeOK := func(p string) bool {
		if len(types) == 0 {
			return true
		}
		e := r.entries[p]
		for _, t := range types {
			if e != nil && strings.EqualFold(e.Type, t) {
				return true
			}
		}
		return false
	}
	all, err := ix.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	include := map[string]bool{}
	var order []string
	add := func(p string) bool {
		if include[p] || len(order) >= limit {
			return false
		}
		include[p] = true
		order = append(order, p)
		return true
	}
	if center == "" {
		var paths []string
		for p := range r.entries {
			if typeOK(p) {
				paths = append(paths, p)
			}
		}
		sortPaths(paths)
		for _, p := range paths {
			add(p)
		}
	} else {
		if _, ok := r.entries[center]; !ok {
			return nil, ErrNotFound
		}
		adj := map[string][]string{}
		for _, e := range all {
			if e.Dst == "" {
				continue
			}
			adj[e.Src] = append(adj[e.Src], e.Dst)
			adj[e.Dst] = append(adj[e.Dst], e.Src)
		}
		add(center)
		frontier := []string{center}
		for d := 0; d < depth && len(frontier) > 0; d++ {
			var next []string
			for _, p := range frontier {
				for _, q := range adj[p] {
					if !include[q] && typeOK(q) && add(q) {
						next = append(next, q)
					}
				}
			}
			frontier = next
		}
	}
	res := &GraphResult{}
	for _, p := range order {
		res.Nodes = append(res.Nodes, r.entries[p])
	}
	dangling := map[string]bool{}
	seen := map[string]bool{}
	for _, e := range all {
		if !include[e.Src] {
			continue
		}
		if e.Dst == "" {
			// Dangling: whole graph, or links leaving the centre's neighbourhood.
			if len(types) > 0 || (center != "" && e.Src != center) {
				continue
			}
			k := NormalizeTarget(e.Target)
			if !dangling[k] {
				dangling[k] = true
				res.Dangling = append(res.Dangling, e.Target)
			}
		} else if !include[e.Dst] || e.Src == e.Dst {
			continue
		}
		k := e.Src + "\x00" + e.Dst + "\x00" + e.Relation
		if e.Dst == "" {
			k += "\x00" + NormalizeTarget(e.Target)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		res.Edges = append(res.Edges, e)
	}
	return res, nil
}
