package webtools

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Article is the readable content of a page.
type Article struct {
	Title       string `json:"title"`
	Byline      string `json:"byline,omitempty"`
	Published   string `json:"published,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Description string `json:"description,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Text        string `json:"text"`
	Links       []Link `json:"links"`
}

// Link is an outgoing link of the main content.
type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

var (
	reUnlikely = regexp.MustCompile(`(?i)-ad-|ai2html|banner|breadcrumb|combx|comment|community|cover-wrap|disqus|extra|footer|gdpr|header|legends|menu|related|remark|replies|rss|shoutbox|sidebar|skyscraper|social|sponsor|supplemental|ad-break|agegate|pagination|pager|popup|yom-remote|cookie|consent|newsletter|subscribe|share|promo|outbrain|taboola|modal|signup|navbar|masthead|skip-link|toolbar|widget`)
	reMaybe    = regexp.MustCompile(`(?i)and|article|body|column|content|main|shadow|story|post-body|entry`)
	rePositive = regexp.MustCompile(`(?i)article|body|content|entry|hentry|h-entry|main|page|post|text|blog|story`)
	reNegative = regexp.MustCompile(`(?i)-ad-|hidden|^hid$| hid$| hid |^hid |banner|combx|comment|com-|contact|foot|footer|footnote|gdpr|masthead|media|meta|outbrain|promo|related|scroll|share|shoutbox|sidebar|skyscraper|sponsor|shopping|tags|tool|widget|cookie|consent|newsletter`)
	reHidden   = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden`)
	reSpaces   = regexp.MustCompile(`[ \t\f\r\x{a0}]+`)
	reNewlines = regexp.MustCompile(`\n{3,}`)
)

// Tags dropped wholesale before scoring.
var dropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Svg: true,
	atom.Canvas: true, atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Button: true,
	atom.Input: true, atom.Select: true, atom.Textarea: true, atom.Nav: true, atom.Footer: true,
	atom.Aside: true, atom.Dialog: true, atom.Link: true, atom.Meta: true, atom.Map: true,
	atom.Audio: true, atom.Video: true, atom.Picture: true, atom.Source: true, atom.Menu: true,
}

var dropRoles = map[string]bool{"navigation": true, "banner": true, "contentinfo": true, "complementary": true,
	"dialog": true, "alertdialog": true, "menu": true, "menubar": true, "search": true, "toolbar": true}

var blockTags = map[atom.Atom]bool{
	atom.Address: true, atom.Article: true, atom.Blockquote: true, atom.Details: true, atom.Div: true,
	atom.Dl: true, atom.Fieldset: true, atom.Figure: true, atom.Footer: true, atom.Form: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Header: true, atom.Hr: true, atom.Li: true, atom.Main: true, atom.Ol: true, atom.P: true,
	atom.Pre: true, atom.Section: true, atom.Table: true, atom.Ul: true,
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// textOf returns the concatenated text of n with collapsed whitespace.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		if n.Type == html.ElementNode && (n.DataAtom == atom.Script || n.DataAtom == atom.Style) {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return collapseSpace(b.String())
}

func findFirst(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, a); f != nil {
			return f
		}
	}
	return nil
}

func findAll(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func hasAncestor(n *html.Node, tags ...atom.Atom) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		for _, t := range tags {
			if p.DataAtom == t {
				return true
			}
		}
	}
	return false
}

// ExtractHTML parses an HTML document and returns its readable content.
// base resolves relative links (nil: links stay as written).
func ExtractHTML(src []byte, base *url.URL, maxLinks int) (*Article, error) {
	doc, err := html.Parse(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	if b := findFirst(doc, atom.Base); b != nil && base != nil {
		if h := attr(b, "href"); h != "" {
			if u, err := base.Parse(h); err == nil {
				base = u
			}
		}
	}
	a := &Article{}
	readMeta(doc, a)
	h1 := ""
	if n := findFirst(doc, atom.H1); n != nil {
		h1 = textOf(n)
	}
	prune(doc)
	body := findFirst(doc, atom.Body)
	if body == nil {
		body = doc
	}
	nodes := pickContent(body)
	text := renderNodes(nodes)
	if utf8.RuneCountInString(text) < 250 {
		if full := renderNodes([]*html.Node{body}); utf8.RuneCountInString(full) > utf8.RuneCountInString(text) {
			text, nodes = full, []*html.Node{body}
		}
	}
	a.Text = text
	a.Title = cleanTitle(a.Title, h1)
	a.Links = collectLinks(nodes, base, maxLinks)
	if len(a.Links) < maxLinks && len(nodes) == 1 && nodes[0] != body && len(a.Links) == 0 {
		a.Links = collectLinks([]*html.Node{body}, base, maxLinks)
	}
	return a, nil
}

// cleanTitle prefers the page's h1 when the <title> is "Headline | Site".
func cleanTitle(title, h1 string) string {
	title, h1 = collapseSpace(title), collapseSpace(h1)
	if title == "" {
		return h1
	}
	if h1 != "" && h1 != title && strings.Contains(title, h1) && len(h1) >= 10 {
		return h1
	}
	for _, sep := range []string{" | ", " – ", " — ", " :: ", " - "} {
		if i := strings.LastIndex(title, sep); i > 0 {
			head := strings.TrimSpace(title[:i])
			if len(strings.Fields(head)) >= 3 && len(title)-i < 40 {
				return head
			}
		}
	}
	return title
}

// ---------------------------------------------------------------- metadata

func readMeta(doc *html.Node, a *Article) {
	if h := findFirst(doc, atom.Html); h != nil {
		a.Lang = attr(h, "lang")
	}
	var docTitle, ogTitle, ldHeadline, ldPub, ldAuthor, metaPub, metaAuthor string
	if t := findFirst(doc, atom.Title); t != nil {
		docTitle = textOf(t)
	}
	for _, m := range findAll(doc, func(n *html.Node) bool { return n.DataAtom == atom.Meta }) {
		key := strings.ToLower(firstNonEmpty(attr(m, "property"), attr(m, "name"), attr(m, "itemprop")))
		val := strings.TrimSpace(attr(m, "content"))
		if val == "" {
			continue
		}
		switch key {
		case "og:title", "twitter:title":
			if ogTitle == "" {
				ogTitle = val
			}
		case "og:site_name", "application-name":
			if a.SiteName == "" {
				a.SiteName = val
			}
		case "description", "og:description", "twitter:description":
			if a.Description == "" {
				a.Description = val
			}
		case "author", "article:author", "dc.creator", "byl", "parsely-author", "sailthru.author":
			if metaAuthor == "" && !strings.HasPrefix(val, "http") {
				metaAuthor = strings.TrimPrefix(val, "By ")
			}
		case "article:published_time", "og:article:published_time", "datepublished", "pubdate", "publishdate",
			"date", "dc.date", "dc.date.issued", "sailthru.date", "parsely-pub-date", "publication_date":
			if metaPub == "" {
				metaPub = val
			}
		}
	}
	for _, s := range findAll(doc, func(n *html.Node) bool {
		return n.DataAtom == atom.Script && strings.Contains(strings.ToLower(attr(n, "type")), "ld+json")
	}) {
		if s.FirstChild == nil {
			continue
		}
		var v any
		if json.Unmarshal([]byte(s.FirstChild.Data), &v) != nil {
			continue
		}
		if obj := findLDArticle(v); obj != nil {
			if ldHeadline == "" {
				ldHeadline, _ = obj["headline"].(string)
			}
			if ldPub == "" {
				ldPub, _ = obj["datePublished"].(string)
			}
			if ldAuthor == "" {
				ldAuthor = ldName(obj["author"])
			}
		}
	}
	if metaPub == "" {
		for _, t := range findAll(doc, func(n *html.Node) bool {
			return (n.DataAtom == atom.Time && attr(n, "datetime") != "") || attr(n, "itemprop") == "datePublished"
		}) {
			metaPub = firstNonEmpty(attr(t, "datetime"), attr(t, "content"), textOf(t))
			break
		}
	}
	if metaAuthor == "" && ldAuthor == "" {
		for _, n := range findAll(doc, func(n *html.Node) bool {
			cls := strings.ToLower(attr(n, "class") + " " + attr(n, "rel") + " " + attr(n, "itemprop"))
			return strings.Contains(cls, "byline") || strings.Contains(cls, "author")
		}) {
			if t := textOf(n); t != "" && len(t) < 100 {
				metaAuthor = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "By "), "by "))
				break
			}
		}
	}
	a.Title = firstNonEmpty(ogTitle, ldHeadline, docTitle)
	a.Byline = firstNonEmpty(ldAuthor, metaAuthor)
	pub := firstNonEmpty(ldPub, metaPub)
	if d := normDate(pub); d != "" {
		a.Published = d
	} else {
		a.Published = pub
	}
}

var ldArticleTypes = map[string]bool{"Article": true, "NewsArticle": true, "BlogPosting": true, "Report": true,
	"ScholarlyArticle": true, "TechArticle": true, "AnalysisNewsArticle": true, "ReportageNewsArticle": true,
	"Review": true, "WebPage": true, "Product": false}

func findLDArticle(v any) map[string]any {
	switch t := v.(type) {
	case []any:
		var page map[string]any
		for _, x := range t {
			if o := findLDArticle(x); o != nil {
				if ldType(o) != "WebPage" {
					return o
				}
				page = o
			}
		}
		return page
	case map[string]any:
		if g, ok := t["@graph"]; ok {
			if o := findLDArticle(g); o != nil {
				return o
			}
		}
		if ldArticleTypes[ldType(t)] {
			return t
		}
	}
	return nil
}

func ldType(o map[string]any) string {
	switch t := o["@type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && ldArticleTypes[s] {
				return s
			}
		}
	}
	return ""
}

func ldName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		s, _ := t["name"].(string)
		return s
	case []any:
		var names []string
		for _, x := range t {
			if n := ldName(x); n != "" {
				names = append(names, n)
			}
		}
		return strings.Join(names, ", ")
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------- pruning

func prune(doc *html.Node) {
	var drop []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.CommentNode {
			drop = append(drop, n)
			return
		}
		if n.Type == html.ElementNode && shouldDrop(n) {
			drop = append(drop, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	for _, n := range drop {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
}

func shouldDrop(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Html, atom.Body, atom.Article, atom.Main:
		return false
	}
	if dropTags[n.DataAtom] {
		return true
	}
	if n.DataAtom == atom.Header && !hasAncestor(n, atom.Article, atom.Main) {
		return true
	}
	if n.DataAtom == atom.Form && len(textOf(n)) < 300 {
		return true
	}
	if dropRoles[strings.ToLower(attr(n, "role"))] || hasAttr(n, "hidden") ||
		attr(n, "aria-hidden") == "true" || reHidden.MatchString(attr(n, "style")) {
		return true
	}
	if n.DataAtom == atom.A || n.DataAtom == atom.Pre || n.DataAtom == atom.Code || n.DataAtom == atom.Table ||
		n.DataAtom == atom.Tbody || n.DataAtom == atom.Tr || n.DataAtom == atom.Td || hasAncestor(n, atom.Pre, atom.Table) {
		return false
	}
	ci := attr(n, "class") + " " + attr(n, "id")
	if strings.TrimSpace(ci) != "" && reUnlikely.MatchString(ci) && !reMaybe.MatchString(ci) {
		return true
	}
	return false
}

// ---------------------------------------------------------------- scoring

func classWeight(n *html.Node) float64 {
	w := 0.0
	for _, s := range []string{attr(n, "class"), attr(n, "id")} {
		if s == "" {
			continue
		}
		if reNegative.MatchString(s) {
			w -= 25
		}
		if rePositive.MatchString(s) {
			w += 25
		}
	}
	return w
}

func initialScore(n *html.Node) float64 {
	s := classWeight(n)
	switch n.DataAtom {
	case atom.Article:
		s += 15
	case atom.Main:
		s += 8
	case atom.Div, atom.Section:
		s += 5
	case atom.Pre, atom.Td, atom.Blockquote:
		s += 3
	case atom.Address, atom.Ol, atom.Ul, atom.Dl, atom.Dd, atom.Dt, atom.Li, atom.Form:
		s -= 3
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Th:
		s -= 5
	}
	return s
}

func linkDensity(n *html.Node) float64 {
	total := len(textOf(n))
	if total == 0 {
		return 0
	}
	links := 0
	for _, a := range findAll(n, func(x *html.Node) bool { return x.DataAtom == atom.A }) {
		links += len(textOf(a))
	}
	return float64(links) / float64(total)
}

func hasBlockChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && blockTags[c.DataAtom] {
			return true
		}
	}
	return false
}

// pickContent returns the node(s) holding the main content (Readability's
// scoring: paragraphs vote for their ancestors, scaled by link density;
// related siblings of the winner are kept).
func pickContent(body *html.Node) []*html.Node {
	scores := map[*html.Node]float64{}
	var order []*html.Node
	touch := func(n *html.Node) {
		if _, ok := scores[n]; !ok {
			scores[n] = initialScore(n)
			order = append(order, n)
		}
	}
	paras := findAll(body, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.P, atom.Pre, atom.Td, atom.Blockquote:
			return true
		case atom.Div, atom.Section:
			return !hasBlockChild(n)
		}
		return false
	})
	for _, p := range paras {
		t := textOf(p)
		if len(t) < 25 {
			continue
		}
		score := 1 + float64(strings.Count(t, ",")+strings.Count(t, "，")) + math.Min(float64(len(t))/100, 3)
		level := 0
		for anc := p.Parent; anc != nil && anc.Type == html.ElementNode && level < 3; anc = anc.Parent {
			touch(anc)
			div := 1.0
			switch level {
			case 0:
			case 1:
				div = 2
			default:
				div = float64(level * 3)
			}
			scores[anc] += score / div
			level++
		}
	}
	var top *html.Node
	best := math.Inf(-1)
	for _, n := range order {
		s := scores[n] * (1 - linkDensity(n))
		scores[n] = s
		if s > best {
			best, top = s, n
		}
	}
	if top == nil {
		return []*html.Node{body}
	}
	// A winner that holds only a fraction of its parent's paragraphs is often
	// one block of an article split across siblings: walk up while the parent
	// scores close to it.
	for p := top.Parent; p != nil && p != body && p.Type == html.ElementNode; p = p.Parent {
		if ps, ok := scores[p]; ok && ps >= best*0.85 && p.DataAtom != atom.Body {
			top, best = p, math.Max(best, ps)
			continue
		}
		break
	}
	parent := top.Parent
	if parent == nil {
		return []*html.Node{top}
	}
	threshold := math.Max(10, best*0.2)
	var out []*html.Node
	for s := parent.FirstChild; s != nil; s = s.NextSibling {
		if s.Type != html.ElementNode {
			continue
		}
		if s == top {
			out = append(out, s)
			continue
		}
		bonus := 0.0
		if c := attr(top, "class"); c != "" && attr(s, "class") == c {
			bonus = best * 0.2
		}
		if sc, ok := scores[s]; ok && sc+bonus >= threshold {
			out = append(out, s)
			continue
		}
		if s.DataAtom == atom.P {
			t := textOf(s)
			ld := linkDensity(s)
			if (len(t) > 80 && ld < 0.25) || (len(t) > 0 && len(t) <= 80 && ld == 0 && strings.ContainsAny(t, ".!?")) {
				out = append(out, s)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- markdown

type mdWriter struct {
	b     strings.Builder
	soft  int // >0 inside list items: block breaks are single newlines
	inPre int
}

func (m *mdWriter) last() byte {
	s := m.b.String()
	if s == "" {
		return '\n'
	}
	return s[len(s)-1]
}

func (m *mdWriter) newline() {
	if m.b.Len() > 0 && m.last() != '\n' {
		m.b.WriteByte('\n')
	}
}

func (m *mdWriter) block() {
	if m.b.Len() == 0 {
		return
	}
	if m.soft > 0 {
		m.newline()
		return
	}
	s := m.b.String()
	switch {
	case strings.HasSuffix(s, "\n\n"):
	case strings.HasSuffix(s, "\n"):
		m.b.WriteByte('\n')
	default:
		m.b.WriteString("\n\n")
	}
}

func (m *mdWriter) text(s string) {
	if m.inPre > 0 {
		m.b.WriteString(s)
		return
	}
	s = reSpaces.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " ")
	if s == "" {
		return
	}
	l := m.last()
	if l == '\n' || l == ' ' {
		s = strings.TrimLeft(s, " ")
	}
	m.b.WriteString(s)
}

func renderNodes(nodes []*html.Node) string {
	m := &mdWriter{}
	for _, n := range nodes {
		m.node(n, 0)
		m.block()
	}
	out := m.b.String()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out = reNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(out)
}

func (m *mdWriter) children(n *html.Node, depth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		m.node(c, depth)
	}
}

func (m *mdWriter) node(n *html.Node, depth int) {
	switch n.Type {
	case html.TextNode:
		m.text(n.Data)
		return
	case html.DocumentNode:
		m.children(n, depth)
		return
	case html.ElementNode:
	default:
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		t := textOf(n)
		if t == "" {
			return
		}
		m.block()
		level := int(n.Data[1] - '0')
		m.b.WriteString(strings.Repeat("#", level) + " " + t)
		m.block()
	case atom.Br:
		m.b.WriteByte('\n')
	case atom.Hr:
		m.block()
		m.b.WriteString("---")
		m.block()
	case atom.Img:
		// Images are dropped; alt text of figures is usually repeated in figcaption.
	case atom.Ul, atom.Ol:
		m.block()
		i := 0
		if s := attr(n, "start"); s != "" {
			for _, r := range s {
				if r >= '0' && r <= '9' {
					i = i*10 + int(r-'0')
				}
			}
			i--
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.DataAtom != atom.Li {
				continue
			}
			if textOf(c) == "" {
				continue
			}
			i++
			m.newline()
			m.b.WriteString(strings.Repeat("  ", depth))
			if n.DataAtom == atom.Ol {
				m.b.WriteString(strconv.Itoa(i) + ". ")
			} else {
				m.b.WriteString("- ")
			}
			m.soft++
			m.children(c, depth+1)
			m.soft--
		}
		m.block()
	case atom.Pre:
		m.block()
		m.b.WriteString("```\n")
		m.inPre++
		m.children(n, depth)
		m.inPre--
		m.newline()
		m.b.WriteString("```")
		m.block()
	case atom.Code:
		if m.inPre > 0 {
			m.children(n, depth)
			return
		}
		if t := textOf(n); t != "" {
			if l := m.last(); l != ' ' && l != '\n' && l != '(' {
				m.b.WriteByte(' ')
			}
			m.b.WriteString("`" + t + "`")
		}
	case atom.Blockquote:
		sub := &mdWriter{}
		sub.children(n, 0)
		q := strings.TrimSpace(reNewlines.ReplaceAllString(sub.b.String(), "\n\n"))
		if q == "" {
			return
		}
		m.block()
		for i, l := range strings.Split(q, "\n") {
			if i > 0 {
				m.b.WriteByte('\n')
			}
			m.b.WriteString(strings.TrimRight("> "+l, " "))
		}
		m.block()
	case atom.Table:
		m.table(n)
	case atom.Td, atom.Th:
		m.text(" ")
		m.children(n, depth)
		m.text(" ")
	default:
		if blockTags[n.DataAtom] || n.DataAtom == atom.Figcaption || n.DataAtom == atom.Dd || n.DataAtom == atom.Dt ||
			n.DataAtom == atom.Summary || n.DataAtom == atom.Tr || n.DataAtom == atom.Caption {
			m.block()
			m.children(n, depth)
			m.block()
			return
		}
		m.children(n, depth)
	}
}

