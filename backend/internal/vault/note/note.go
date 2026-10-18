// Package note parses Obsidian-compatible Markdown notes: YAML frontmatter,
// title, [[wikilinks]] (aliases, headings, embeds), #tags, Dataview-style
// inline fields (`Key:: value`) and typed relations from frontmatter.
//
// The parser is line based. Fenced code blocks, inline code spans, HTML
// comments and URLs are ignored when extracting links, tags and fields.
package note

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// RelLinksTo is the relation of plain body links.
const RelLinksTo = "links_to"

// Link is one reference from a note to another.
type Link struct {
	// Target is the link target without heading or alias, as written
	// (e.g. "people/João" or "João.md").
	Target string
	// Heading is the part after '#', e.g. "Playbook" or "^block-id".
	Heading string
	// Alias is the display text after '|'.
	Alias string
	// Embed is true for ![[embeds]].
	Embed bool
	// Relation is "links_to" for body links, the normalised key for inline
	// fields and frontmatter relations (e.g. "works_at", "memory.reads").
	Relation string
	// Line is the 1-based line number in the file (0 for frontmatter links).
	Line int
}

// Note is a parsed note.
type Note struct {
	Path        string
	Frontmatter map[string]any
	// FrontmatterErr is set when the frontmatter block exists but is not
	// valid YAML (the note is still usable: Frontmatter is empty).
	FrontmatterErr error
	// HasFrontmatter reports whether the file starts with a --- block.
	HasFrontmatter bool
	// FrontmatterRaw is the YAML text between the delimiters.
	FrontmatterRaw string
	Body           string
	// BodyLine is the 1-based line number where Body starts.
	BodyLine int

	ID      string
	Type    string
	Title   string
	Aliases []string
	// Tags are de-duplicated, without '#', in order of appearance
	// (frontmatter first).
	Tags  []string
	Links []Link
	// Fields holds inline fields (`Key:: value`) by normalised key.
	Fields map[string][]string
	// Headings in document order (text only).
	Headings []string
	// HeadingLines are the 1-based line numbers and levels of Headings.
	HeadingLines []HeadingPos
	// Tasks are the checkbox items outside code blocks.
	Tasks []Task
}

// HeadingPos locates a heading.
type HeadingPos struct {
	Line  int
	Level int
	Text  string
}

// Parse parses a note. p is the vault-relative path (used for the
// fallback title). Parse never fails; problems are reported in
// FrontmatterErr.
func Parse(p string, src []byte) *Note {
	n := &Note{Path: p, Frontmatter: map[string]any{}, Fields: map[string][]string{}}
	fm, body, bodyLine, ok := SplitFrontmatter(src)
	n.HasFrontmatter = ok
	n.FrontmatterRaw = fm
	n.Body = body
	n.BodyLine = bodyLine
	if ok && strings.TrimSpace(fm) != "" {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(fm), &m); err != nil {
			n.FrontmatterErr = fmt.Errorf("frontmatter: %w", err)
		} else if m != nil {
			n.Frontmatter = Normalize(m).(map[string]any)
			// Keep scalars like ids and titles exactly as written
			// (YAML would turn `id: 0123` into 83 and `title: 1e3` into 1000).
			var raw struct {
				ID    yaml.Node `yaml:"id"`
				Title yaml.Node `yaml:"title"`
			}
			if yaml.Unmarshal([]byte(fm), &raw) == nil {
				if raw.ID.Kind == yaml.ScalarNode {
					n.Frontmatter["id"] = raw.ID.Value
				}
				if raw.Title.Kind == yaml.ScalarNode && raw.Title.Tag != "!!null" {
					n.Frontmatter["title"] = raw.Title.Value
				}
			}
		}
	}
	n.ID = scalarString(n.Frontmatter["id"])
	n.Type = strings.TrimSpace(scalarString(n.Frontmatter["type"]))
	n.Aliases = stringList(n.Frontmatter["aliases"])
	if len(n.Aliases) == 0 {
		n.Aliases = stringList(n.Frontmatter["alias"])
	}
	tags := newOrderedSet()
	for _, t := range stringList(n.Frontmatter["tags"]) {
		for _, f := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			if f = strings.TrimPrefix(f, "#"); f != "" {
				tags.add(f)
			}
		}
	}
	n.Links = append(n.Links, frontmatterLinks(n.Frontmatter)...)

	h1 := n.scanBody(tags)
	n.Tags = tags.items

	switch {
	case scalarString(n.Frontmatter["title"]) != "":
		n.Title = strings.TrimSpace(scalarString(n.Frontmatter["title"]))
	case h1 != "":
		n.Title = h1
	default:
		n.Title = strings.TrimSuffix(path.Base(filepathToSlash(p)), path.Ext(p))
	}
	return n
}

func filepathToSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// SplitFrontmatter splits a note into its YAML frontmatter and body.
// The frontmatter must start on the first line with "---" and end with a
// line "---" (or "..."). bodyLine is the 1-based line number of the body.
func SplitFrontmatter(src []byte) (fm, body string, bodyLine int, ok bool) {
	s := string(bytes.TrimPrefix(src, []byte("\uFEFF")))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return "", s, 1, false
	}
	lines := strings.SplitAfter(s, "\n")
	for i := 1; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], " \t\n")
		if l == "---" || l == "..." {
			fm = strings.Join(lines[1:i], "")
			body = strings.Join(lines[i+1:], "")
			return fm, body, i + 2, true
		}
	}
	// Unterminated: treat the whole file as body.
	return "", s, 1, false
}

var (
	reWikilink   = regexp.MustCompile(`(!?)\[\[([^\[\]\n]+?)\]\]`)
	reInlineCode = regexp.MustCompile("`+[^`\n]*`+")
	reComment    = regexp.MustCompile(`<!--.*?-->`)
	reURL        = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s<>()\[\]]+|www\.[^\s<>()\[\]]+`)
	reMdLink     = regexp.MustCompile(`(!?)\[([^\]\n]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	reTag        = regexp.MustCompile(`(?:^|[\s,;])#([\p{L}\p{N}_/\-]+)`)
	reHeading    = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reField      = regexp.MustCompile(`^\s*(?:>\s*)*(?:[-*+]\s+(?:\[.\]\s+)?|\d+[.)]\s+)?(?:\*\*|__)?([^:\[\]()\n*_#.,;!?][^:\[\]()\n.,;!?]{0,60}?)(?:\*\*|__)?::(?:\s+(.*?)|\s*)$`)
	reBracketFld = regexp.MustCompile(`[\[(]([^:\[\]()\n]+?)::\s*((?:\[\[[^\]\n]*\]\]|[^\]\)\[\n])*?)[\])]`)
	reFence      = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
)

// scanBody extracts headings, links, tags and inline fields. It returns
// the first H1.
func (n *Note) scanBody(tags *orderedSet) (h1 string) {
	lines := strings.Split(n.Body, "\n")
	var fence string
	inComment := false
	for i, raw := range lines {
		lineNo := n.BodyLine + i
		trimmed := strings.TrimSpace(raw)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := reFence.FindStringSubmatch(raw); m != nil {
			fence = m[1]
			continue
		}
		line := raw
		// Multi-line HTML comments.
		if inComment {
			end := strings.Index(line, "-->")
			if end < 0 {
				continue
			}
			line = line[end+3:]
			inComment = false
		}
		line = reComment.ReplaceAllString(line, " ")
		if start := strings.Index(line, "<!--"); start >= 0 {
			line = line[:start]
			inComment = true
		}
		line = reInlineCode.ReplaceAllStringFunc(line, blank)

		if t, ok := ParseTaskLine(raw); ok {
			t.Line = lineNo
			n.Tasks = append(n.Tasks, t)
		}
		if m := reHeading.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[2])
			n.Headings = append(n.Headings, text)
			n.HeadingLines = append(n.HeadingLines, HeadingPos{Line: lineNo, Level: len(m[1]), Text: text})
			if len(m[1]) == 1 && h1 == "" {
				h1 = stripMarkup(text)
			}
		}

		// Inline fields: whole-line `Key:: value` and bracketed [key:: value].
		type span struct {
			from, to int
			rel      string
		}
		var spans []span
		if m := reField.FindStringSubmatchIndex(line); m != nil && !reHeading.MatchString(line) {
			key := strings.TrimSpace(line[m[2]:m[3]])
			val := ""
			if m[4] >= 0 {
				val = strings.TrimSpace(line[m[4]:m[5]])
			}
			if k := NormalizeKey(key); k != "" {
				n.Fields[k] = append(n.Fields[k], val)
				spans = append(spans, span{m[0], m[1], k})
			}
		} else {
			for _, m := range reBracketFld.FindAllStringSubmatchIndex(line, -1) {
				key := strings.TrimSpace(line[m[2]:m[3]])
				val := strings.TrimSpace(line[m[4]:m[5]])
				if k := NormalizeKey(key); k != "" {
					n.Fields[k] = append(n.Fields[k], val)
					spans = append(spans, span{m[0], m[1], k})
				}
			}
		}

		// Wikilinks.
		for _, m := range reWikilink.FindAllStringSubmatchIndex(line, -1) {
			l, ok := ParseLinkText(line[m[4]:m[5]])
			if !ok {
				continue
			}
			l.Embed = m[3] > m[2]
			l.Relation = RelLinksTo
			for _, s := range spans {
				if m[0] >= s.from && m[1] <= s.to {
					l.Relation = s.rel
				}
			}
			l.Line = lineNo
			n.Links = append(n.Links, l)
		}
		noWiki := reWikilink.ReplaceAllStringFunc(line, blank)

		// Markdown links to local notes: [text](other%20note.md#h).
		for _, m := range reMdLink.FindAllStringSubmatch(noWiki, -1) {
			target := m[3]
			if reURL.MatchString(target) || strings.HasPrefix(target, "#") || strings.Contains(target, ":") {
				continue
			}
			target = strings.ReplaceAll(target, "%20", " ")
			heading := ""
			if i := strings.Index(target, "#"); i >= 0 {
				target, heading = target[:i], target[i+1:]
			}
			if !strings.HasSuffix(strings.ToLower(target), ".md") {
				if m[1] == "" {
					continue // only notes; images etc. are attachments
				}
			}
			n.Links = append(n.Links, Link{Target: target, Heading: heading, Alias: m[2], Embed: m[1] != "", Relation: RelLinksTo, Line: lineNo})
		}
		noLinks := reMdLink.ReplaceAllStringFunc(noWiki, blank)
		noLinks = reURL.ReplaceAllStringFunc(noLinks, blank)

		// Tags (a heading's leading '#' is followed by a space, so it never matches).
		for _, m := range reTag.FindAllStringSubmatch(noLinks, -1) {
			t := strings.TrimRight(m[1], "/")
			if t == "" || isAllDigits(t) {
				continue
			}
			tags.add(t)
		}
	}
	return h1
}

