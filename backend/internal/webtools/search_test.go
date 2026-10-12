package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testAPI() *apiClient { return newAPIClient(5*time.Second, false) }

func TestSearXNG(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"query":"go","results":[
{"url":"https://go.dev/doc/?utm_source=x&lang=en#top","title":"Go docs","content":"The Go  programming\nlanguage","engine":"bing","publishedDate":"2026-09-01T00:00:00"},
{"url":"https://www.go.dev/doc/?lang=en&utm_campaign=z","title":"dup","content":"duplicate","engine":"google","publishedDate":null},
{"url":"https://example.org/a?fbclid=123","title":"","content":"no title","engine":"google"},
{"url":"javascript:alert(1)","title":"bad","content":""}
]}`)
	}))
	defer srv.Close()
	p := &SearXNG{cfg: SearXNGConfig{BaseURL: srv.URL + "/", Headers: map[string]string{"X-Token": "abc"}}, c: testAPI()}
	s := &Searcher{Providers: []Provider{p}, Default: 8, Max: 20}
	res, err := s.Search(context.Background(), SearchRequest{Query: "golang", Freshness: "week", Lang: "pt", Site: "go.dev"})
	if err != nil {
		t.Fatal(err)
	}
	q := got.URL.Query()
	if q.Get("format") != "json" || q.Get("time_range") != "week" || q.Get("language") != "pt" || q.Get("q") != "golang site:go.dev" {
		t.Errorf("query = %v", q)
	}
	if got.Header.Get("X-Token") != "abc" {
		t.Error("custom header missing")
	}
	if res.Provider != "searxng" {
		t.Errorf("provider = %q", res.Provider)
	}
	// site filter drops example.org; dedup drops the www duplicate.
	if len(res.Results) != 1 {
		t.Fatalf("results = %+v", res.Results)
	}
	r := res.Results[0]
	if r.URL != "https://go.dev/doc/?lang=en" || r.Snippet != "The Go programming language" || r.Published != "2026-09-01" || r.Source != "searxng/bing" {
		t.Errorf("result = %+v", r)
	}

	// Without site filter: tracking param stripped, empty title filled.
	res, err = s.Search(context.Background(), SearchRequest{Query: "golang"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 || res.Results[1].URL != "https://example.org/a" || res.Results[1].Title == "" {
		t.Errorf("results = %+v", res.Results)
	}
}

func TestSearXNGForbiddenHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	p := &SearXNG{cfg: SearXNGConfig{BaseURL: srv.URL}, c: testAPI()}
	_, err := p.Search(context.Background(), SearchRequest{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "json format") {
		t.Errorf("err = %v", err)
	}
}

func TestBrave(t *testing.T) {
	t.Setenv("TEST_BRAVE_KEY", "k123")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "k123" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("freshness") != "pd" || r.URL.Query().Get("count") != "3" || r.URL.Query().Get("search_lang") != "pt" {
			http.Error(w, "bad params "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"web":{"results":[{"title":"A","url":"https://a.example/x","description":"<strong>bold</strong> text","page_age":"2026-10-01T10:00:00"},
{"title":"B","url":"https://b.example/","description":"b","age":"2 days ago"}]}}`)
	}))
	defer srv.Close()
	p := &Brave{cfg: APIKeyConfig{APIKeyEnv: "TEST_BRAVE_KEY", BaseURL: srv.URL}, c: testAPI()}
	res, err := p.Search(context.Background(), SearchRequest{Query: "q", Count: 3, Freshness: "day", Lang: "pt-PT"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Snippet != "bold text" || res[0].Published != "2026-10-01T10:00:00Z" || res[1].Published != "2 days ago" {
		t.Errorf("res = %+v", res)
	}
	p2 := &Brave{cfg: APIKeyConfig{APIKeyEnv: "TEST_BRAVE_MISSING"}, c: testAPI()}
	if _, err := p2.Search(context.Background(), SearchRequest{Query: "q"}); err == nil {
		t.Error("expected missing key error")
	}
}

func TestTavily(t *testing.T) {
	t.Setenv("TEST_TAVILY_KEY", "tv")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer tv" || body["query"] != "q" || body["time_range"] != "month" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if d, _ := body["include_domains"].([]any); len(d) != 1 || d[0] != "x.org" {
			http.Error(w, "bad domains", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"results":[{"title":"T","url":"https://x.org/p","content":"c","published_date":"2026-01-02"}]}`)
	}))
	defer srv.Close()
	p := &Tavily{cfg: APIKeyConfig{APIKeyEnv: "TEST_TAVILY_KEY", BaseURL: srv.URL}, c: testAPI()}
	res, err := p.Search(context.Background(), SearchRequest{Query: "q", Count: 5, Freshness: "month", Site: "x.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Published != "2026-01-02" {
		t.Errorf("res = %+v", res)
	}
}

const ddgFixture = `<!DOCTYPE html><html><body>
<div id="links" class="results">
 <div class="result results_links results_links_deep result--ad">
  <h2 class="result__title"><a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=x&u3=https%3A%2F%2Fads.example">Buy now</a></h2>
  <a class="result__snippet" href="#">Sponsored</a>
 </div>
 <div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
   <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FSearXNG%3Futm_medium%3Dx&amp;rut=abc">SearXNG - <b>Wikipedia</b></a></h2>
   <div class="result__extras"><span class="result__timestamp">2025-03-04</span></div>
   <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FSearXNG">SearXNG is a free <b>metasearch</b> engine.</a>
  </div>
 </div>
 <div class="result results_links results_links_deep web-result ">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://docs.searxng.org/">Welcome to SearXNG</a></h2>
  <a class="result__snippet" href="https://docs.searxng.org/">Documentation.</a>
 </div>
</div></body></html>`

func TestParseDDG(t *testing.T) {
	res, err := ParseDDGHTML([]byte(ddgFixture))
	if err != nil {
		t.Fatal(err)
	}
	res = dedupResults(res, "")
	if len(res) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if res[0].URL != "https://en.wikipedia.org/wiki/SearXNG" || res[0].Title != "SearXNG - Wikipedia" ||
		res[0].Snippet != "SearXNG is a free metasearch engine." || res[0].Published != "2025-03-04" {
		t.Errorf("res[0] = %+v", res[0])
	}
	if res[1].URL != "https://docs.searxng.org/" {
		t.Errorf("res[1] = %+v", res[1])
	}

	// Challenge page.
	if _, err := ParseDDGHTML([]byte(`<html><body><form id="challenge-form" action="//duckduckgo.com/anomaly.js"></form></body></html>`)); !errors.Is(err, ErrDDGBlocked) {
		t.Errorf("challenge err = %v", err)
	}
	// Unknown markup.
	if _, err := ParseDDGHTML([]byte(`<html><body><p>hello</p></body></html>`)); err == nil {
		t.Error("expected markup error")
	}
	// Valid empty results page.
	if res, err := ParseDDGHTML([]byte(`<html><body><div id="links" class="results"><div class="no-results">No results.</div></div></body></html>`)); err != nil || len(res) != 0 {
		t.Errorf("empty: %v %v", res, err)
	}
}

func TestDDGProvider(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		io.WriteString(w, ddgFixture)
	}))
	defer srv.Close()
	p := &DuckDuckGo{cfg: DuckDuckGoConfig{BaseURL: srv.URL, MinInterval: Duration(time.Millisecond)}, c: testAPI()}
	res, err := p.Search(context.Background(), SearchRequest{Query: "searxng", Lang: "pt", Freshness: "year"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 { // the ad is skipped
		t.Errorf("res = %d", len(res))
	}
	if form["q"][0] != "searxng" || form["kl"][0] != "pt-pt" || form["df"][0] != "y" {
		t.Errorf("form = %v", form)
	}

	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer blocked.Close()
	p2 := &DuckDuckGo{cfg: DuckDuckGoConfig{BaseURL: blocked.URL}, c: testAPI()}
	if _, err := p2.Search(context.Background(), SearchRequest{Query: "x"}); !errors.Is(err, ErrDDGBlocked) {
		t.Errorf("err = %v", err)
	}
}

type fakeProvider struct {
	name string
	res  []SearchResult
	err  error
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Search(context.Context, SearchRequest) ([]SearchResult, error) {
	return f.res, f.err
}

func TestSearcherFallback(t *testing.T) {
	s := &Searcher{Default: 2, Max: 5, Providers: []Provider{
		fakeProvider{name: "a", err: errors.New("down")},
		fakeProvider{name: "b"},
		fakeProvider{name: "c", res: []SearchResult{{Title: "1", URL: "https://x/1"}, {Title: "2", URL: "https://x/2"}, {Title: "3", URL: "https://x/3"}}},
	}}
	res, err := s.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "c" || len(res.Results) != 2 || len(res.Errors) != 2 {
		t.Errorf("res = %+v", res)
	}
	s.Providers = s.Providers[:1]
	if _, err := s.Search(context.Background(), SearchRequest{Query: "q"}); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("err = %v", err)
	}
	s.Providers = []Provider{fakeProvider{name: "b"}}
	if res, err := s.Search(context.Background(), SearchRequest{Query: "q"}); err != nil || len(res.Results) != 0 {
		t.Errorf("empty: %+v %v", res, err)
	}
	if _, err := s.Search(context.Background(), SearchRequest{Query: " "}); err == nil {
		t.Error("expected query error")
	}
	if _, err := (&Searcher{}).Search(context.Background(), SearchRequest{Query: "q"}); !errors.Is(err, ErrNoProviders) {
		t.Errorf("err = %v", err)
	}
}

func TestProviderOrder(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	cases := []struct {
		cfg  SearchConfig
		want string
	}{
		{SearchConfig{}, "duckduckgo"},
		{SearchConfig{SearXNG: SearXNGConfig{BaseURL: "http://s"}}, "searxng,duckduckgo"},
		{SearchConfig{Provider: StringList{"brave", "DDG"}}, "brave,duckduckgo"},
		{SearchConfig{Provider: StringList{"searxng"}, Fallback: StringList{"tavily", "searxng"}}, "searxng,tavily"},
		{SearchConfig{DuckDuckGo: DuckDuckGoConfig{Disabled: true}}, ""},
	}
	for i, c := range cases {
		c.cfg.Brave.APIKeyEnv, c.cfg.Tavily.APIKeyEnv = "BRAVE_API_KEY", "TAVILY_API_KEY"
		if got := strings.Join(c.cfg.providerOrder(), ","); got != c.want {
			t.Errorf("%d: order = %q, want %q", i, got, c.want)
		}
	}
	t.Setenv("BRAVE_API_KEY", "x")
	cfg := SearchConfig{Brave: APIKeyConfig{APIKeyEnv: "BRAVE_API_KEY"}}
	if got := strings.Join(cfg.providerOrder(), ","); got != "brave,duckduckgo" {
		t.Errorf("auto brave: %q", got)
	}
}

func TestCleanURL(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Example.COM:443/a?utm_source=x&id=3&gclid=y#frag": "https://example.com/a?id=3",
		"http://example.com":                    "http://example.com/",
		"https://x.org/p?ref_src=twsrc&q=a%20b": "https://x.org/p?q=a%20b",
		"https://x.org/p?UTM_Campaign=1":        "https://x.org/p",
		"not a url":                             "not a url",
	}
	for in, want := range cases {
		if got := CleanURL(in); got != want {
			t.Errorf("CleanURL(%q) = %q, want %q", in, got, want)
		}
	}
	if dedupKey("https://www.example.com/a/") != dedupKey("http://example.com/a?utm_source=1") {
		t.Error("dedupKey should ignore scheme, www, trailing slash and tracking")
	}
}
