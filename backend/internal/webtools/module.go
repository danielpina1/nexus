// Package webtools provides web research tools for agents: web.search
// (pluggable providers with fallback: SearXNG, Brave, Tavily, DuckDuckGo
// HTML), web.fetch (download + readability-style extraction to compact
// Markdown, minimal PDF text, SSRF guard, on-disk cache) and web.extract
// (fetch + BM25 passage ranking against a question).
//
// Two modules share the implementation:
//
//   - "tools.web" (node side, default on) registers the tools as node-local
//     tools, so agents running on that node (runtime openai/ollama/browser)
//     call them directly from the node's network.
//   - "tools.web.hub" (hub side, default on when compiled into nexusd)
//     registers them as hub tools, so workflow `tool:` steps can call
//     web.search/web.fetch and nodes without the node module get them
//     proxied. A node-local tool wins over the hub one of the same name.
//
// Configuration (section "tools.web" on nodes, "tools.web.hub" on the hub):
//
//	tools.web:
//	  search:
//	    provider: [searxng, duckduckgo]   # order = fallback; default: every configured one, then duckduckgo
//	    searxng:
//	      base_url: http://searxng.lan:8080   # JSON format must be enabled in settings.yml
//	      # engines: "google,bing,duckduckgo,wikipedia"
//	      # categories: general
//	      # safesearch: 0
//	      # headers: { Authorization: "Bearer ${SEARXNG_TOKEN}" }
//	    brave:  { api_key_env: BRAVE_API_KEY }
//	    tavily: { api_key_env: TAVILY_API_KEY }
//	    duckduckgo: { disabled: false, min_interval: 2s }
//	    default_count: 8
//	    max_count: 20
//	    timeout: 15s
//	    use_env_proxy: false
//	  fetch:
//	    timeout: 20s
//	    max_download_bytes: 5242880
//	    max_chars: 40000
//	    max_links: 20
//	    user_agent: ""
//	    accept_language: "en;q=0.9, pt;q=0.8, *;q=0.5"
//	    allow_private: false       # SSRF guard off (not recommended)
//	    allow_hosts: []            # e.g. [wiki.lan, "*.home.arpa", 192.168.1.0/24]
//	    deny_hosts: []
//	    respect_robots: false
//	    use_env_proxy: false
//	  cache:
//	    disabled: false
//	    ttl: 6h
//	    dir: ""                    # default <data_dir>/tools.web/cache
//	    max_bytes: 536870912
//
// Search providers are configured by the operator and are reached without
// the SSRF guard (a home SearXNG usually lives on a private address); the
// guard applies to model-chosen URLs (web.fetch / web.extract), at dial time
// for every connection including redirects.
package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

func init() {
	kernel.RegisterModule(func() kernel.Module { return &Module{side: kernel.SideNode} })
	kernel.RegisterModule(func() kernel.Module { return &Module{side: kernel.SideHub} })
}

// Tool names.
const (
	ToolSearch  = "web.search"
	ToolFetch   = "web.fetch"
	ToolExtract = "web.extract"
)

