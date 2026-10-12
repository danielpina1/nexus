package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

const defaultTimeout = 300 * time.Second

// ProviderScoper is implemented by LLMs that can change the provider used
// for model names that are not configured aliases. The "ollama" runtime uses
// it so raw model names go to the local endpoint.
type ProviderScoper interface {
	WithDefaultProvider(name string) kernel.LLM
	HasProvider(name string) bool
}

// APIError is a non-2xx response (or an error object in a 2xx body) from a
// provider. StatusCode is 0 for errors reported inside a stream.
type APIError struct {
	Provider   string
	StatusCode int
	Message    string
	URL        string
}

func (e *APIError) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("llm: %s: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("llm: %s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Retryable reports whether the request may succeed when retried.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// ChainError reports the failures of every target tried for one call.
type ChainError struct {
	Model string
	Errs  []error
}

func (e *ChainError) Error() string {
	parts := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("llm: %s: all %d targets failed: %s", e.Model, len(e.Errs), strings.Join(parts, "; "))
}

func (e *ChainError) Unwrap() []error { return e.Errs }

// ErrNoEmbeddings is returned when the provider's API has no embeddings endpoint.
var ErrNoEmbeddings = errors.New("this provider's API has no embeddings endpoint; point the embed alias at an openai-compatible provider")

// Client implements kernel.LLM.
type Client struct {
	cfg             Config
	httpc           *http.Client            // override for every provider (tests)
	clients         map[string]*http.Client // per provider (TLS settings)
	bases           map[string]string       // normalised base URLs
	log             *slog.Logger
	getenv          func(string) string
	backoff         func(attempt int) time.Duration
	defaultProvider string
	st              *sharedState
}

// sharedState is shared by scoped copies of a Client.
type sharedState struct {
	health *healthTracker

	mu         sync.Mutex
	discovered map[string][]ModelInfo // provider -> models
	auto       map[string]Target      // alias -> auto-selected target
	discErr    error
	discAt     time.Time
	discMu     sync.Mutex // serialises discovery
}

var (
	_ kernel.LLM     = (*Client)(nil)
	_ ProviderScoper = (*Client)(nil)
)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for every provider (tests,
// custom transports). It replaces the per-provider TLS configuration.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpc = h } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithGetenv overrides environment lookup for API keys.
func WithGetenv(f func(string) string) Option { return func(c *Client) { c.getenv = f } }

// WithBackoff overrides the retry delay function (Retry-After still wins).
func WithBackoff(f func(attempt int) time.Duration) Option {
	return func(c *Client) { c.backoff = f }
}

// New builds a client. It fails when no providers are configured, a
// provider is invalid (base_url, api, auth, TLS) or an alias points at an
// unknown provider.
func New(cfg Config, opts ...Option) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	c := &Client{
		cfg:     cfg,
		clients: map[string]*http.Client{},
		bases:   map[string]string{},
		log:     slog.Default(),
		getenv:  os.Getenv,
		backoff: defaultBackoff,
		st: &sharedState{
			health:     newHealthTracker(cfg.Health, cfg.providerNames()),
			discovered: map[string][]ModelInfo{},
			auto:       map[string]Target{},
		},
	}
	for name, p := range cfg.Providers {
		base, _ := NormalizeBaseURL(p.BaseURL) // validated above
		c.bases[name] = base
		hc, err := providerHTTPClient(p)
		if err != nil {
			return nil, fmt.Errorf("llm: provider %q: %w", name, err)
		}
		c.clients[name] = hc
	}
	c.defaultProvider = cfg.defaultProvider()
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func providerHTTPClient(p ProviderConfig) (*http.Client, error) {
	if p.CAFile == "" {
		return &http.Client{}, nil
	}
	pem, err := os.ReadFile(p.CAFile)
	if err != nil {
		return nil, fmt.Errorf("ca_file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("ca_file %s: no PEM certificates found", p.CAFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: tr}, nil
}

func defaultBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond << attempt
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

// Config returns the client's configuration.
func (c *Client) Config() Config { return c.cfg }

// DefaultProvider returns the provider used for raw model names.
func (c *Client) DefaultProvider() string { return c.defaultProvider }

// WithDefaultProvider returns a copy whose unknown-alias provider is name
// (if configured; otherwise the receiver is returned unchanged). The copy
// shares health and discovery state.
func (c *Client) WithDefaultProvider(name string) kernel.LLM {
	if !c.HasProvider(name) {
		return c
	}
	cp := *c
	cp.defaultProvider = name
	return &cp
}

// HasProvider reports whether a provider is configured.
func (c *Client) HasProvider(name string) bool {
	_, ok := c.cfg.Providers[name]
	return ok
}

func (c *Client) baseURL(provider string) string { return c.bases[provider] }

func (c *Client) httpClient(provider string) *http.Client {
	if c.httpc != nil {
		return c.httpc
	}
	if hc := c.clients[provider]; hc != nil {
		return hc
	}
	return http.DefaultClient
}

// ---- alias resolution ----

// Resolve maps a model alias to its primary provider and concrete model
// without network access (auto aliases discovered earlier are used).
func (c *Client) Resolve(model string) (provider, concrete string, err error) {
	ts, err := c.chain(context.Background(), model, false)
	if err != nil {
		return "", "", err
	}
	return ts[0].Provider, ts[0].Model, nil
}

// ResolveChain returns the ordered targets (primary, then fallbacks) for a
// model alias, running model discovery when aliases_auto needs it.
func (c *Client) ResolveChain(ctx context.Context, model string) ([]Target, error) {
	return c.chain(ctx, model, true)
}

func (c *Client) chain(ctx context.Context, model string, discover bool) ([]Target, error) {
	if model == "" {
		return nil, errors.New("llm: empty model")
	}
	a, configured := c.cfg.Aliases[model]
	if !configured {
		if c.cfg.AliasesAuto && len(c.cfg.preferences(model)) > 0 {
			if t, ok := c.autoTarget(ctx, model, discover); ok {
				return []Target{t}, nil
			}
		}
		if c.defaultProvider == "" {
			return nil, errors.New("llm: no default provider")
		}
		return []Target{{c.defaultProvider, model}}, nil
	}
	provider := a.Provider
	if provider == "" {
		provider = c.defaultProvider
	}
	var out []Target
	switch {
	case !isPlaceholder(a.Model):
		out = append(out, Target{provider, a.Model})
	case strings.TrimSpace(a.Model) == "" && !c.cfg.AliasesAuto:
		// Legacy: an alias without a model uses the alias name as model.
		out = append(out, Target{provider, model})
	case c.cfg.AliasesAuto:
		if t, ok := c.autoTarget(ctx, model, discover); ok {
			out = append(out, t)
		}
	}
	for _, f := range a.Fallback {
		p := f.Provider
		if p == "" {
			p = c.defaultProvider
		}
		out = append(out, Target{p, f.Model})
	}
	if len(out) == 0 {
		if c.cfg.AliasesAuto {
			msg := "no discovered model matches its preferences"
			if err := c.discoveryErr(); err != nil {
				msg = "model discovery failed: " + err.Error()
			}
			return nil, fmt.Errorf("llm: alias %q has no model (%s); set llm.aliases.%s.model", model, msg, model)
		}
		return nil, fmt.Errorf("llm: alias %q has placeholder model %q; set llm.aliases.%s.model or enable aliases_auto", model, a.Model, model)
	}
	return out, nil
}

// orderTargets puts targets whose provider is cooling down last (they are
// still tried, as a last resort, when everything else fails).
func (c *Client) orderTargets(ts []Target) []Target {
	if len(ts) < 2 {
		return ts
	}
	var ok, cooling []Target
	for _, t := range ts {
		if c.st.health.available(t.Provider) {
			ok = append(ok, t)
		} else {
			cooling = append(cooling, t)
		}
	}
	return append(ok, cooling...)
}

// ---- HTTP with retries ----

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func (c *Client) setHeaders(hreq *http.Request, pc ProviderConfig) {
	if pc.api() == APIAnthropic {
		hreq.Header.Set("anthropic-version", pc.anthropicVersion())
	}
	mode, header := pc.authMode()
	if mode != AuthNone && pc.APIKeyEnv != "" {
		if key := c.getenv(pc.APIKeyEnv); key != "" {
			if mode == AuthHeader {
				hreq.Header.Set(header, key)
			} else {
				hreq.Header.Set("Authorization", "Bearer "+key)
			}
		}
	}
	for k, v := range pc.Headers {
		hreq.Header.Set(k, v)
	}
}

// do sends a request to provider path, retrying on 429/5xx/network errors.
// body nil means GET. The returned response has a 2xx status; the caller
// must close its body.
func (c *Client) do(ctx context.Context, provider, path string, body []byte) (*http.Response, error) {
	pc, ok := c.cfg.Providers[provider]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
	url := c.baseURL(provider) + path
	timeout := pc.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	maxRetries := c.cfg.maxRetries()
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		hreq, err := http.NewRequestWithContext(rctx, method, url, rd)
		if err != nil {
			cancel()
			return nil, err
		}
		if body != nil {
			hreq.Header.Set("Content-Type", "application/json")
			hreq.Header.Set("Accept", "application/json, text/event-stream")
		} else {
			hreq.Header.Set("Accept", "application/json")
		}
		c.setHeaders(hreq, pc)
		resp, err := c.httpClient(provider).Do(hreq)
		var retryAfter time.Duration
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("llm: %s: %w", provider, err)
			if isTLSError(err) {
				return nil, lastErr // retrying will not fix certificates
			}
		} else if resp.StatusCode/100 == 2 {
			resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		} else {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			cancel()
			apiErr := &APIError{Provider: provider, StatusCode: resp.StatusCode, Message: errorMessage(msg), URL: url}
			if !apiErr.Retryable() {
				return nil, apiErr
			}
			lastErr = apiErr
			if s := resp.Header.Get("Retry-After"); s != "" {
				if secs, perr := strconv.Atoi(strings.TrimSpace(s)); perr == nil && secs >= 0 {
					retryAfter = time.Duration(secs) * time.Second
					if retryAfter > 60*time.Second {
						retryAfter = 60 * time.Second
					}
				}
			}
		}
		if attempt >= maxRetries {
			return nil, lastErr
		}
		wait := c.backoff(attempt)
		if retryAfter > 0 {
			wait = retryAfter
		}
		c.log.Debug("llm: retrying", "provider", provider, "attempt", attempt+1, "wait", wait, "err", lastErr)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func isTLSError(err error) bool {
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	var rh tls.RecordHeaderError
	var cv *tls.CertificateVerificationError
	return errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) || errors.As(err, &rh) || errors.As(err, &cv)
}

func errorMessage(body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil {
		if len(e.Error) > 0 {
			var we wireError
			if json.Unmarshal(e.Error, &we) == nil && we.Message != "" {
				return we.Message
			}
			var s string
			if json.Unmarshal(e.Error, &s) == nil && s != "" {
				return s
			}
		}
		if e.Message != "" {
			return e.Message
		}
		if len(e.Detail) > 0 {
			var s string
			if json.Unmarshal(e.Detail, &s) == nil && s != "" {
				return s
			}
			return string(e.Detail)
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "(empty body)"
	}
	if strings.HasPrefix(strings.ToLower(s), "<!doctype") || strings.HasPrefix(strings.ToLower(s), "<html") {
		return "(HTML page — the base_url probably points at a web UI, not the API)"
	}
	return truncate(s, 300)
}

// ---- cost ----

type usage struct {
	input, output int64
}

func (c *Client) cost(provider, model string, u *usage) *nexusv1.Cost {
	if u == nil {
		return nil
	}
	cost := &nexusv1.Cost{
		InputTokens:  u.input,
		OutputTokens: u.output,
		Model:        model,
		Amount:       &nexusv1.Money{Currency: c.cfg.currency()},
	}
	p, ok := c.cfg.Prices[provider+"/"+model]
	if !ok {
		p, ok = c.cfg.Prices[model]
	}
	if ok {
		major := (float64(u.input)*p.Input + float64(u.output)*p.Output) / 1e6
		// Round up to whole minor units: budgets must never under-count spend.
		cost.Amount.MinorUnits = int64(math.Ceil(major*100 - 1e-9))
	}
	return cost
}

// ---- kernel.LLM ----

// Stop reasons (normalised across APIs).
const (
	StopEnd           = "stop"
	StopLength        = "length"
	StopToolCalls     = "tool_calls"
	StopContentFilter = "content_filter"
)

func normalizeStop(s string) string {
	switch s {
	case "stop", "end_turn", "stop_sequence", "eos":
		return StopEnd
	case "length", "max_tokens", "model_context_window_exceeded":
		return StopLength
	case "tool_calls", "tool_use", "function_call":
		return StopToolCalls
	case "content_filter", "refusal", "safety":
		return StopContentFilter
	}
	return s
}

// ChatResult is a chat response with routing details.
type ChatResult struct {
	Response *kernel.ChatResponse
	// Target actually used.
	Target Target
	// StopReason is normalised: stop, length, tool_calls, content_filter
	// (other provider values pass through).
	StopReason string
	Latency    time.Duration
	// Deltas is the number of streamed content deltas.
	Deltas int
	// Fallbacks is how many targets failed before this one.
	Fallbacks int
}

// Chat performs a non-streaming chat completion.
func (c *Client) Chat(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	res, err := c.ChatEx(ctx, req, false, nil)
	if err != nil {
		return nil, err
	}
	return res.Response, nil
}

// ChatStream performs a streaming chat completion, calling onDelta for
// each content delta. Tool call deltas are assembled.
func (c *Client) ChatStream(ctx context.Context, req kernel.ChatRequest, onDelta func(string)) (*kernel.ChatResponse, error) {
	res, err := c.ChatEx(ctx, req, true, onDelta)
	if err != nil {
		return nil, err
	}
	return res.Response, nil
}

// ChatEx resolves req.Model to its target chain and tries each target in
// turn (healthy providers first). A streaming call only falls back while
// no content has been delivered.
func (c *Client) ChatEx(ctx context.Context, req kernel.ChatRequest, stream bool, onDelta func(string)) (*ChatResult, error) {
	targets, err := c.chain(ctx, req.Model, true)
	if err != nil {
		return nil, err
	}
	targets = c.orderTargets(targets)
	emitted := false
	wrapped := func(s string) {
		emitted = true
		if onDelta != nil {
			onDelta(s)
		}
	}
	var errs []error
	for i, t := range targets {
		res, err := c.ChatTarget(ctx, t, req, stream, wrapped)
		if err == nil {
			res.Fallbacks = i
			return res, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if emitted || !shouldFallback(err) || i == len(targets)-1 {
			break
		}
		c.st.health.fellBack(t.Provider)
		c.log.Warn("llm: falling back", "model", req.Model, "from", t.String(), "to", targets[i+1].String(), "err", err)
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, &ChainError{Model: req.Model, Errs: errs}
}

// ChatTarget sends req to one concrete target (no alias resolution, no
// fallback) and records provider health.
func (c *Client) ChatTarget(ctx context.Context, t Target, req kernel.ChatRequest, stream bool, onDelta func(string)) (*ChatResult, error) {
	pc, ok := c.cfg.Providers[t.Provider]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", t.Provider)
	}
	deltas := 0
	counted := func(s string) {
		deltas++
		if onDelta != nil {
			onDelta(s)
		}
	}
	start := time.Now()
	var res *ChatResult
	var err error
	if pc.api() == APIAnthropic {
		res, err = c.anthropicChat(ctx, t, pc, req, stream, counted)
	} else {
		res, err = c.openaiChat(ctx, t, pc, req, stream, counted)
	}
	lat := time.Since(start)
	c.record(ctx, t.Provider, err, lat)
	if err != nil {
		return nil, err
	}
	res.Target = t
	res.Latency = lat
	res.Deltas = deltas
	return res, nil
}

func (c *Client) record(ctx context.Context, provider string, err error, lat time.Duration) {
	switch {
	case err == nil:
		c.st.health.success(provider, lat)
	case ctx.Err() != nil:
		// caller gave up; says nothing about the provider
	case isProviderFailure(err):
		if c.st.health.failure(provider, err) {
			s := c.st.health.snapshot(provider)
			c.log.Warn("llm: provider degraded", "provider", provider, "cooldown", s.Cooldown, "failures", s.ConsecutiveFailures, "err", err)
		}
	default:
		c.st.health.neutral(provider)
	}
}

// Embed returns vectors in input order. Fallbacks are only used when they
// serve the same model name: vectors from different models are not
// comparable.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	targets, err := c.chain(ctx, model, true)
	if err != nil {
		return nil, err
	}
	same := targets[:1]
	for _, t := range targets[1:] {
		if t.Model == targets[0].Model {
			same = append(same, t)
		}
	}
	same = c.orderTargets(same)
	var errs []error
	for i, t := range same {
		vecs, err := c.EmbedTarget(ctx, t, inputs)
		if err == nil {
			return vecs, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !shouldFallback(err) || i == len(same)-1 {
			break
		}
		c.st.health.fellBack(t.Provider)
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, &ChainError{Model: model, Errs: errs}
}

// EmbedTarget embeds inputs with one concrete target.
func (c *Client) EmbedTarget(ctx context.Context, t Target, inputs []string) ([][]float32, error) {
	pc, ok := c.cfg.Providers[t.Provider]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", t.Provider)
	}
	if pc.api() == APIAnthropic {
		return nil, fmt.Errorf("llm: %s: %w", t.Provider, ErrNoEmbeddings)
	}
	start := time.Now()
	vecs, err := c.openaiEmbed(ctx, t, inputs)
	c.record(ctx, t.Provider, err, time.Since(start))
	return vecs, err
}