func (m *mdWriter) table(n *html.Node) {
	rows := findAll(n, func(x *html.Node) bool { return x.DataAtom == atom.Tr })
	if len(rows) == 0 {
		m.block()
		m.children(n, 0)
		m.block()
		return
	}
	m.block()
	for i, r := range rows {
		var cells []string
		header := false
		for c := r.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
				cells = append(cells, strings.ReplaceAll(textOf(c), "|", "/"))
				header = header || c.DataAtom == atom.Th
			}
		}
		if len(cells) == 0 {
			continue
		}
		m.newline()
		m.b.WriteString("| " + strings.Join(cells, " | ") + " |")
		if i == 0 && header {
			m.b.WriteString("\n|" + strings.Repeat(" --- |", len(cells)))
		}
	}
	m.block()
}

// ---------------------------------------------------------------- links

func collectLinks(nodes []*html.Node, base *url.URL, max int) []Link {
	out := []Link{}
	seen := map[string]bool{}
	pageKey := ""
	if base != nil {
		pageKey = dedupKey(base.String())
	}
	for _, root := range nodes {
		for _, a := range findAll(root, func(n *html.Node) bool { return n.DataAtom == atom.A && attr(n, "href") != "" }) {
			if len(out) >= max {
				return out
			}
			href := strings.TrimSpace(attr(a, "href"))
			if strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") ||
				strings.HasPrefix(strings.ToLower(href), "mailto:") {
				continue
			}
			u, err := url.Parse(href)
			if err != nil {
				continue
			}
			if base != nil {
				u = base.ResolveReference(u)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				continue
			}
			clean := CleanURL(u.String())
			k := dedupKey(clean)
			if seen[k] || k == pageKey {
				continue
			}
			seen[k] = true
			t := textOf(a)
			if t == "" {
				t = attr(a, "title")
			}
			if len(t) > 120 {
				t = t[:120]
				for !utf8.ValidString(t) {
					t = t[:len(t)-1]
				}
				t += "…"
			}
			out = append(out, Link{Text: t, URL: clean})
		}
	}
	return out
}
