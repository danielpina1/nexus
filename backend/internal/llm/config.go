// Package llm implements kernel.LLM over HTTP model APIs: any
// OpenAI-compatible endpoint (Ollama's /v1, vLLM, llama.cpp server, LiteLLM,
// OpenAI…) and the Anthropic Messages API (or a gateway that exposes it).
//
// Agents reference model aliases (vault policies/models.md); this package
// resolves an alias to a provider + concrete model (with optional fallback
// chain), talks to the provider, tracks provider health and reports usage as
// nexusv1.Cost.
//
// Configuration (module section "llm" on the hub, "node.llm" on nodes):
//
//	llm:
//	  default_provider: ollama         # used for raw model names (default: first provider by name)
//	  currency: EUR                    # currency of the price table (default EUR)
//	  max_retries: 3                   # retries on 429/5xx/network errors (per target)
//	  aliases_auto: true               # fill unset aliases from discovered models
//	  alias_preferences:               # optional; overrides the built-in lists
//	    smart: [sonnet, gpt-4.1, qwen2.5:14b]
//	  health:
//	    failure_threshold: 3           # consecutive failures before a provider is degraded
//	    cooldown: 15s                  # first cool-down; doubles each time, up to max_cooldown
//	    max_cooldown: 5m
//	  providers:
//	    ollama:
//	      base_url: http://localhost:11434   # "/v1" is appended when the URL has no path
//	      api: openai                        # openai (default) | anthropic
//	      auth: none                         # bearer (default) | header | none
//	      no_stream_usage: true
//	    cloud:
//	      base_url: https://api.anthropic.com
//	      api: anthropic                     # /v1/messages, x-api-key by default
//	      api_key_env: ANTHROPIC_API_KEY
//	      ca_file: /etc/nexus/gateway-ca.pem # custom CA for a self-signed gateway
//	  aliases:
//	    fast-local: { provider: ollama, model: "qwen2.5:7b" }
//	    smart:
//	      provider: cloud
//	      model: claude-sonnet-4-5
//	      fallback: [{ provider: ollama, model: "qwen2.5:14b" }]
//	    embed:      { provider: ollama, model: "nomic-embed-text" }
//	  prices:                          # optional; per million tokens, major units
//	    claude-sonnet-4-5: { input: 3.0, output: 15.0 }
//
// An unknown alias is treated as a raw model name on the default provider.
// When no providers are configured the module provides nothing.
package llm

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// API flavours.
const (
	APIOpenAI    = "openai"
	APIAnthropic = "anthropic"
)

// Auth modes.
const (
	AuthBearer = "bearer"
	AuthHeader = "header"
	AuthNone   = "none"
)

// DefaultAnthropicVersion is sent as the anthropic-version header.
const DefaultAnthropicVersion = "2023-06-01"

// Config is the module configuration.
type Config struct {
	DefaultProvider string                    `yaml:"default_provider"`
	Currency        string                    `yaml:"currency"`
	MaxRetries      *int                      `yaml:"max_retries"`
	Providers       map[string]ProviderConfig `yaml:"providers"`
	Aliases         map[string]Alias          `yaml:"aliases"`
	// AliasesAuto fills aliases whose model is unset (empty, CHANGE_ME or
	// "auto") from the providers' model lists. Explicit aliases always win.
	AliasesAuto bool `yaml:"aliases_auto"`
	// AliasPreferences overrides the built-in substring preference lists
	// used by AliasesAuto (alias -> ordered substrings).
	AliasPreferences map[string][]string `yaml:"alias_preferences"`
	Health           HealthConfig        `yaml:"health"`
	// Prices keyed by concrete model name, or "provider/model" for a
	// provider-specific price (checked first).
	Prices map[string]Price `yaml:"prices"`
}

// HealthConfig tunes provider health tracking.
type HealthConfig struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	Cooldown         time.Duration `yaml:"cooldown"`
	MaxCooldown      time.Duration `yaml:"max_cooldown"`
}

