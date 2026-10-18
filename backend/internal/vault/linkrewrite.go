package vault

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

var (
	reWikiFull = regexp.MustCompile(`(!?)\[\[([^\[\]\n]+?)\]\]`)
	reMdFull   = regexp.MustCompile(`(!?)\[([^\]\n]*)\]\(([^)\s]+)((?:\s+"[^"]*")?)\)`)
	reFenceRW  = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
)

// linkTarget is how a rewritten link should point at its note.
type linkTarget struct {
	path    string // new vault path of the target note
	heading string // replacement heading ("" keeps the link's own; "-" drops it)
}

// rewriter rewrites links in note text after notes move (rename, merge,
// split). resolve maps a link target written in `from` to a vault path
// using the index as it was before the move.
type rewriter struct {
	ctx     context.Context
	ix      *index.Index
	moves   map[string]linkTarget            // old path → new location
	anchors map[string]map[string]linkTarget // old path → lower heading → new note (split)
	// names reports how many notes have a given lower-case base name after
	// the move (to choose between [[name]] and [[folder/name]]).
	names map[string]int
	// keepIDs: id links stay valid (rename keeps the note's id).
	keepIDs bool
}

func newRewriter(ctx context.Context, ix *index.Index, moves map[string]linkTarget) (*rewriter, error) {
	all, err := ix.ListByType(ctx, "")
	if err != nil {
		return nil, err
	}
	rw := &rewriter{ctx: ctx, ix: ix, moves: moves, anchors: map[string]map[string]linkTarget{}, names: map[string]int{}}
	moved := map[string]bool{}
	for old := range moves {
		moved[old] = true
	}
	for _, e := range all {
		if !moved[e.Path] {
			rw.names[baseNoExt(e.Path)]++
		}
	}
	added := map[string]bool{}
	for _, t := range moves {
		if !added[t.path] && !rw.exists(t.path, all) {
			rw.names[baseNoExt(t.path)]++
			added[t.path] = true
		}
	}
	return rw, nil
}

func (rw *rewriter) exists(p string, all []*index.Entry) bool {
	for _, e := range all {
		if e.Path == p {
			if _, moved := rw.moves[p]; !moved {
				return true
			}
		}
	}
	return false
}

func baseNoExt(p string) string {
	return strings.ToLower(strings.TrimSuffix(path.Base(p), path.Ext(p)))
}

// linkText is the shortest unambiguous wikilink text for a path.
func (rw *rewriter) linkText(p string, pathStyle bool) string {
	noExt := strings.TrimSuffix(p, ".md")
	if pathStyle || rw.names[baseNoExt(p)] > 1 {
		return noExt
	}
	return path.Base(noExt)
}

// rewrite returns text with links fixed. from is where the note's links
// were resolved (its old path), to its new path (same unless it moves).
func (rw *rewriter) rewrite(text, from, to string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	fence := ""
	changed := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trim, fence) && strings.Trim(trim, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := reFenceRW.FindStringSubmatch(line); m != nil {
			fence = m[1]
			continue
		}
		nl := reWikiFull.ReplaceAllStringFunc(line, func(m string) string {
			sub := reWikiFull.FindStringSubmatch(m)
			out, ok := rw.wiki(sub[1], sub[2], from)
			if !ok {
				return m
			}
			return out
		})
		nl = reMdFull.ReplaceAllStringFunc(nl, func(m string) string {
			sub := reMdFull.FindStringSubmatch(m)
			out, ok := rw.md(sub, from, to)
			if !ok {
				return m
			}
			return out
		})
		if nl != line {
			lines[i] = nl
			changed = true
		}
	}
	return strings.Join(lines, ""), changed
}

func (rw *rewriter) target(old string, heading string) (linkTarget, bool) {
	if hs, ok := rw.anchors[old]; ok && heading != "" {
		if t, ok := hs[strings.ToLower(heading)]; ok {
			return t, true
		}
	}
	t, ok := rw.moves[old]
	return t, ok
}

