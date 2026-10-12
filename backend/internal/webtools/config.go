package webtools

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults and hard limits.
const (
	DefaultUserAgent        = "Mozilla/5.0 (compatible; NexusResearch/1.0; +https://github.com/danielpina1/nexus)"
	DefaultMaxDownloadBytes = 5 << 20
	DefaultMaxChars         = 40000
	HardMaxChars            = 200000
	DefaultMaxLinks         = 20
	DefaultSearchCount      = 8
	MaxSearchCount          = 20
	DefaultCacheTTL         = 6 * time.Hour
	DefaultFetchTimeout     = 20 * time.Second
	DefaultSearchTimeout    = 15 * time.Second
	MaxRedirects            = 5
)

// StringList accepts a scalar or a sequence in YAML.
type StringList []string

func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if s := strings.TrimSpace(n.Value); s != "" && n.Tag != "!!null" {
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					*l = append(*l, p)
				}
			}
		}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err != nil {
			return err
		}
		*l = out
		return nil
	}
	return fmt.Errorf("want a string or a list")
}

// Duration accepts "15s", "6h" or a number of seconds.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("want a duration")
	}
	s := strings.TrimSpace(n.Value)
	if s == "" {
		return nil
	}
	if v, err := time.ParseDuration(s); err == nil {
		*d = Duration(v)
		return nil
	}
	var secs int
	if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && fmt.Sprint(secs) == s {
		*d = Duration(time.Duration(secs) * time.Second)
		return nil
	}
	return fmt.Errorf("invalid duration %q", s)
}

// Config is the module configuration (section "tools.web" on nodes,
// "tools.web.hub" on the hub).
type Config struct {
	Search SearchConfig `yaml:"search"`
	Fetch  FetchConfig  `yaml:"fetch"`
	Cache  CacheConfig  `yaml:"cache"`
}

// SearchConfig selects and configures search providers.
type SearchConfig struct {
	// Provider is the provider order (a name or a list); later entries are
	// fallbacks. Empty: every configured provider (searxng with a base_url,
	// brave/tavily with a key in the environment), then duckduckgo.
	Provider StringList `yaml:"provider"`
	// Fallback is appended to Provider (convenience).
	Fallback     StringList       `yaml:"fallback"`
	SearXNG      SearXNGConfig    `yaml:"searxng"`
	Brave        APIKeyConfig     `yaml:"brave"`
	Tavily       APIKeyConfig     `yaml:"tavily"`
	DuckDuckGo   DuckDuckGoConfig `yaml:"duckduckgo"`
	DefaultCount int              `yaml:"default_count"`
	MaxCount     int              `yaml:"max_count"`
	Timeout      Duration         `yaml:"timeout"`
	// UseEnvProxy sends provider requests through HTTP(S)_PROXY.
	UseEnvProxy bool `yaml:"use_env_proxy"`
}

// SearXNGConfig points at a (self-hosted) SearXNG instance.
type SearXNGConfig struct {
	BaseURL string `yaml:"base_url"`
	// Engines / Categories are passed through when set (comma separated).
	Engines    string `yaml:"engines"`
	Categories string `yaml:"categories"`
	// SafeSearch 0, 1 or 2 (default: instance setting).
	SafeSearch *int `yaml:"safesearch"`
	// Headers are added to each request (e.g. a reverse-proxy auth token).
	Headers map[string]string `yaml:"headers"`
}

// APIKeyConfig is a hosted API provider.
type APIKeyConfig struct {
	APIKeyEnv string `yaml:"api_key_env"`
	BaseURL   string `yaml:"base_url"` // override (tests, proxies)
}

func (c APIKeyConfig) key() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(c.APIKeyEnv))
}

// DuckDuckGoConfig configures the keyless HTML fallback.
type DuckDuckGoConfig struct {
	Disabled bool   `yaml:"disabled"`
	BaseURL  string `yaml:"base_url"` // default https://html.duckduckgo.com/html/
	// MinInterval spaces requests (DDG rate-limits scrapers). Default 2s.
	MinInterval Duration `yaml:"min_interval"`
}

