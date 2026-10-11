package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
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

// APIError is a non-2xx response from a provider.
type APIError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: %s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Retryable reports whether the request may succeed when retried.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Client implements kernel.LLM.
type Client struct {
	cfg             Config
	httpc           *http.Client
	log             *slog.Logger
	getenv          func(string) string
	backoff         func(attempt int) time.Duration
	defaultProvider string
}

var (
	_ kernel.LLM     = (*Client)(nil)
	_ ProviderScoper = (*Client)(nil)
)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (tests, custom transports).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpc = h } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithGetenv overrides environment lookup for API keys.
func WithGetenv(f func(string) string) Option { return func(c *Client) { c.getenv = f } }

// WithBackoff overrides the retry delay function (Retry-After still wins).
func WithBackoff(f func(attempt int) time.Duration) Option {
	return func(c *Client) { c.backoff = f }
}

// New builds a client. It fails when no providers are configured or an alias
// points at an unknown provider.
func New(cfg Config, opts ...Option) (*Client, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("llm: no providers configured")
	}
	for name, p := range cfg.Providers {
		if strings.TrimSpace(p.BaseURL) == "" {
			return nil, fmt.Errorf("llm: provider %q has no base_url", name)
		}
	}
	for alias, a := range cfg.Aliases {
		if a.Provider != "" {
			if _, ok := cfg.Providers[a.Provider]; !ok {
				return nil, fmt.Errorf("llm: alias %q uses unknown provider %q", alias, a.Provider)
			}
		}
	}
	c := &Client{
		cfg:     cfg,
		httpc:   &http.Client{},
		log:     slog.Default(),
		getenv:  os.Getenv,
		backoff: defaultBackoff,
	}
	c.defaultProvider = cfg.defaultProvider()
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func defaultBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond << attempt
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

// WithDefaultProvider returns a copy whose unknown-alias provider is name
// (if configured; otherwise the receiver is returned unchanged).
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

// Resolve maps a model alias to provider and concrete model.
func (c *Client) Resolve(model string) (provider, concrete string, err error) {
	if a, ok := c.cfg.Aliases[model]; ok {
		provider = a.Provider
		if provider == "" {
			provider = c.defaultProvider
		}
		concrete = a.Model
		if concrete == "" {
			concrete = model
		}
		return provider, concrete, nil
	}
	if model == "" {
		return "", "", errors.New("llm: empty model")
	}
	if c.defaultProvider == "" {
		return "", "", errors.New("llm: no default provider")
	}
	return c.defaultProvider, model, nil
}

// ---- wire types (OpenAI chat completions) ----

type wireFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	Index    *int     `json:"index,omitempty"`
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Function wireFunc `json:"function"`
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall  `json:"tool_calls,omitempty"`
}

type wireFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireTool struct {
	Type     string      `json:"type"`
	Function wireFuncDef `json:"function"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireRequest struct {
	Model          string          `json:"model"`
	Messages       []wireMessage   `json:"messages"`
	Tools          []wireTool      `json:"tools,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
}

type wireUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *wireError `json:"error"`
}

// ---- tool name mapping ----
//
// OpenAI function names must match ^[a-zA-Z0-9_-]{1,64}$, while Nexus tool
// names are dot-separated ("memory.search"). Names are sanitised on the way
// out and mapped back on the way in, so callers only see Nexus names.

type nameMap struct {
	toWire   map[string]string
	fromWire map[string]string
}