func (rw *rewriter) wiki(bang, inner, from string) (string, bool) {
	escaped := strings.Contains(inner, `\|`)
	l, ok := note.ParseLinkText(inner)
	if !ok || index.IsAttachmentTarget(l.Target) {
		return "", false
	}
	e, ok := rw.ix.Resolve(rw.ctx, l.Target, from)
	if !ok {
		return "", false
	}
	splitHeading := false
	if hs, ok := rw.anchors[e.Path]; ok && l.Heading != "" {
		_, splitHeading = hs[strings.ToLower(l.Heading)]
	}
	t, ok := rw.target(e.Path, l.Heading)
	if !ok {
		return "", false
	}
	norm := index.NormalizeTarget(l.Target)
	byName := norm == baseNoExt(e.Path)
	byPath := strings.Contains(norm, "/") && strings.HasSuffix(index.NormalizeTarget(e.Path), norm)
	byID := e.ID != "" && norm == strings.ToLower(e.ID)
	if !splitHeading {
		if t.path == e.Path {
			return "", false
		}
		// Aliases (and, on rename, ids) travel with the note: such links
		// keep working unchanged.
		if !byName && !byPath && (!byID || rw.keepIDs) {
			return "", false
		}
	}
	newTarget := rw.linkText(t.path, byPath)
	if strings.HasSuffix(strings.ToLower(l.Target), ".md") {
		newTarget += ".md"
	}
	heading := l.Heading
	if splitHeading || t.heading == "-" {
		heading = ""
	} else if t.heading != "" {
		heading = t.heading
	}
	out := newTarget
	if heading != "" {
		out += "#" + heading
	}
	if l.Alias != "" {
		sep := "|"
		if escaped {
			sep = `\|`
		}
		out += sep + l.Alias
	}
	return bang + "[[" + out + "]]", true
}

func (rw *rewriter) md(sub []string, from, to string) (string, bool) {
	bang, text, raw, title := sub[1], sub[2], sub[3], sub[4]
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "mailto:") {
		return "", false
	}
	target, frag := raw, ""
	if i := strings.Index(raw, "#"); i >= 0 {
		target, frag = raw[:i], raw[i:]
	}
	encoded := strings.Contains(target, "%")
	dec, err := url.PathUnescape(target)
	if err != nil {
		dec = target
	}
	if !strings.HasSuffix(strings.ToLower(dec), ".md") {
		return "", false
	}
	// Resolve relative to the note's folder first, then the vault root.
	var resolved string
	cand := path.Clean(path.Join(path.Dir(from), dec))
	if strings.HasPrefix(dec, "/") {
		cand = strings.TrimPrefix(path.Clean(dec), "/")
	}
	if e, ok := rw.ix.Get(rw.ctx, cand); ok {
		resolved = e.Path
	} else if e, ok := rw.ix.Get(rw.ctx, path.Clean(dec)); ok {
		resolved = e.Path
	} else {
		return "", false
	}
	newPath := resolved
	if t, ok := rw.target(resolved, strings.TrimPrefix(frag, "#")); ok {
		newPath = t.path
		if t.heading == "-" {
			frag = ""
		}
	}
	if newPath == resolved && path.Dir(from) == path.Dir(to) {
		return "", false
	}
	rel := relPath(path.Dir(to), newPath)
	if encoded {
		rel = strings.ReplaceAll(rel, " ", "%20")
	}
	return bang + "[" + text + "](" + rel + frag + title + ")", true
}

// relPath returns target relative to dir (both vault paths).
func relPath(dir, target string) string {
	if dir == "." || dir == "" {
		return target
	}
	ds := strings.Split(dir, "/")
	ts := strings.Split(target, "/")
	i := 0
	for i < len(ds) && i < len(ts)-1 && ds[i] == ts[i] {
		i++
	}
	var parts []string
	for j := i; j < len(ds); j++ {
		parts = append(parts, "..")
	}
	parts = append(parts, ts[i:]...)
	return strings.Join(parts, "/")
}
