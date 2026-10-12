package webtools

import (
	"net/url"
	"strings"
	"testing"
)

const articleHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<title>Home solar batteries compared | Energy Weekly</title>
<meta property="og:site_name" content="Energy Weekly">
<meta name="description" content="A comparison of home batteries.">
<meta name="author" content="Ana Costa">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"WebSite","name":"Energy Weekly"},
 {"@type":"NewsArticle","headline":"Home solar batteries compared","datePublished":"2026-09-15T08:30:00Z","author":[{"@type":"Person","name":"Ana Costa"},{"@type":"Person","name":"Rui Melo"}]}]}</script>
<style>.x{color:red}</style>
<script>var tracking = "SECRET_SCRIPT_TEXT";</script>
</head>
<body>
<header class="site-header"><a href="/">Energy Weekly</a> <a href="/subscribe">Subscribe now</a></header>
<nav class="main-nav"><ul><li><a href="/news">News</a></li><li><a href="/reviews">Reviews NAVTEXT</a></li></ul></nav>
<div id="cookie-banner">We use cookies COOKIETEXT. <button>Accept</button></div>
<div class="layout">
 <main>
  <article class="post">
   <h1>Home solar batteries compared</h1>
   <p class="byline">By Ana Costa</p>
   <p>Home batteries store surplus solar energy for the evening, and prices have fallen by about 30% since 2023, according to the <a href="https://example.org/report?utm_source=newsletter">industry report</a>.</p>
   <p>We tested five batteries over three months, measuring usable capacity, round-trip efficiency, noise, and how well each integrates with Home Assistant.</p>
   <h2>Results</h2>
   <p>The best performer kept 92% round-trip efficiency, while the cheapest model, at 4,200 euros, lost almost 15% of the stored energy.</p>
   <ul>
    <li>Battery A: 10 kWh, 92% efficiency</li>
    <li>Battery B: 13.5 kWh, 90% efficiency</li>
   </ul>
   <table><tr><th>Model</th><th>Price</th></tr><tr><td>A</td><td>5,900 €</td></tr></table>
   <blockquote><p>Efficiency matters more than capacity for most homes.</p></blockquote>
   <pre><code>usable = nominal * depth_of_discharge</code></pre>
   <p>See also the <a href="/methodology">methodology</a> and <a href="#top">back to top</a>.</p>
   <div class="share-buttons"><a href="https://twitter.com/share">Share SHARETEXT</a></div>
  </article>
 </main>
 <aside class="sidebar"><h3>Popular</h3><p>SIDEBARTEXT: ten things you did not know about heat pumps, with lots of words to look like a paragraph.</p></aside>
</div>
<div class="related-articles"><h3>Related</h3><p><a href="/x">RELATEDTEXT one</a></p></div>
<footer><p>© 2026 Energy Weekly FOOTERTEXT. All rights reserved, including the rights to many long footer sentences.</p></footer>
</body></html>`

func TestExtractArticle(t *testing.T) {
	base, _ := url.Parse("https://energy.example/articles/batteries")
	a, err := ExtractHTML([]byte(articleHTML), base, 20)
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Home solar batteries compared" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Byline != "Ana Costa, Rui Melo" {
		t.Errorf("byline = %q", a.Byline)
	}
	if a.Published != "2026-09-15T08:30:00Z" {
		t.Errorf("published = %q", a.Published)
	}
	if a.SiteName != "Energy Weekly" || a.Lang != "en" || a.Description == "" {
		t.Errorf("meta = %+v", a)
	}
	for _, want := range []string{
		"Home batteries store surplus solar energy",
		"industry report",
		"We tested five batteries",
		"## Results",
		"- Battery A: 10 kWh, 92% efficiency",
		"| Model | Price |",
		"> Efficiency matters more than capacity",
		"```\nusable = nominal * depth_of_discharge\n```",
	} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("text missing %q\n---\n%s", want, a.Text)
		}
	}
	for _, bad := range []string{"NAVTEXT", "COOKIETEXT", "SIDEBARTEXT", "FOOTERTEXT", "RELATEDTEXT", "SECRET_SCRIPT_TEXT", "SHARETEXT", "color:red", "Subscribe now"} {
		if strings.Contains(a.Text, bad) {
			t.Errorf("text contains boilerplate %q\n---\n%s", bad, a.Text)
		}
	}
	urls := map[string]bool{}
	for _, l := range a.Links {
		urls[l.URL] = true
	}
	if !urls["https://example.org/report"] || !urls["https://energy.example/methodology"] {
		t.Errorf("links = %+v", a.Links)
	}
	if urls["https://energy.example/news"] || urls["https://energy.example/subscribe"] {
		t.Errorf("nav links leaked: %+v", a.Links)
	}
}

// A page without semantic tags: the content block must win by scoring.
const divSoupHTML = `<html><head><title>Recipe: bacalhau à brás</title></head><body>
<div id="top"><div class="menu"><a href="/a">Home</a> | <a href="/b">Recipes</a> | <a href="/c">Contact</a> | <a href="/d">Shop</a></div></div>
<div id="wrap">
 <div class="col-left"><div class="links"><a href="/1">Link one is here</a><br><a href="/2">Link two is here as well</a><br><a href="/3">Link three is also a link</a></div></div>
 <div class="entry-content">
  <div>Bacalhau à brás is a Portuguese dish made from shreds of salted cod, onions and thinly chopped fried potatoes, all bound with scrambled eggs.</div>
  <div>It is usually garnished with black olives and sprinkled with fresh parsley, and it is one of the most popular ways to cook cod in Lisbon.</div>
  <div>Soak the cod for 24 hours, changing the water three times, then shred it by hand, removing skin and bones carefully.</div>
 </div>
