package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// SearchRequest is the web.search input.
type SearchRequest struct {
	Query     string `json:"query"`
	Count     int    `json:"count,omitempty"`
	Freshness string `json:"freshness,omitempty"` // day | week | month | year
	Site      string `json:"site,omitempty"`
	Lang      string `json:"lang,omitempty"`
}

// SearchResult is one hit.
type SearchResult struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet"`
	Published string `json:"published,omitempty"`
	Source    string `json:"source,omitempty"` // engine / provider that found it
}

// SearchResponse is the web.search output.
type SearchResponse struct {
	Query    string         `json:"query"`
	Provider string         `json:"provider"`
	Results  []SearchResult `json:"results"`
	// Errors lists providers that failed before one answered.
	Errors []string `json:"errors,omitempty"`
}

// Provider is a search backend.
type Provider interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]SearchResult, error)
}

// ErrNoProviders is returned when no provider is configured.
var ErrNoProviders = errors.New("web.search: no search provider configured (set tools.web search.provider)")

// Searcher runs providers in fallback order.
type Searcher struct {
	Providers []Provider
	Default   int
	Max       int
}

// Search queries providers in order until one returns results.
func (s *Searcher) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return nil, errors.New("web.search: query required")
	}
	if req.Count <= 0 {
		req.Count = s.Default
	}
	if s.Max > 0 && req.Count > s.Max {
		req.Count = s.Max
	}
	req.Freshness = normFreshness(req.Freshness)
	req.Site = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(req.Site, "https://"), "http://"))
	req.Site = strings.TrimSuffix(req.Site, "/")
	if len(s.Providers) == 0 {
		return nil, ErrNoProviders
	}
	resp := &SearchResponse{Query: req.Query, Results: []SearchResult{}}
	for _, p := range s.Providers {
		if ctx.Err() != nil {
			break
		}
		res, err := p.Search(ctx, req)
		if err != nil {
			resp.Errors = append(resp.Errors, p.Name()+": "+err.Error())
			continue
		}
		res = dedupResults(res, req.Site)
		if len(res) == 0 {
			resp.Errors = append(resp.Errors, p.Name()+": no results")
			continue
		}
		if len(res) > req.Count {
			res = res[:req.Count]
		}
		resp.Provider, resp.Results = p.Name(), res
		return resp, nil
	}
	if len(resp.Errors) > 0 && allNoResults(resp.Errors) {
		return resp, nil // a legitimate empty answer
	}
	return nil, fmt.Errorf("web.search: every provider failed: %s", strings.Join(resp.Errors, "; "))
}

func allNoResults(errs []string) bool {
	for _, e := range errs {
		if !strings.HasSuffix(e, ": no results") {
			return false
		}
	}
	return true
}

func normFreshness(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "day", "d", "24h", "pd":
		return "day"
	case "week", "w", "7d", "pw":
		return "week"
	case "month", "m", "30d", "pm":
		return "month"
	case "year", "y", "365d", "py":
		return "year"
	}
	return ""
}

// dedupResults cleans URLs, drops duplicates, empty and non-http entries,
// and (when site is set) results outside the site.
func dedupResults(in []SearchResult, site string) []SearchResult {
	seen := map[string]bool{}
	out := make([]SearchResult, 0, len(in))
	for _, r := range in {
		r.URL = CleanURL(r.URL)
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		if site != "" && !hostInSite(u.Hostname(), site) {
			continue
		}
		k := dedupKey(r.URL)
		if seen[k] {
			continue
		}
		seen[k] = true
		r.Title = collapseSpace(r.Title)
		r.Snippet = collapseSpace(r.Snippet)
		if r.Title == "" {
			r.Title = u.Host + u.Path
		}
		out = append(out, r)
	}
	return out
}

func hostInSite(host, site string) bool {
	site = strings.ToLower(site)
	if i := strings.IndexByte(site, '/'); i >= 0 {
		site = site[:i] // path filters are left to the engine
	}
	host = strings.ToLower(host)
	return host == site || strings.HasSuffix(host, "."+site) ||
		strings.TrimPrefix(host, "www.") == strings.TrimPrefix(site, "www.")
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func queryWithSite(req SearchRequest) string {
	if req.Site == "" || strings.Contains(req.Query, "site:") {
		return req.Query
	}
	return req.Query + " site:" + req.Site
}

// ---------------------------------------------------------------- HTTP helper

type apiClient struct {
	http *http.Client
	ua   string
}

func newAPIClient(timeout time.Duration, useProxy bool) *apiClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if !useProxy {
		tr.Proxy = nil
	}
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &apiClient{http: &http.Client{Transport: tr, Timeout: timeout}, ua: DefaultUserAgent}
}

// do sends req and returns the body (capped at 4 MiB) for 2xx responses.
func (c *apiClient) do(req *http.Request) ([]byte, error) {
	b, _, err := c.doStatus(req)
	return b, err
}