func blank(s string) string { return strings.Repeat(" ", len(s)) }

func isAllDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// stripMarkup removes simple emphasis and link markup from heading text.
func stripMarkup(s string) string {
	s = reWikilink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reWikilink.FindStringSubmatch(m)
		l, _ := ParseLinkText(sub[2])
		if l.Alias != "" {
			return l.Alias
		}
		return l.Target
	})
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	return strings.TrimSpace(s)
}

// ParseLinkText parses the inside of [[...]]: "target#heading|alias".
// Escaped pipes ("\|", used inside tables) are accepted. ok is false for
// empty targets (same-note heading links like [[#Heading]]).
func ParseLinkText(s string) (Link, bool) {
	var l Link
	s = strings.ReplaceAll(s, `\|`, "|")
	if i := strings.Index(s, "|"); i >= 0 {
		l.Alias = strings.TrimSpace(s[i+1:])
		s = s[:i]
	}
	if i := strings.Index(s, "#"); i >= 0 {
		l.Heading = strings.TrimSpace(s[i+1:])
		s = s[:i]
	}
	l.Target = strings.TrimSpace(s)
	return l, l.Target != ""
}

// ExtractLinks returns the wikilinks in a string (used for frontmatter values).
func ExtractLinks(s string) []Link {
	var out []Link
	for _, m := range reWikilink.FindAllStringSubmatch(s, -1) {
		if l, ok := ParseLinkText(m[2]); ok {
			l.Embed = m[1] == "!"
			out = append(out, l)
		}
	}
	return out
}

// reservedKeys are frontmatter keys that never produce relations.
var reservedKeys = map[string]bool{
	"id": true, "type": true, "title": true, "aliases": true, "alias": true, "tags": true,
	"created": true, "updated": true, "cssclasses": true,
}

// frontmatterLinks turns frontmatter values containing [[links]] into typed
// relations. Nested maps produce dotted relation names ("memory.reads").
func frontmatterLinks(fm map[string]any) []Link {
	var out []Link
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var walk func(rel string, v any)
	walk = func(rel string, v any) {
		switch t := v.(type) {
		case string:
			for _, l := range ExtractLinks(t) {
				l.Relation = rel
				out = append(out, l)
			}
		case []any:
			for _, e := range t {
				walk(rel, e)
			}
		case map[string]any:
			ks := make([]string, 0, len(t))
			for k := range t {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				walk(rel+"."+NormalizeKey(k), t[k])
			}
		}
	}
	for _, k := range keys {
		if reservedKeys[strings.ToLower(k)] {
			continue
		}
		walk(NormalizeKey(k), fm[k])
	}
	return out
}

// NormalizeKey turns a field name into a relation name: lower case,
// spaces and dashes become underscores ("Preferred brands" → "preferred_brands").
func NormalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range k {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// Normalize converts YAML-decoded values into JSON-compatible ones:
// map[any]any → map[string]any, time.Time → RFC 3339 string, ints → int64/float64 kept.
func Normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = Normalize(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = Normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Normalize(e)
		}
		return out
	case time.Time:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 && t.Location() == time.UTC {
			return t.Format("2006-01-02")
		}
		return t.Format(time.RFC3339)
	default:
		return v
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any, []any:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// StringList accepts a YAML list or a single scalar and returns strings.
func StringList(v any) []string { return stringList(v) }

func stringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, e := range t {
			if s := strings.TrimSpace(scalarString(e)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := strings.TrimSpace(scalarString(t)); s != "" {
			return []string{s}
		}
		return nil
	}
}

type orderedSet struct {
	seen  map[string]bool
	items []string
}

func newOrderedSet() *orderedSet { return &orderedSet{seen: map[string]bool{}} }
func (s *orderedSet) add(v string) {
	k := strings.ToLower(v)
	if !s.seen[k] {
		s.seen[k] = true
		s.items = append(s.items, v)
	}
}
