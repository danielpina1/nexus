package vault

import (
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// Query language (Dataview-lite), used by the Query RPC, by Search when the
// text contains filters, and by the vault.query agent tool:
//
//	type:person tag:work updated:>2026-09-01 links:[[ACME]] sort:-updated limit:20
//
// Terms are AND-ed; a leading "-" negates a filter; comma-separated values
// are OR-ed (type:person,project). Free words and "quoted phrases" are a
// full-text search (hits get **highlighted** snippets and a score).
//
//	type:X           frontmatter type
//	tag:X            tag (nested tags match: tag:work matches work/acme)
//	path:P folder:P  path prefix (path:people/)
//	title:T          title contains T (case-insensitive)
//	links:[[X]]      notes linking to X (X resolved like a wikilink)
//	from:[[X]]       notes X links to
//	created: updated:  dates, with > >= < <= = and YYYY-MM-DD, YYYY-MM,
//	                 today, yesterday, or relative -7d / -2w / -3m / -1y
//	has:field        frontmatter field present and non-empty
//	                 (has:tasks / has:open-tasks / has:links / has:backlinks)
//	orphan:true      no links in or out
//	<field>:V        any other key compares a frontmatter field: text is
//	                 matched case-insensitively (lists match any element),
//	                 numbers and dates support > >= < <=; [[links]] match
//	                 the note they resolve to
//	sort:F sort:-F   updated, created, title, path, type, score or a field
//	limit:N          default 50, max 500

// QueryHit is one query result.
type QueryHit struct {
	Entry   *index.Entry
	Snippet string
	Score   float64
}

// QueryResult is the outcome of Query.
type QueryResult struct {
	Hits     []QueryHit
	Total    int
	Warnings []string
}

type qterm struct {
	key, op, val string
	neg          bool
}

var reQueryKey = regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_.\-]*$`)

// knownQueryKeys route Search text to the query engine.
var knownQueryKeys = map[string]bool{
	"type": true, "tag": true, "path": true, "folder": true, "title": true, "links": true, "from": true,
	"created": true, "updated": true, "has": true, "orphan": true, "sort": true, "limit": true,
}

// tokenizeQuery splits on spaces outside "quotes" and [[links]].
func tokenizeQuery(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote, depth := false, 0
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	rs := []rune(q)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"' && depth == 0:
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == '[' && i+1 < len(rs) && rs[i+1] == '[' && !inQuote:
			depth++
			cur.WriteString("[[")
			i++
		case r == ']' && i+1 < len(rs) && rs[i+1] == ']' && depth > 0:
			depth--
			cur.WriteString("]]")
			i++
		case unicode.IsSpace(r) && !inQuote && depth == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func parseQuery(q string) (terms []qterm, text []string) {
	for _, tok := range tokenizeQuery(q) {
		k, val, ok := strings.Cut(tok, ":")
		if !ok || val == "" || !reQueryKey.MatchString(k) || strings.HasPrefix(val, "//") {
			text = append(text, strings.Trim(tok, `"`))
			continue
		}
		t := qterm{key: strings.ToLower(k)}
		if strings.HasPrefix(t.key, "-") {
			t.neg, t.key = true, t.key[1:]
		}
		for _, op := range []string{">=", "<=", "!=", ">", "<", "="} {
			if strings.HasPrefix(val, op) {
				t.op, val = op, val[len(op):]
				break
			}
		}
		t.val = strings.Trim(val, `"`)
		terms = append(terms, t)
	}
	return terms, text
}

// LooksLikeQuery reports whether search text uses query filters.
func LooksLikeQuery(q string) bool {
	terms, _ := parseQuery(q)
	for _, t := range terms {
		if knownQueryKeys[t.key] {
			return true
		}
	}
	return false
}