// ProviderConfig is one model endpoint.
type ProviderConfig struct {
	// BaseURL is the API root. A URL without a path gets "/v1" appended;
	// a URL with a path (e.g. http://gw:8080/v1 or http://gw/api/openai/v1)
	// is used as given. Endpoint suffixes such as /chat/completions are
	// stripped.
	BaseURL string `yaml:"base_url"`
	// API is the wire flavour: "openai" (default) or "anthropic", or a
	// subscription CLI: "claude-cli" (Claude Code) or "codex-cli" (Codex).
	API string `yaml:"api"`
	// Binary is the CLI to run for claude-cli / codex-cli (default claude / codex).
	Binary string `yaml:"binary"`
	// Kind is a deprecated synonym of API.
	Kind string `yaml:"kind"`
	// APIKeyEnv names the environment variable holding the key.
	APIKeyEnv string `yaml:"api_key_env"`
	// Auth: "bearer" (Authorization: Bearer <key>; default for openai),
	// "header" (raw key in AuthHeader; default for anthropic with
	// x-api-key) or "none" (LAN endpoints).
	Auth string `yaml:"auth"`
	// AuthHeader is the header carrying the key in "header" mode.
	AuthHeader string `yaml:"auth_header"`
	// AnthropicVersion is the anthropic-version header (anthropic API).
	AnthropicVersion string `yaml:"anthropic_version"`
	// MaxTokens is the default max_tokens for the anthropic API, which
	// requires it (default 4096).
	MaxTokens int `yaml:"max_tokens"`
	// CAFile is a PEM bundle trusted in addition to the system roots, for
	// gateways with a private CA or self-signed certificate.
	CAFile string `yaml:"ca_file"`
	// InsecureSkipVerify is refused; it exists only to produce a helpful error.
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify"`
	Headers            map[string]string `yaml:"headers"`
	Timeout            time.Duration     `yaml:"timeout"`
	// NoStreamUsage disables stream_options.include_usage for servers that reject it.
	NoStreamUsage bool `yaml:"no_stream_usage"`
}

// Target is a concrete provider + model.
type Target struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

func (t Target) String() string { return t.Provider + "/" + t.Model }

// Alias maps a model alias to a provider and concrete model, with an
// optional ordered fallback chain used when the primary fails.
type Alias struct {
	Provider string   `yaml:"provider"`
	Model    string   `yaml:"model"`
	Fallback []Target `yaml:"fallback"`
}

// Price is the cost per million tokens in major currency units.
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// DefaultAliasPreferences are the substring preference lists used by
// aliases_auto. Earlier entries win; among models matching the same entry
// the lexicographically greatest id (usually the newest) is chosen.
var DefaultAliasPreferences = map[string][]string{
	"fast-local": {"haiku", "mini", "flash", "llama", "qwen", "phi", "gemma", "mistral"},
	"smart":      {"sonnet", "gpt-4.1", "gpt-4o", "gpt-4", "gpt-5", "gemini", "qwen", "llama", "mistral"},
	"best":       {"opus", "gpt-5", "o3", "gpt-4.1", "sonnet", "gemini", "qwen", "llama"},
	"embed":      {"embed"},
}

// isPlaceholder reports whether an alias model is unset.
func isPlaceholder(model string) bool {
	m := strings.TrimSpace(model)
	return m == "" || strings.EqualFold(m, "CHANGE_ME") || strings.EqualFold(m, "auto")
}

func (p ProviderConfig) api() string {
	a := strings.ToLower(strings.TrimSpace(p.API))
	if a == "" {
		a = strings.ToLower(strings.TrimSpace(p.Kind))
	}
	if a == "" {
		return APIOpenAI
	}
	return a
}

// authMode returns the auth mode and header name.
func (p ProviderConfig) authMode() (mode, header string) {
	mode = strings.ToLower(strings.TrimSpace(p.Auth))
	header = strings.TrimSpace(p.AuthHeader)
	if mode == "" {
		switch {
		case header != "":
			mode = AuthHeader
		case p.api() == APIAnthropic:
			mode, header = AuthHeader, "x-api-key"
		default:
			mode = AuthBearer
		}
	}
	if mode == AuthHeader && header == "" {
		header = "x-api-key"
	}
	return mode, header
}