</div>
<div class="foot">Copyright, terms, privacy, cookies, contacts, careers, press, advertise with us, newsletter signup.</div>
</body></html>`

func TestExtractDivSoup(t *testing.T) {
	a, err := ExtractHTML([]byte(divSoupHTML), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Text, "salted cod") || !strings.Contains(a.Text, "Soak the cod") {
		t.Errorf("main text missing:\n%s", a.Text)
	}
	if strings.Contains(a.Text, "Link two") || strings.Contains(a.Text, "Copyright") {
		t.Errorf("boilerplate kept:\n%s", a.Text)
	}
	if a.Title != "Recipe: bacalhau à brás" {
		t.Errorf("title = %q", a.Title)
	}
}

func TestExtractShortPageFallsBackToBody(t *testing.T) {
	a, err := ExtractHTML([]byte(`<html><body><h1>Status</h1><span>All systems operational.</span></body></html>`), nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Text, "All systems operational.") || !strings.Contains(a.Text, "# Status") {
		t.Errorf("text = %q", a.Text)
	}
	if a.Title != "Status" {
		t.Errorf("title = %q", a.Title)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := []struct{ title, h1, want string }{
		{"Big news about batteries | Site", "", "Big news about batteries"},
		{"Short | Site", "", "Short | Site"},
		{"Home batteries compared - Energy Weekly", "Home batteries compared", "Home batteries compared"},
		{"", "Only H1", "Only H1"},
	}
	for _, c := range cases {
		if got := cleanTitle(c.title, c.h1); got != c.want {
			t.Errorf("cleanTitle(%q,%q) = %q, want %q", c.title, c.h1, got, c.want)
		}
	}
}

func TestTruncateText(t *testing.T) {
	s := strings.Repeat("para one is here.\n\n", 50)
	out := truncateText(s, 100)
	if !strings.HasSuffix(out, "…[truncated]") || len([]rune(out)) > 100+len("\n\n…[truncated]") {
		t.Errorf("out = %q", out)
	}
	if truncateText("short", 100) != "short" {
		t.Error("short text changed")
	}
	// Multi-byte runes are never split.
	if out := truncateText(strings.Repeat("é", 50), 10); !strings.HasPrefix(out, "éééééééééé") {
		t.Errorf("rune cut: %q", out)
	}
}

func TestPassages(t *testing.T) {
	text := "# Intro\n\nGeneral words about the weather and the sea.\n\n# Prices\n\nThe battery costs 5,900 euros including installation.\n\n# Noise\n\nThe inverter fan is quiet, around 30 dB."
	top, total := RankPassages(text, "How much does the battery cost (price in euros)?", 60, 2)
	if total != 3 {
		t.Errorf("total = %d", total)
	}
	if len(top) == 0 || !strings.Contains(top[0].Text, "5,900 euros") || top[0].Heading != "Prices" {
		t.Errorf("top = %+v", top)
	}
	// Accent folding and plural stemming: "preços" matches "preço".
	top, _ = RankPassages("O preço da bateria é alto.\n\nO tempo está bom.", "preços bateria", 30, 1)
	if len(top) != 1 || !strings.Contains(top[0].Text, "preço") {
		t.Errorf("pt top = %+v", top)
	}
	if top, _ := RankPassages(text, "the of and", 60, 2); len(top) != 0 {
		t.Errorf("stopword-only query matched: %+v", top)
	}
	long := strings.Repeat("Sentence number one is long enough. ", 100)
	for _, c := range splitPassages(long, 300) {
		if len(c.text) > 300 {
			t.Errorf("chunk too long: %d", len(c.text))
		}
	}
}

func TestRobots(t *testing.T) {
	r := parseRobots([]byte(`
User-agent: Googlebot
Disallow: /

User-agent: *
Disallow: /private/
Allow: /private/public
Disallow: /*.pdf$
Disallow: /search?
`), robotsAgent)
	cases := map[string]bool{
		"/":                    true,
		"/private/x":           false,
		"/private/public/page": true,
		"/docs/a.pdf":          false,
		"/docs/a.pdf.html":     true,
		"/search?q=1":          false,
		"/searching":           true,
	}
	for p, want := range cases {
		if got := r.allows(p); got != want {
			t.Errorf("allows(%q) = %v, want %v", p, got, want)
		}
	}
	mine := parseRobots([]byte("User-agent: NexusResearch\nDisallow: /\n\nUser-agent: *\nAllow: /\n"), robotsAgent)
	if mine.allows("/x") {
		t.Error("specific group should apply")
	}
}