func sanitizeName(n string) string {
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" {
		s = "tool"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func newNameMap(specs []kernel.ToolSpec) *nameMap {
	m := &nameMap{toWire: map[string]string{}, fromWire: map[string]string{}}
	for _, s := range specs {
		m.add(s.Name)
	}
	return m
}

func (m *nameMap) add(name string) string {
	if w, ok := m.toWire[name]; ok {
		return w
	}
	w := sanitizeName(name)
	base := w
	for i := 2; ; i++ {
		if _, taken := m.fromWire[w]; !taken {
			break
		}
		suffix := "_" + strconv.Itoa(i)
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		w = base + suffix
	}
	m.toWire[name] = w
	m.fromWire[w] = name
	return w
}

func (m *nameMap) back(w string) string {
	if n, ok := m.fromWire[w]; ok {
		return n
	}
	return w
}

// ---- request building ----

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func (c *Client) buildRequest(req kernel.ChatRequest, model string, stream bool, pc ProviderConfig) (*wireRequest, *nameMap) {
	nm := newNameMap(req.Tools)
	wr := &wireRequest{
		Model:       model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}
	if stream && !pc.NoStreamUsage {
		wr.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.JSON {
		wr.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	for _, t := range req.Tools {
		params := t.InputSchema
		if len(bytes.TrimSpace(params)) == 0 || string(bytes.TrimSpace(params)) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wr.Tools = append(wr.Tools, wireTool{
			Type: "function",
			Function: wireFuncDef{
				Name:        nm.add(t.Name),
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID}
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0 {
			wm.Content = json.RawMessage("null")
		} else {
			wm.Content = jsonString(m.Content)
		}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: wireFunc{Name: nm.add(tc.Name), Arguments: args},
			})
		}
		wr.Messages = append(wr.Messages, wm)
	}
	return wr, nm
}

// contentText decodes an OpenAI content field (string, null, or an array of
// {type:"text", text} parts).
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
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

// post sends body to provider path, retrying on 429/5xx/network errors.
// The returned response has a 2xx status; the caller must close its body.
func (c *Client) post(ctx context.Context, provider, path string, body []byte) (*http.Response, error) {
	pc := c.cfg.Providers[provider]
	url := strings.TrimRight(pc.BaseURL, "/") + path
	timeout := pc.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxRetries := c.cfg.maxRetries()
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		hreq, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", "application/json, text/event-stream")
		if pc.APIKeyEnv != "" {
			if key := c.getenv(pc.APIKeyEnv); key != "" {
				hreq.Header.Set("Authorization", "Bearer "+key)
			}
		}
		for k, v := range pc.Headers {
			hreq.Header.Set(k, v)
		}
		resp, err := c.httpc.Do(hreq)
		var retryAfter time.Duration
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("llm: %s: %w", provider, err)
		} else if resp.StatusCode/100 == 2 {
			resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		} else {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			cancel()
			apiErr := &APIError{Provider: provider, StatusCode: resp.StatusCode, Message: errorMessage(msg)}
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

func errorMessage(body []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Error) > 0 {
		var we wireError
		if json.Unmarshal(e.Error, &we) == nil && we.Message != "" {
			return we.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "(empty body)"
	}
	return s
}

// ---- cost ----

func (c *Client) cost(provider, model string, u *wireUsage) *nexusv1.Cost {
	if u == nil {
		return nil
	}
	cost := &nexusv1.Cost{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		Model:        model,
		Amount:       &nexusv1.Money{Currency: c.cfg.currency()},
	}
	p, ok := c.cfg.Prices[provider+"/"+model]
	if !ok {
		p, ok = c.cfg.Prices[model]
	}
	if ok {
		major := (float64(u.PromptTokens)*p.Input + float64(u.CompletionTokens)*p.Output) / 1e6
		// Round up to whole minor units: budgets must never under-count spend.
		cost.Amount.MinorUnits = int64(math.Ceil(major*100 - 1e-9))
	}
	return cost
}

// ---- kernel.LLM ----

// Chat performs a non-streaming chat completion.
func (c *Client) Chat(ctx context.Context, req kernel.ChatRequest) (*kernel.ChatResponse, error) {
	provider, model, err := c.Resolve(req.Model)
	if err != nil {
		return nil, err
	}
	wr, nm := c.buildRequest(req, model, false, c.cfg.Providers[provider])
	body, err := json.Marshal(wr)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, provider, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, provider, model, nm, nil)
}

func (c *Client) decodeResponse(resp *http.Response, provider, model string, nm *nameMap, onDelta func(string)) (*kernel.ChatResponse, error) {
	var out wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm: %s: decode response: %w", provider, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, &APIError{Provider: provider, StatusCode: resp.StatusCode, Message: out.Error.Message}
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: %s: response has no choices", provider)
	}
	m := out.Choices[0].Message
	msg := kernel.ChatMessage{Role: "assistant", Content: contentText(m.Content)}
	for i, tc := range m.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		msg.ToolCalls = append(msg.ToolCalls, kernel.LLMToolCall{
			ID: id, Name: nm.back(tc.Function.Name), Arguments: tc.Function.Arguments,
		})
	}
	if msg.Content != "" && onDelta != nil {
		onDelta(msg.Content)
	}
	return &kernel.ChatResponse{Message: msg, Cost: c.cost(provider, model, out.Usage)}, nil
}

// ChatStream performs a streaming chat completion (SSE), calling onDelta for
// each content delta. Tool call deltas are assembled by index.
func (c *Client) ChatStream(ctx context.Context, req kernel.ChatRequest, onDelta func(string)) (*kernel.ChatResponse, error) {
	provider, model, err := c.Resolve(req.Model)
	if err != nil {
		return nil, err
	}
	wr, nm := c.buildRequest(req, model, true, c.cfg.Providers[provider])
	body, err := json.Marshal(wr)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, provider, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Some servers ignore stream=true and answer with plain JSON.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		return c.decodeResponse(resp, provider, model, nm, onDelta)
	}

	acc := newStreamAccumulator(onDelta)
	if err := readSSE(resp.Body, acc.handle); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("llm: %s: stream: %w", provider, err)
	}
	msg := kernel.ChatMessage{Role: "assistant", Content: acc.content.String()}
	for i, tc := range acc.toolCalls() {
		id := tc.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		msg.ToolCalls = append(msg.ToolCalls, kernel.LLMToolCall{
			ID: id, Name: nm.back(tc.name.String()), Arguments: tc.args.String(),
		})
	}
	return &kernel.ChatResponse{Message: msg, Cost: c.cost(provider, model, acc.usage)}, nil
}

// Embed calls /embeddings and returns vectors in input order.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	provider, concrete, err := c.Resolve(model)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"model": concrete, "input": inputs})
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, provider, "/embeddings", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error *wireError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm: %s: decode embeddings: %w", provider, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, &APIError{Provider: provider, StatusCode: resp.StatusCode, Message: out.Error.Message}
	}
	if len(out.Data) != len(inputs) {
		return nil, fmt.Errorf("llm: %s: got %d embeddings for %d inputs", provider, len(out.Data), len(inputs))
	}
	sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