func (p ProviderConfig) anthropicVersion() string {
	if p.AnthropicVersion != "" {
		return p.AnthropicVersion
	}
	return DefaultAnthropicVersion
}

func (p ProviderConfig) maxTokens() int {
	if p.MaxTokens > 0 {
		return p.MaxTokens
	}
	return 4096
}

// NormalizeBaseURL validates and normalises a provider base URL: trailing
// slashes and endpoint suffixes are removed and "/v1" is appended when the
// URL has no path.
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty base_url")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid base_url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base_url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("base_url %q has no host", raw)
	}
	p := strings.TrimRight(u.Path, "/")
	for _, suf := range []string{"/chat/completions", "/completions", "/messages", "/embeddings", "/models"} {
		if strings.HasSuffix(p, suf) {
			p = strings.TrimSuffix(p, suf)
			break
		}
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/v1"
	}
	u.Path = p
	u.RawPath = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Config) validate() error {
	if len(c.Providers) == 0 {
		return errors.New("llm: no providers configured")
	}
	for name, p := range c.Providers {
		if isCLIAPI(p.api()) {
			continue // a local CLI: no URL, auth or TLS settings
		}
		if _, err := NormalizeBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("llm: provider %q: %w", name, err)
		}
		switch p.api() {
		case APIOpenAI, APIAnthropic:
		default:
			return fmt.Errorf("llm: provider %q: unknown api %q (use openai, anthropic, claude-cli or codex-cli)", name, p.api())
		}
		mode, _ := p.authMode()
		switch mode {
		case AuthBearer, AuthHeader, AuthNone:
		default:
			return fmt.Errorf("llm: provider %q: unknown auth %q (use bearer, header or none)", name, p.Auth)
		}
		if p.InsecureSkipVerify {
			return fmt.Errorf("llm: provider %q: insecure_skip_verify is not supported; "+
				"export the gateway's CA or self-signed certificate as PEM and set ca_file: /path/to/ca.pem", name)
		}
	}
	check := func(alias string, t Target) error {
		if t.Provider == "" {
			return nil
		}
		if _, ok := c.Providers[t.Provider]; !ok {
			return fmt.Errorf("llm: alias %q uses unknown provider %q", alias, t.Provider)
		}
		return nil
	}
	for alias, a := range c.Aliases {
		if err := check(alias, Target{a.Provider, a.Model}); err != nil {
			return err
		}
		for _, f := range a.Fallback {
			if err := check(alias, f); err != nil {
				return err
			}
			if isPlaceholder(f.Model) {
				return fmt.Errorf("llm: alias %q has a fallback without a model", alias)
			}
		}
	}
	return nil
}

func (c *Config) defaultProvider() string {
	if c.DefaultProvider != "" {
		if _, ok := c.Providers[c.DefaultProvider]; ok {
			return c.DefaultProvider
		}
	}
	names := c.providerNames()
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func (c *Config) providerNames() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) maxRetries() int {
	if c.MaxRetries == nil {
		return 3
	}
	if *c.MaxRetries < 0 {
		return 0
	}
	return *c.MaxRetries
}

func (c *Config) currency() string {
	if c.Currency == "" {
		return "EUR"
	}
	return c.Currency
}

func (c *Config) preferences(alias string) []string {
	if p, ok := c.AliasPreferences[alias]; ok {
		return p
	}
	return DefaultAliasPreferences[alias]
}

func (h HealthConfig) threshold() int {
	if h.FailureThreshold <= 0 {
		return 3
	}
	return h.FailureThreshold
}

func (h HealthConfig) cooldown() time.Duration {
	if h.Cooldown <= 0 {
		return 15 * time.Second
	}
	return h.Cooldown
}

func (h HealthConfig) maxCooldown() time.Duration {
	if h.MaxCooldown <= 0 {
		return 5 * time.Minute
	}
	return h.MaxCooldown
}