func (c *apiClient) doStatus(req *http.Request) ([]byte, int, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return b, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return b, resp.StatusCode, nil
}

// ---------------------------------------------------------------- SearXNG

// SearXNG queries a SearXNG instance's JSON API (format=json must be enabled
// in its settings.yml: search.formats: [html, json]).
type SearXNG struct {
	cfg SearXNGConfig
	c   *apiClient
}

func (p *SearXNG) Name() string { return "searxng" }

func (p *SearXNG) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if p.cfg.BaseURL == "" {
		return nil, errors.New("base_url not configured")
	}
	q := url.Values{"q": {queryWithSite(req)}, "format": {"json"}, "pageno": {"1"}}
	if req.Freshness != "" {
		q.Set("time_range", req.Freshness)
	}
	if req.Lang != "" {
		q.Set("language", req.Lang)
	}
	if p.cfg.Engines != "" {
		q.Set("engines", p.cfg.Engines)
	}
	if p.cfg.Categories != "" {
		q.Set("categories", p.cfg.Categories)
	}
	if p.cfg.SafeSearch != nil {
		q.Set("safesearch", strconv.Itoa(*p.cfg.SafeSearch))
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.cfg.BaseURL, "/")+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	for k, v := range p.cfg.Headers {
		r.Header.Set(k, os.ExpandEnv(v))
	}
	b, err := p.c.do(r)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 403") {
			return nil, fmt.Errorf("%w (is the json format enabled in SearXNG settings.yml search.formats?)", err)
		}
		return nil, err
	}
	var out struct {
		Results []struct {
			URL           string   `json:"url"`
			Title         string   `json:"title"`
			Content       string   `json:"content"`
			PublishedDate *string  `json:"publishedDate"`
			Engine        string   `json:"engine"`
			Engines       []string `json:"engines"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	res := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		sr := SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Source: "searxng"}
		if r.Engine != "" {
			sr.Source = "searxng/" + r.Engine
		}
		if r.PublishedDate != nil {
			sr.Published = normDate(*r.PublishedDate)
		}
		res = append(res, sr)
	}
	return res, nil
}

// ---------------------------------------------------------------- Brave

// Brave uses the Brave Search API (https://api.search.brave.com).
type Brave struct {
	cfg APIKeyConfig
	c   *apiClient
}

func (p *Brave) Name() string { return "brave" }

func (p *Brave) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	key := p.cfg.key()
	if key == "" {
		return nil, fmt.Errorf("API key not set (env %s)", p.cfg.APIKeyEnv)
	}
	base := p.cfg.BaseURL
	if base == "" {
		base = "https://api.search.brave.com/res/v1/web/search"
	}
	q := url.Values{"q": {queryWithSite(req)}, "count": {strconv.Itoa(min(req.Count, 20))}}
	if f := map[string]string{"day": "pd", "week": "pw", "month": "pm", "year": "py"}[req.Freshness]; f != "" {
		q.Set("freshness", f)
	}
	if req.Lang != "" {
		q.Set("search_lang", strings.ToLower(strings.SplitN(req.Lang, "-", 2)[0]))
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-Subscription-Token", key)
	b, err := p.c.do(r)
	if err != nil {
		return nil, err
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	res := make([]SearchResult, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		pub := normDate(r.PageAge)
		if pub == "" {
			pub = r.Age
		}
		res = append(res, SearchResult{Title: r.Title, URL: r.URL, Snippet: stripTags(r.Description), Published: pub, Source: "brave"})
	}
	return res, nil
}

// ---------------------------------------------------------------- Tavily

// Tavily uses the Tavily search API (https://api.tavily.com/search).
type Tavily struct {
	cfg APIKeyConfig
	c   *apiClient
}

func (p *Tavily) Name() string { return "tavily" }

func (p *Tavily) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	key := p.cfg.key()
	if key == "" {
		return nil, fmt.Errorf("API key not set (env %s)", p.cfg.APIKeyEnv)
	}
	base := p.cfg.BaseURL
	if base == "" {
		base = "https://api.tavily.com/search"
	}
	body := map[string]any{"query": req.Query, "max_results": min(req.Count, 20), "search_depth": "basic"}
	if req.Freshness != "" {
		body["time_range"] = req.Freshness
	}
	if req.Site != "" {
		body["include_domains"] = []string{req.Site}
	}
	bb, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(bb))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	b, err := p.c.do(r)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	res := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		res = append(res, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Published: normDate(r.PublishedDate), Source: "tavily"})
	}
	return res, nil
}

// ---------------------------------------------------------------- DuckDuckGo

// DuckDuckGo scrapes the keyless HTML endpoint. It is a best-effort
// fallback: DuckDuckGo changes markup and rate-limits automated clients
// (answering with a CAPTCHA page), so it can stop working at any time.
type DuckDuckGo struct {
	cfg  DuckDuckGoConfig
	c    *apiClient
	mu   sync.Mutex
	last time.Time
}

func (p *DuckDuckGo) Name() string { return "duckduckgo" }

// ErrDDGBlocked means DuckDuckGo answered with a bot challenge.
var ErrDDGBlocked = errors.New("duckduckgo refused the request (bot challenge / rate limit); configure SearXNG for reliable search")

func (p *DuckDuckGo) wait(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := time.Duration(p.cfg.MinInterval) - time.Since(p.last); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	p.last = time.Now()
	return nil
}

func ddgRegion(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" {
		return ""
	}
	if a, b, ok := strings.Cut(l, "-"); ok { // pt-PT → pt-pt ; en-US → us-en
		if a == "en" {
			return b + "-en"
		}
		return b + "-" + a
	}
	m := map[string]string{"pt": "pt-pt", "en": "us-en", "es": "es-es", "fr": "fr-fr", "de": "de-de",
		"it": "it-it", "nl": "nl-nl", "br": "br-pt", "uk": "uk-en"}
	return m[l]
}

func (p *DuckDuckGo) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if p.cfg.Disabled {
		return nil, errors.New("disabled")
	}
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	base := p.cfg.BaseURL
	if base == "" {
		base = "https://html.duckduckgo.com/html/"
	}
	form := url.Values{"q": {queryWithSite(req)}, "b": {""}}
	if kl := ddgRegion(req.Lang); kl != "" {
		form.Set("kl", kl)
	}
	if f := map[string]string{"day": "d", "week": "w", "month": "m", "year": "y"}[req.Freshness]; f != "" {
		form.Set("df", f)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "text/html")
	// The HTML endpoint serves browsers; a browser-like UA avoids an
	// immediate challenge (it is still a scraper, see the type doc).
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0")
	b, status, err := p.c.doStatus(r)
	switch status {
	case http.StatusAccepted, http.StatusForbidden, http.StatusTooManyRequests:
		return nil, ErrDDGBlocked // DDG answers 202 + challenge when rate-limiting
	}
	if err != nil {
		return nil, err
	}
	return ParseDDGHTML(b)
}

// ParseDDGHTML extracts results from html.duckduckgo.com markup.
func ParseDDGHTML(b []byte) ([]SearchResult, error) {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var res []SearchResult
	sawResultsPage, blocked := false, false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if blocked {
			return
		}
		if n.Type == html.ElementNode {
			cls := " " + attr(n, "class") + " "
			if strings.Contains(cls, " anomaly-modal") || attr(n, "id") == "challenge-form" ||
				(n.DataAtom == atom.Form && strings.Contains(attr(n, "action"), "anomaly")) {
				blocked = true
				return
			}
			if attr(n, "id") == "links" || strings.Contains(cls, " results ") || strings.Contains(cls, " no-results ") {
				sawResultsPage = true
			}
			if n.DataAtom == atom.Div && strings.Contains(cls, " result ") && !strings.Contains(cls, " result--ad") {
				if r, ok := ddgResult(n); ok {
					res = append(res, r)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if blocked {
		return nil, ErrDDGBlocked
	}
	if len(res) == 0 && !sawResultsPage {
		return nil, errors.New("unrecognised DuckDuckGo HTML (markup changed?)")
	}
	return res, nil
}

func ddgResult(n *html.Node) (SearchResult, bool) {
	var r SearchResult
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			cls := " " + attr(n, "class") + " "
			switch {
			case n.DataAtom == atom.A && strings.Contains(cls, " result__a ") && r.URL == "":
				r.URL = ddgUnwrap(attr(n, "href"))
				r.Title = textOf(n)
				return
			case strings.Contains(cls, " result__snippet ") && r.Snippet == "":
				r.Snippet = textOf(n)
				if r.URL == "" && n.DataAtom == atom.A {
					r.URL = ddgUnwrap(attr(n, "href"))
				}
				return
			case strings.Contains(cls, " result__timestamp ") || strings.Contains(cls, " result__extras__date "):
				r.Published = collapseSpace(textOf(n))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	r.Source = "duckduckgo"
	return r, r.URL != ""
}

// ddgUnwrap resolves //duckduckgo.com/l/?uddg=<target> redirect links.
func ddgUnwrap(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if t := u.Query().Get("uddg"); t != "" {
			return t
		}
		if strings.HasPrefix(u.Path, "/y.js") { // ad click-through
			return ""
		}
	}
	return u.String()
}

// ---------------------------------------------------------------- helpers

// normDate returns YYYY-MM-DD (or the date-time) for common formats, or "".
func normDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05",
		"2006-01-02", time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2 January 2006",
		"January 2, 2006", "Jan 2, 2006", "2006/01/02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
				return t.Format("2006-01-02")
			}
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func stripTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return html.UnescapeString(s)
	}
	n, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	return collapseSpace(textOf(n))
}