// Query runs the query language. limit > 0 overrides limit: in the query.
func (v *Vault) Query(ctx context.Context, q string, limit int) (*QueryResult, error) {
	terms, text := parseQuery(q)
	res := &QueryResult{}
	sortKey, desc := "", false
	qlimit := 50
	var filters []qterm
	for _, t := range terms {
		switch t.key {
		case "sort":
			sortKey, desc = t.val, false
			if strings.HasPrefix(sortKey, "-") {
				sortKey, desc = sortKey[1:], true
			}
			if t.neg {
				desc = true
			}
		case "limit":
			n, err := strconv.Atoi(t.val)
			if err != nil || n <= 0 {
				res.Warnings = append(res.Warnings, "limit must be a positive number")
				continue
			}
			qlimit = n
		default:
			filters = append(filters, t)
		}
	}
	if limit > 0 {
		qlimit = limit
	}
	qlimit = min(qlimit, 500)
	// Candidates: full-text hits, or every note.
	var cands []QueryHit
	ftext := strings.TrimSpace(strings.Join(text, " "))
	if ftext != "" {
		hits, err := v.ix.Search(ctx, index.Query{Text: ftext, Limit: 100})
		if err != nil {
			return nil, err
		}
		// Search caps at 100; widen with a second pass for big result sets.
		for _, h := range hits {
			cands = append(cands, QueryHit{Entry: h.Entry, Snippet: h.Snippet, Score: h.Score})
		}
	} else {
		all, err := v.ix.ListByType(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, e := range all {
			cands = append(cands, QueryHit{Entry: e})
		}
	}
	qc := &queryCtx{v: v, ctx: ctx, now: v.now()}
	for _, f := range filters {
		if w := qc.prepare(f); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
	}
	var out []QueryHit
	for _, c := range cands {
		ok := true
		for _, f := range filters {
			m, err := qc.match(c.Entry, f)
			if err != nil {
				ok = false
				break
			}
			if m == f.neg {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	if sortKey == "" && ftext == "" {
		sortKey, desc = "updated", true
	}
	if sortKey != "" && sortKey != "score" {
		sort.SliceStable(out, func(i, j int) bool {
			// Notes without the sort field always go last.
			mi, mj := missingSortKey(out[i].Entry, sortKey), missingSortKey(out[j].Entry, sortKey)
			if mi != mj {
				return mj
			}
			c := compareEntries(out[i].Entry, out[j].Entry, sortKey)
			if desc {
				return c > 0
			}
			return c < 0
		})
	} else if sortKey == "score" && !desc {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	}
	for i := range out {
		if out[i].Snippet == "" {
			out[i].Snippet = out[i].Entry.Title
		}
	}
	res.Total = len(out)
	if len(out) > qlimit {
		out = out[:qlimit]
	}
	res.Hits = out
	res.Warnings = uniqueStrings(res.Warnings, "")
	return res, nil
}

func missingSortKey(e *index.Entry, key string) bool {
	switch key {
	case "updated", "modified", "mtime", "created", "ctime", "title", "name", "path", "file", "type", "size":
		return false
	}
	_, ok := lookupField(e.Frontmatter, key)
	return !ok
}

func compareEntries(a, b *index.Entry, key string) int {
	switch key {
	case "updated", "modified", "mtime":
		return a.Updated.Compare(b.Updated)
	case "created", "ctime":
		return a.Created.Compare(b.Created)
	case "title", "name":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "path", "file":
		return strings.Compare(a.Path, b.Path)
	case "type":
		return strings.Compare(a.Type, b.Type)
	case "size":
		return cmpInt(a.Size, b.Size)
	}
	av, bv := a.Frontmatter[key], b.Frontmatter[key]
	if av == nil || bv == nil {
		switch {
		case av == nil && bv == nil:
			return 0
		case av == nil:
			return 1 // missing values last
		default:
			return -1
		}
	}
	if af, ok := toFloat(av); ok {
		if bf, ok := toFloat(bv); ok {
			return cmpFloat(af, bf)
		}
	}
	if at, ok := index.ParseTime(av); ok {
		if bt, ok := index.ParseTime(bv); ok {
			return at.Compare(bt)
		}
	}
	return strings.Compare(strings.ToLower(fmt.Sprint(av)), strings.ToLower(fmt.Sprint(bv)))
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil && !math.IsNaN(f)
	}
	return 0, false
}

type queryCtx struct {
	v   *Vault
	ctx context.Context
	now time.Time

	edges    []index.Edge
	edgesErr error
	loaded   bool
	links    map[string]map[string]bool // term value → set of matching paths
	tasks    map[string][2]int          // path → (open, done)
}

func (qc *queryCtx) allEdges() ([]index.Edge, error) {
	if !qc.loaded {
		qc.edges, qc.edgesErr = qc.v.ix.AllEdges(qc.ctx)
		qc.loaded = true
	}
	return qc.edges, qc.edgesErr
}

func linkValue(s string) string {
	if ls := note.ExtractLinks(s); len(ls) > 0 {
		return ls[0].Target
	}
	return s
}

// prepare precomputes link sets; it returns a warning for bad terms.
func (qc *queryCtx) prepare(t qterm) string {
	switch t.key {
	case "links", "from":
		if qc.links == nil {
			qc.links = map[string]map[string]bool{}
		}
		edges, err := qc.allEdges()
		if err != nil {
			return err.Error()
		}
		for _, val := range strings.Split(t.val, ",") {
			target := linkValue(strings.TrimSpace(val))
			set := map[string]bool{}
			e, ok := qc.v.ix.Resolve(qc.ctx, target, "")
			for _, ed := range edges {
				switch t.key {
				case "links":
					if (ok && ed.Dst == e.Path) || (ed.Dst == "" && index.NormalizeTarget(ed.Target) == index.NormalizeTarget(target)) {
						set[ed.Src] = true
					}
				case "from":
					if ok && ed.Src == e.Path && ed.Dst != "" {
						set[ed.Dst] = true
					}
				}
			}
			qc.links[t.key+"\x00"+val] = set
		}
	case "created", "updated":
		for _, val := range strings.Split(t.val, ",") {
			if _, _, ok := qc.parseDate(val); !ok {
				return fmt.Sprintf("%s: cannot parse date %q", t.key, val)
			}
		}
	case "has":
		if strings.HasSuffix(t.val, "tasks") && qc.tasks == nil {
			qc.tasks = map[string][2]int{}
			ts, err := qc.v.ix.Tasks(qc.ctx, index.TaskFilter{})
			if err != nil {
				return err.Error()
			}
			for _, tk := range ts {
				c := qc.tasks[tk.Path]
				if tk.Done {
					c[1]++
				} else {
					c[0]++
				}
				qc.tasks[tk.Path] = c
			}
		}
	}
	return ""
}

// parseDate returns the [from, to) range a date value denotes.
func (qc *queryCtx) parseDate(s string) (time.Time, time.Time, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	loc := qc.now.Location()
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	switch s {
	case "today":
		d := day(qc.now)
		return d, d.AddDate(0, 0, 1), true
	case "yesterday":
		d := day(qc.now).AddDate(0, 0, -1)
		return d, d.AddDate(0, 0, 1), true
	}
	if len(s) >= 3 && (s[0] == '-' || s[0] == '+') {
		n, err := strconv.Atoi(s[1 : len(s)-1])
		if err == nil {
			if s[0] == '-' {
				n = -n
			}
			var t time.Time
			switch s[len(s)-1] {
			case 'd':
				t = qc.now.AddDate(0, 0, n)
			case 'w':
				t = qc.now.AddDate(0, 0, 7*n)
			case 'm':
				t = qc.now.AddDate(0, n, 0)
			case 'y':
				t = qc.now.AddDate(n, 0, 0)
			case 'h':
				t = qc.now.Add(time.Duration(n) * time.Hour)
			default:
				return time.Time{}, time.Time{}, false
			}
			return t, t, true
		}
	}
	if t, err := time.ParseInLocation("2006-01", s, loc); err == nil && len(s) == 7 {
		return t, t.AddDate(0, 1, 0), true
	}
	if t, err := time.ParseInLocation("2006", s, loc); err == nil && len(s) == 4 {
		return t, t.AddDate(1, 0, 0), true
	}
	if t, ok := index.ParseTime(s); ok {
		if len(s) == 10 {
			return t, t.AddDate(0, 0, 1), true
		}
		return t, t, true
	}
	return time.Time{}, time.Time{}, false
}

func cmpTime(t time.Time, op string, from, to time.Time) bool {
	switch op {
	case ">":
		if to.After(from) {
			return !t.Before(to)
		}
		return t.After(from)
	case ">=":
		return !t.Before(from)
	case "<":
		return t.Before(from)
	case "<=":
		if to.After(from) {
			return t.Before(to)
		}
		return !t.After(from)
	case "!=":
		return !(cmpTime(t, "", from, to))
	default:
		if to.After(from) {
			return !t.Before(from) && t.Before(to)
		}
		return t.Equal(from)
	}
}

func (qc *queryCtx) match(e *index.Entry, t qterm) (bool, error) {
	vals := strings.Split(t.val, ",")
	any := func(fn func(string) bool) bool {
		for _, v := range vals {
			if fn(strings.TrimSpace(v)) {
				return true
			}
		}
		return false
	}
	switch t.key {
	case "type":
		return any(func(v string) bool { return strings.EqualFold(e.Type, v) }), nil
	case "tag", "tags":
		return any(func(v string) bool { return index.HasAnyTag(e.Tags, []string{v}) }), nil
	case "path", "folder", "in":
		return any(func(v string) bool {
			v = strings.TrimPrefix(v, "/")
			return strings.HasPrefix(strings.ToLower(e.Path), strings.ToLower(v))
		}), nil
	case "title":
		return any(func(v string) bool { return strings.Contains(strings.ToLower(e.Title), strings.ToLower(v)) }), nil
	case "id":
		return any(func(v string) bool { return strings.EqualFold(e.ID, v) }), nil
	case "links", "from":
		return any(func(v string) bool { return qc.links[t.key+"\x00"+v][e.Path] }), nil
	case "created", "updated":
		tm := e.Updated
		if t.key == "created" {
			tm = e.Created
		}
		return any(func(v string) bool {
			from, to, ok := qc.parseDate(v)
			return ok && cmpTime(tm, t.op, from, to)
		}), nil
	case "orphan":
		edges, err := qc.allEdges()
		if err != nil {
			return false, err
		}
		linked := false
		for _, ed := range edges {
			if (ed.Src == e.Path && ed.Dst != "" && ed.Dst != e.Path) || (ed.Dst == e.Path && ed.Src != e.Path) {
				linked = true
				break
			}
		}
		want := !strings.EqualFold(t.val, "false")
		return linked != want, nil
	case "has":
		return any(func(v string) bool { return qc.has(e, v) }), nil
	}
	// Frontmatter field.
	fv, ok := lookupField(e.Frontmatter, t.key)
	if !ok {
		return false, nil
	}
	return any(func(v string) bool { return qc.fieldMatch(e, fv, t.op, v) }), nil
}

func lookupField(fm map[string]any, key string) (any, bool) {
	if v, ok := fm[key]; ok {
		return v, v != nil
	}
	for k, v := range fm {
		if strings.EqualFold(k, key) || note.NormalizeKey(k) == key {
			return v, v != nil
		}
	}
	// Dotted keys: "delivery.preferred_window".
	if head, rest, ok := strings.Cut(key, "."); ok {
		if m, ok := fm[head].(map[string]any); ok {
			return lookupField(m, rest)
		}
	}
	return nil, false
}

func (qc *queryCtx) has(e *index.Entry, what string) bool {
	switch strings.ToLower(what) {
	case "tasks":
		c := qc.tasks[e.Path]
		return c[0]+c[1] > 0
	case "open-tasks", "open_tasks":
		return qc.tasks[e.Path][0] > 0
	case "links", "backlinks":
		edges, _ := qc.allEdges()
		for _, ed := range edges {
			if what == "links" && ed.Src == e.Path {
				return true
			}
			if what == "backlinks" && ed.Dst == e.Path && ed.Src != e.Path {
				return true
			}
		}
		return false
	case "tags":
		return len(e.Tags) > 0
	case "aliases":
		return len(e.Aliases) > 0
	case "id":
		return e.ID != ""
	}
	v, ok := lookupField(e.Frontmatter, what)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	}
	return true
}

func (qc *queryCtx) fieldMatch(e *index.Entry, fv any, op, want string) bool {
	if list, ok := fv.([]any); ok {
		if op == "!=" {
			for _, x := range list {
				if qc.fieldMatch(e, x, "=", want) {
					return false
				}
			}
			return true
		}
		for _, x := range list {
			if qc.fieldMatch(e, x, op, want) {
				return true
			}
		}
		return false
	}
	if op == "!=" {
		return !qc.fieldMatch(e, fv, "=", want)
	}
	// Links: compare resolved notes.
	if s, ok := fv.(string); ok && strings.Contains(s, "[[") {
		for _, l := range note.ExtractLinks(s) {
			a, aok := qc.v.ix.Resolve(qc.ctx, l.Target, e.Path)
			b, bok := qc.v.ix.Resolve(qc.ctx, linkValue(want), "")
			if aok && bok && a.Path == b.Path {
				return true
			}
			if strings.EqualFold(index.NormalizeTarget(l.Target), index.NormalizeTarget(linkValue(want))) {
				return true
			}
		}
		return false
	}
	if op == ">" || op == ">=" || op == "<" || op == "<=" {
		if a, ok := toFloat(fv); ok {
			if b, err := strconv.ParseFloat(want, 64); err == nil {
				c := cmpFloat(a, b)
				return (op == ">" && c > 0) || (op == ">=" && c >= 0) || (op == "<" && c < 0) || (op == "<=" && c <= 0)
			}
		}
		if a, ok := index.ParseTime(fv); ok {
			if from, to, ok := qc.parseDate(want); ok {
				return cmpTime(a, op, from, to)
			}
		}
		return false
	}
	switch t := fv.(type) {
	case bool:
		return strings.EqualFold(strconv.FormatBool(t), want)
	case float64, int, int64:
		b, err := strconv.ParseFloat(want, 64)
		a, _ := toFloat(t)
		return err == nil && a == b
	}
	s := fmt.Sprint(fv)
	if a, ok := index.ParseTime(s); ok && op == "=" {
		if from, to, ok := qc.parseDate(want); ok {
			return cmpTime(a, "=", from, to)
		}
	}
	return strings.EqualFold(strings.TrimSpace(s), want) || (op == "" && strings.Contains(strings.ToLower(s), strings.ToLower(want)) && len(want) >= 3)
}

var _ = path.Base