// Module registers the web tools on a node ("tools.web") or the hub
// ("tools.web.hub").
type Module struct {
	side kernel.Side
	tk   *Toolkit

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (m *Module) Info() kernel.ModuleInfo {
	if m.side == kernel.SideHub {
		return kernel.ModuleInfo{
			Name:           "tools.web.hub",
			Description:    "Web research tools on the hub (web.search, web.fetch, web.extract) for workflows and proxied agents",
			Side:           kernel.SideHub,
			DefaultEnabled: true,
		}
	}
	return kernel.ModuleInfo{
		Name:           "tools.web",
		Description:    "Node-local web research tools: web.search, web.fetch, web.extract",
		Side:           kernel.SideNode,
		DefaultEnabled: true,
	}
}

func (m *Module) Init(_ context.Context, h kernel.Host) error {
	var cfg Config
	if err := h.Config(&cfg); err != nil {
		return fmt.Errorf("%s config: %w", m.Info().Name, err)
	}
	if cfg.Cache.Dir == "" {
		cfg.Cache.Dir = filepath.Join(h.DataDir(), "cache")
	}
	tk, err := NewToolkit(cfg)
	if err != nil {
		return err
	}
	for _, t := range tk.Tools() {
		if err := h.Tools().Register(t); err != nil {
			return err
		}
	}
	m.tk = tk
	order := make([]string, 0, len(tk.search.Providers))
	for _, p := range tk.search.Providers {
		order = append(order, p.Name())
	}
	log := h.Logger()
	log.Info("web tools ready", "module", m.Info().Name, "search_providers", strings.Join(order, ","),
		"cache", !cfg.Cache.Disabled, "allow_private", cfg.Fetch.AllowPrivate)
	if len(order) == 1 && order[0] == "duckduckgo" {
		log.Warn("web.search uses only the DuckDuckGo HTML fallback, which may be rate-limited; configure a SearXNG instance (see docs/RESEARCH.md)")
	}
	return nil
}

func (m *Module) Start(context.Context) error {
	if m.tk == nil || m.tk.cache == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			_, _ = m.tk.cache.Prune()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (m *Module) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
	return nil
}

// Toolkit holds the configured searcher and fetcher.
type Toolkit struct {
	cfg     Config
	search  *Searcher
	fetcher *Fetcher
	cache   *diskCache
}

// NewToolkit builds the tools from cfg (defaults applied).
func NewToolkit(cfg Config) (*Toolkit, error) {
	cfg.defaults()
	tk := &Toolkit{cfg: cfg}
	if !cfg.Cache.Disabled && cfg.Cache.Dir != "" {
		c, err := newDiskCache(cfg.Cache.Dir, time.Duration(cfg.Cache.TTL), cfg.Cache.MaxBytes)
		if err != nil {
			return nil, fmt.Errorf("tools.web cache: %w", err)
		}
		tk.cache = c
	}
	tk.fetcher = NewFetcher(cfg.Fetch, tk.cache)
	api := newAPIClient(time.Duration(cfg.Search.Timeout), cfg.Search.UseEnvProxy)
	s := &Searcher{Default: cfg.Search.DefaultCount, Max: cfg.Search.MaxCount}
	for _, name := range cfg.Search.providerOrder() {
		switch name {
		case "searxng":
			if cfg.Search.SearXNG.BaseURL == "" {
				return nil, errors.New("tools.web: provider searxng needs search.searxng.base_url")
			}
			s.Providers = append(s.Providers, &SearXNG{cfg: cfg.Search.SearXNG, c: api})
		case "brave":
			s.Providers = append(s.Providers, &Brave{cfg: cfg.Search.Brave, c: api})
		case "tavily":
			s.Providers = append(s.Providers, &Tavily{cfg: cfg.Search.Tavily, c: api})
		case "duckduckgo":
			s.Providers = append(s.Providers, &DuckDuckGo{cfg: cfg.Search.DuckDuckGo, c: api})
		default:
			return nil, fmt.Errorf("tools.web: unknown search provider %q (searxng, brave, tavily, duckduckgo)", name)
		}
	}
	tk.search = s
	return tk, nil
}

// Tool input schemas.
var (
	searchSchema = json.RawMessage(`{"type":"object","properties":{
"query":{"type":"string","description":"Search query. Plain keywords work best; one topic per query."},
"count":{"type":"integer","minimum":1,"maximum":20,"description":"Number of results (default 8)"},
"freshness":{"type":"string","enum":["day","week","month","year"],"description":"Only results from the last day/week/month/year"},
"site":{"type":"string","description":"Restrict to a domain, e.g. reddit.com"},
"lang":{"type":"string","description":"Result language, e.g. en, pt, pt-PT"}},
"required":["query"]}`)
	fetchSchema = json.RawMessage(`{"type":"object","properties":{
"url":{"type":"string","description":"http(s) URL to read"},
"max_chars":{"type":"integer","minimum":500,"maximum":200000,"description":"Cap on returned text (default 40000)"}},
"required":["url"]}`)
	extractSchema = json.RawMessage(`{"type":"object","properties":{
"url":{"type":"string","description":"http(s) URL to read"},
"question":{"type":"string","description":"What you are looking for; passages are ranked by these keywords"},
"max_passages":{"type":"integer","minimum":1,"maximum":20,"description":"Passages to return (default 6)"},
"passage_chars":{"type":"integer","minimum":200,"maximum":4000,"description":"Approximate passage size (default 800)"}},
"required":["url","question"]}`)
)

// Tools returns web.search, web.fetch and web.extract.
func (tk *Toolkit) Tools() []kernel.Tool {
	read := nexusv1.ActionClass_ACTION_CLASS_READ
	return []kernel.Tool{
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name: ToolSearch,
			Description: "Search the web. Returns {provider, results:[{title,url,snippet,published?}]}. " +
				"Snippets are not evidence: open the pages you rely on with web.fetch or web.extract.",
			InputSchema: searchSchema, ActionClass: read,
		}, F: tk.invokeSearch},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name: ToolFetch,
			Description: "Read a web page (or text/PDF) as compact Markdown: {url, final_url, title, byline?, published?, " +
				"text, links, content_type, truncated}. Navigation, ads and boilerplate are removed. Results are cached.",
			InputSchema: fetchSchema, ActionClass: read,
		}, F: tk.invokeFetch},
		kernel.ToolFunc{S: kernel.ToolSpec{
			Name: ToolExtract,
			Description: "Read a page and return only the passages most relevant to a question (keyword/BM25 ranking). " +
				"Cheaper than web.fetch for long pages; fetch the full page when context matters.",
			InputSchema: extractSchema, ActionClass: read,
		}, F: tk.invokeExtract},
	}
}