// FetchConfig bounds web.fetch / web.extract.
type FetchConfig struct {
	Timeout          Duration `yaml:"timeout"`
	MaxDownloadBytes int64    `yaml:"max_download_bytes"`
	MaxChars         int      `yaml:"max_chars"`
	MaxLinks         int      `yaml:"max_links"`
	UserAgent        string   `yaml:"user_agent"`
	AcceptLanguage   string   `yaml:"accept_language"`
	// AllowPrivate disables the SSRF guard (private, loopback, link-local…
	// addresses become reachable). Prefer AllowHosts.
	AllowPrivate bool `yaml:"allow_private"`
	// AllowHosts are hostnames ("wiki.lan", "*.home.arpa") or CIDRs
	// ("192.168.1.0/24") reachable even though they are private.
	AllowHosts []string `yaml:"allow_hosts"`
	// DenyHosts are never fetched (hostnames, "*.suffix" or CIDRs).
	DenyHosts []string `yaml:"deny_hosts"`
	// RespectRobots checks robots.txt before fetching.
	RespectRobots bool `yaml:"respect_robots"`
	// UseEnvProxy sends fetches through HTTP(S)_PROXY. The SSRF guard then
	// checks resolved addresses before each request instead of at dial time.
	UseEnvProxy bool `yaml:"use_env_proxy"`
}

// CacheConfig is the on-disk fetch cache.
type CacheConfig struct {
	Disabled bool     `yaml:"disabled"`
	TTL      Duration `yaml:"ttl"`
	Dir      string   `yaml:"dir"`      // default <data_dir>/cache
	MaxBytes int64    `yaml:"max_bytes"` // default 512 MiB; oldest entries pruned
}

func (c *Config) defaults() {
	s := &c.Search
	if s.DefaultCount <= 0 {
		s.DefaultCount = DefaultSearchCount
	}
	if s.MaxCount <= 0 {
		s.MaxCount = MaxSearchCount
	}
	if s.Timeout <= 0 {
		s.Timeout = Duration(DefaultSearchTimeout)
	}
	if s.Brave.APIKeyEnv == "" {
		s.Brave.APIKeyEnv = "BRAVE_API_KEY"
	}
	if s.Tavily.APIKeyEnv == "" {
		s.Tavily.APIKeyEnv = "TAVILY_API_KEY"
	}
	if s.DuckDuckGo.MinInterval <= 0 {
		s.DuckDuckGo.MinInterval = Duration(2 * time.Second)
	}
	f := &c.Fetch
	if f.Timeout <= 0 {
		f.Timeout = Duration(DefaultFetchTimeout)
	}
	if f.MaxDownloadBytes <= 0 {
		f.MaxDownloadBytes = DefaultMaxDownloadBytes
	}
	if f.MaxChars <= 0 {
		f.MaxChars = DefaultMaxChars
	}
	f.MaxChars = min(f.MaxChars, HardMaxChars)
	if f.MaxLinks <= 0 {
		f.MaxLinks = DefaultMaxLinks
	}
	if f.UserAgent == "" {
		f.UserAgent = DefaultUserAgent
	}
	if f.AcceptLanguage == "" {
		f.AcceptLanguage = "en;q=0.9, pt;q=0.8, *;q=0.5"
	}
	if c.Cache.TTL <= 0 {
		c.Cache.TTL = Duration(DefaultCacheTTL)
	}
	if c.Cache.MaxBytes <= 0 {
		c.Cache.MaxBytes = 512 << 20
	}
}

// providerOrder resolves the provider fallback order.
func (s SearchConfig) providerOrder() []string {
	var order []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "ddg" {
			n = "duckduckgo"
		}
		if n != "" && !seen[n] {
			seen[n] = true
			order = append(order, n)
		}
	}
	for _, n := range s.Provider {
		add(n)
	}
	for _, n := range s.Fallback {
		add(n)
	}
	if len(order) > 0 {
		return order
	}
	if s.SearXNG.BaseURL != "" {
		add("searxng")
	}
	if s.Brave.key() != "" {
		add("brave")
	}
	if s.Tavily.key() != "" {
		add("tavily")
	}
	if !s.DuckDuckGo.Disabled {
		add("duckduckgo")
	}
	return order
}
