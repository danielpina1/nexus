package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/danielpina1/nexus/backend/kernel"
)

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

func (u *wireUsage) toUsage() *usage {
	if u == nil {
		return nil
	}
	return &usage{input: u.PromptTokens, output: u.CompletionTokens}
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
// OpenAI and Anthropic tool names must match ^[a-zA-Z0-9_-]{1,64}$, while
// Nexus tool names are dot-separated ("memory.search"). Names are sanitised
// on the way out and mapped back on the way in, so callers only see Nexus
// names.

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

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// toolParams returns a tool's JSON schema, defaulting to an empty object.
func toolParams(schema json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(schema)
	if len(t) == 0 || string(t) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return schema
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
		wr.Tools = append(wr.Tools, wireTool{
			Type: "function",
			Function: wireFuncDef{
				Name:        nm.add(t.Name),
				Description: t.Description,
				Parameters:  toolParams(t.InputSchema),
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

func (c *Client) openaiChat(ctx context.Context, t Target, pc ProviderConfig, req kernel.ChatRequest, stream bool, onDelta func(string)) (*ChatResult, error) {
	wr, nm := c.buildRequest(req, t.Model, stream, pc)
	body, err := json.Marshal(wr)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, t.Provider, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Some servers ignore stream=true and answer with plain JSON.
	if !stream || strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return c.decodeResponse(resp, t, nm, onDelta)
	}
	acc := newStreamAccumulator(onDelta)
	if err := readSSE(resp.Body, acc.handle); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var apiErr *APIError
		if asAPI(err, &apiErr) {
			apiErr.Provider = t.Provider
			return nil, apiErr
		}
		return nil, fmt.Errorf("llm: %s: stream: %w", t.Provider, err)
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
	stop := normalizeStop(acc.finish)
	if stop == "" && len(msg.ToolCalls) > 0 {
		stop = StopToolCalls
	}
	return &ChatResult{
		Response:   &kernel.ChatResponse{Message: msg, Cost: c.cost(t.Provider, t.Model, acc.usage.toUsage())},
		StopReason: stop,
	}, nil
}

func (c *Client) decodeResponse(resp *http.Response, t Target, nm *nameMap, onDelta func(string)) (*ChatResult, error) {
	var out wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm: %s: decode response: %w", t.Provider, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, &APIError{Provider: t.Provider, Message: out.Error.Message}
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: %s: response has no choices", t.Provider)
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
	stop := normalizeStop(out.Choices[0].FinishReason)
	if stop == "" && len(msg.ToolCalls) > 0 {
		stop = StopToolCalls
	}
	return &ChatResult{
		Response:   &kernel.ChatResponse{Message: msg, Cost: c.cost(t.Provider, t.Model, out.Usage.toUsage())},
		StopReason: stop,
	}, nil
}

func (c *Client) openaiEmbed(ctx context.Context, t Target, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": t.Model, "input": inputs})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, t.Provider, "/embeddings", body)
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
		return nil, fmt.Errorf("llm: %s: decode embeddings: %w", t.Provider, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, &APIError{Provider: t.Provider, Message: out.Error.Message}
	}
	if len(out.Data) != len(inputs) {
		return nil, fmt.Errorf("llm: %s: got %d embeddings for %d inputs", t.Provider, len(out.Data), len(inputs))
	}
	sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