func (tk *Toolkit) invokeSearch(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in SearchRequest
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, fmt.Errorf("web.search: %w", err)
	}
	res, err := tk.Search(ctx, in)
	if err != nil {
		return kernel.ToolResult{}, err
	}
	return kernel.ToolOutput(res)
}

// Search runs web.search.
func (tk *Toolkit) Search(ctx context.Context, in SearchRequest) (*SearchResponse, error) {
	return tk.search.Search(ctx, in)
}

func (tk *Toolkit) invokeFetch(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, fmt.Errorf("web.fetch: %w", err)
	}
	res, err := tk.fetcher.Fetch(ctx, in.URL, in.MaxChars)
	if err != nil {
		return kernel.ToolResult{}, fmt.Errorf("web.fetch: %w", err)
	}
	return kernel.ToolOutput(res)
}

// ExtractResult is the web.extract output.
type ExtractResult struct {
	URL           string    `json:"url"`
	FinalURL      string    `json:"final_url"`
	Title         string    `json:"title"`
	Byline        string    `json:"byline,omitempty"`
	Published     string    `json:"published,omitempty"`
	ContentType   string    `json:"content_type"`
	Passages      []Passage `json:"passages"`
	TotalPassages int       `json:"total_passages"`
	Note          string    `json:"note,omitempty"`
	Cached        bool      `json:"cached,omitempty"`
}

func (tk *Toolkit) invokeExtract(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
	var in struct {
		URL          string `json:"url"`
		Question     string `json:"question"`
		MaxPassages  int    `json:"max_passages"`
		PassageChars int    `json:"passage_chars"`
	}
	if err := kernel.ToolInput(c, &in); err != nil {
		return kernel.ToolResult{}, fmt.Errorf("web.extract: %w", err)
	}
	res, err := tk.Extract(ctx, in.URL, in.Question, in.MaxPassages, in.PassageChars)
	if err != nil {
		return kernel.ToolResult{}, fmt.Errorf("web.extract: %w", err)
	}
	return kernel.ToolOutput(res)
}

// Extract fetches url and returns the passages most relevant to question.
func (tk *Toolkit) Extract(ctx context.Context, url, question string, maxPassages, passageChars int) (*ExtractResult, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("question required")
	}
	if maxPassages <= 0 {
		maxPassages = 6
	}
	maxPassages = min(maxPassages, 20)
	if passageChars <= 0 {
		passageChars = 800
	}
	passageChars = max(200, min(passageChars, 4000))
	page, err := tk.fetcher.Fetch(ctx, url, HardMaxChars)
	if err != nil {
		return nil, err
	}
	out := &ExtractResult{URL: page.URL, FinalURL: page.FinalURL, Title: page.Title, Byline: page.Byline,
		Published: page.Published, ContentType: page.ContentType, Note: page.Note, Cached: page.Cached}
	out.Passages, out.TotalPassages = RankPassages(page.Text, question, passageChars, maxPassages)
	if len(out.Passages) == 0 {
		out.Passages = leadPassages(page.Text, passageChars, 2)
		out.Note = strings.TrimSpace(out.Note + " No passage matched the question's keywords; showing the beginning of the page.")
	}
	if out.Passages == nil {
		out.Passages = []Passage{}
	}
	return out, nil
}
