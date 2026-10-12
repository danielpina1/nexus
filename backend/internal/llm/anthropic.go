package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Anthropic Messages API (POST {base}/messages).

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type antMsg struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      string    `json:"system,omitempty"`
	Messages    []antMsg  `json:"messages"`
	Tools       []antTool `json:"tools,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type antUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (u *antUsage) input() int64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

type antResponse struct {
	Type       string     `json:"type"`
	Model      string     `json:"model"`
	StopReason string     `json:"stop_reason"`
	Content    []antBlock `json:"content"`
	Usage      *antUsage  `json:"usage"`
	Error      *wireError `json:"error"`
}

const jsonInstruction = "Respond with a single valid JSON object and nothing else: no prose, no Markdown code fences."

// buildAnthropic converts a provider-neutral request. System messages are
// hoisted into "system"; tool results become user tool_result blocks;
// consecutive messages of the same role are merged (the API requires
// alternation).
func buildAnthropic(req kernel.ChatRequest, model string, stream bool, pc ProviderConfig) (*antRequest, *nameMap) {
	nm := newNameMap(req.Tools)
	ar := &antRequest{Model: model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, Stream: stream}
	if ar.MaxTokens <= 0 {
		ar.MaxTokens = pc.maxTokens()
	}
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, antTool{Name: nm.add(t.Name), Description: t.Description, InputSchema: toolParams(t.InputSchema)})
	}
	var system []string
	push := func(role string, blocks ...antBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(ar.Messages); n > 0 && ar.Messages[n-1].Role == role {
			ar.Messages[n-1].Content = append(ar.Messages[n-1].Content, blocks...)
			return
		}
		ar.Messages = append(ar.Messages, antMsg{Role: role, Content: blocks})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if strings.TrimSpace(m.Content) != "" {
				system = append(system, m.Content)
			}
		case "assistant":
			var blocks []antBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, antBlock{Type: "text", Text: m.Content})
			}
			for i, tc := range m.ToolCalls {
				input := json.RawMessage(strings.TrimSpace(tc.Arguments))
				if len(input) == 0 || !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("toolu_%d", i)
				}
				blocks = append(blocks, antBlock{Type: "tool_use", ID: id, Name: nm.add(tc.Name), Input: input})
			}
			push("assistant", blocks...)
		case "tool":
			push("user", antBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		default: // user and anything unknown
			if strings.TrimSpace(m.Content) != "" {
				push("user", antBlock{Type: "text", Text: m.Content})
			}
		}
	}
	if req.JSON {
		system = append(system, jsonInstruction)
	}
	ar.System = strings.Join(system, "\n\n")
	return ar, nm
}

// stripFences removes a Markdown code fence around a JSON answer.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	return strings.TrimSpace(t)
}

func (c *Client) anthropicChat(ctx context.Context, t Target, pc ProviderConfig, req kernel.ChatRequest, stream bool, onDelta func(string)) (*ChatResult, error) {
	ar, nm := buildAnthropic(req, t.Model, stream, pc)
	body, err := json.Marshal(ar)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, t.Provider, "/messages", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out *antResponse
	if !stream || strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		out = &antResponse{}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("llm: %s: decode response: %w", t.Provider, err)
		}
		if out.Error != nil && out.Error.Message != "" {
			return nil, &APIError{Provider: t.Provider, Message: out.Error.Message}
		}
		if out.Type != "" && out.Type != "message" {
			return nil, fmt.Errorf("llm: %s: unexpected response type %q", t.Provider, out.Type)
		}
		if onDelta != nil {
			for _, b := range out.Content {
				if b.Type == "text" && b.Text != "" {
					onDelta(b.Text)
				}
			}
		}
	} else {
		acc := &antStream{onDelta: onDelta, blocks: map[int]*antStreamBlock{}}
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
		out = acc.response()
	}
	msg := kernel.ChatMessage{Role: "assistant"}
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			args := strings.TrimSpace(string(b.Input))
			if args == "" || args == "null" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, kernel.LLMToolCall{ID: b.ID, Name: nm.back(b.Name), Arguments: args})
		}
	}
	msg.Content = text.String()
	if req.JSON {
		msg.Content = stripFences(msg.Content)
	}
	var u *usage
	if out.Usage != nil {
		u = &usage{input: out.Usage.input(), output: out.Usage.OutputTokens}
	}
	return &ChatResult{
		Response:   &kernel.ChatResponse{Message: msg, Cost: c.cost(t.Provider, t.Model, u)},
		StopReason: normalizeStop(out.StopReason),
	}, nil
}

// ---- streaming ----

type antStreamBlock struct {
	typ, id, name string
	text          strings.Builder
	json          strings.Builder
	input         json.RawMessage // from content_block_start
}

type antStream struct {
	onDelta func(string)
	blocks  map[int]*antStreamBlock
	stop    string
	usage   *antUsage
}

type antEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *antUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *antBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *antUsage       `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (s *antStream) handle(data []byte) error {
	var ev antEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("bad event %q: %w", truncate(string(data), 200), err)
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			u := *ev.Message.Usage
			s.usage = &u
		}
	case "content_block_start":
		b := &antStreamBlock{}
		if ev.ContentBlock != nil {
			b.typ, b.id, b.name = ev.ContentBlock.Type, ev.ContentBlock.ID, ev.ContentBlock.Name
			b.input = ev.ContentBlock.Input
			if ev.ContentBlock.Text != "" {
				b.text.WriteString(ev.ContentBlock.Text)
				if s.onDelta != nil {
					s.onDelta(ev.ContentBlock.Text)
				}
			}
		}
		s.blocks[ev.Index] = b
	case "content_block_delta":
		if ev.Delta == nil {
			return nil
		}
		b, ok := s.blocks[ev.Index]
		if !ok {
			b = &antStreamBlock{typ: "text"}
			s.blocks[ev.Index] = b
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				b.text.WriteString(ev.Delta.Text)
				if s.onDelta != nil {
					s.onDelta(ev.Delta.Text)
				}
			}
		case "input_json_delta":
			b.json.WriteString(ev.Delta.PartialJSON)
		}
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			if s.usage == nil {
				s.usage = &antUsage{}
			}
			// message_delta usage is cumulative.
			s.usage.OutputTokens = ev.Usage.OutputTokens
			if ev.Usage.InputTokens > 0 {
				s.usage.InputTokens = ev.Usage.InputTokens
			}
		}
	case "message_stop":
		return errStreamDone
	case "error":
		return &APIError{Message: "stream error: " + errorMessage(data)}
	}
	return nil
}

func (s *antStream) response() *antResponse {
	idx := make([]int, 0, len(s.blocks))
	for i := range s.blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := &antResponse{Type: "message", StopReason: s.stop, Usage: s.usage}
	for _, i := range idx {
		b := s.blocks[i]
		switch b.typ {
		case "text":
			out.Content = append(out.Content, antBlock{Type: "text", Text: b.text.String()})
		case "tool_use":
			input := json.RawMessage(b.json.String())
			if len(bytes.TrimSpace(input)) == 0 {
				input = b.input
			}
			out.Content = append(out.Content, antBlock{Type: "tool_use", ID: b.id, Name: b.name, Input: input})
		}
	}
	return out
}
