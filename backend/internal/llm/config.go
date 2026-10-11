// Package llm implements kernel.LLM over OpenAI-compatible HTTP APIs
// (Unify Gateway, Ollama's /v1, vLLM, llama.cpp server, OpenAI itself…).
//
// Agents reference model aliases (vault policies/models.md); this package
// resolves an alias to a provider + concrete model, talks to the provider's
// /chat/completions and /embeddings endpoints, and reports usage as
// nexusv1.Cost.
//
// Configuration (module section "llm" on the hub, "node.llm" on nodes):
//
//	llm:
//	  default_provider: unify          # used for unknown aliases (raw model names)
//	  currency: EUR                    # currency of the price table (default EUR)
//	  max_retries: 3                   # retries on 429/5xx/network errors
//	  providers:
//	    unify:
//	      base_url: https://gateway.example/v1
//	      api_key_env: UNIFY_API_KEY   # bearer token read from this env var
//	      headers: { X-Client: nexus } # extra headers
//	      timeout: 120s                # per request (default 120s)
//	    ollama:
//	      base_url: http://localhost:11434/v1
//	      no_stream_usage: true        # don't send stream_options.include_usage
//	  aliases:
//	    smart:      { provider: unify,  model: "gpt-4.1" }
//	    fast-local: { provider: ollama, model: "qwen2.5:7b" }
//	    embed:      { provider: ollama, model: "nomic-embed-text" }
//	  prices:                          # optional; per million tokens, major units
//	    gpt-4.1: { input: 2.0, output: 8.0 }
//
// An unknown alias is treated as a raw model name on the default provider.
// When no providers are configured the module provides nothing.
package llm

import (
	"sort"
	"time"
)

// Config is the module configuration.
type Config struct {
	DefaultProvider string                    `yaml:"default_provider"`
	Currency        string                    `yaml:"currency"`
	MaxRetries      *int                      `yaml:"max_retries"`
	Providers       map[string]ProviderConfig `yaml:"providers"`
	Aliases         map[string]Alias          `yaml:"aliases"`
	// Prices keyed by concrete model name, or "provider/model" for a
	// provider-specific price (checked first).
	Prices map[string]Price `yaml:"prices"`
}

// ProviderConfig is one OpenAI-compatible endpoint.
type ProviderConfig struct {
	// BaseURL includes the API version prefix, e.g. http://localhost:11434/v1.
	BaseURL string `yaml:"base_url"`
	// APIKeyEnv names the environment variable holding the bearer token.
	APIKeyEnv string            `yaml:"api_key_env"`
	Headers   map[string]string `yaml:"headers"`
	Timeout   time.Duration     `yaml:"timeout"`
	// NoStreamUsage disables stream_options.include_usage for servers that reject it.
	NoStreamUsage bool `yaml:"no_stream_usage"`
}

// Alias maps a model alias to a provider and concrete model.
type Alias struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// Price is the cost per million tokens in major currency units.
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

func (c *Config) defaultProvider() string {
	if c.DefaultProvider != "" {
		if _, ok := c.Providers[c.DefaultProvider]; ok {
			return c.DefaultProvider
		}
	}
	if _, ok := c.Providers["unify"]; ok {
		return "unify"
	}
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
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
