package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
)

func anthropicClient(t *testing.T, url string) *Client {
	t.Helper()
	zero := 0
	c, err := New(Config{
		MaxRetries: &zero,
		Providers:  map[string]ProviderConfig{"cloud": {BaseURL: url, API: "anthropic", APIKeyEnv: "K"}},
		Aliases:    map[string]Alias{"smart": {Provider: "cloud", Model: "claude-x"}},
		Prices:     map[string]Price{"claude-x": {Input: 3, Output: 15}},
	}, WithGetenv(func(string) string { return "ak" }))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnthropicChatToolsAndRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "ak" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers %v", r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","model":"claude-x","stop_reason":"tool_use",
			"content":[{"type":"text","text":"Let me search."},
			           {"type":"tool_use","id":"toolu_1","name":"memory_search","input":{"q":"milk"}}],
			"usage":{"input_tokens":900000,"cache_read_input_tokens":100000,"output_tokens":100000}}`)
	}))
	defer srv.Close()
	c := anthropicClient(t, srv.URL)
	res, err := c.ChatEx(context.Background(), kernel.ChatRequest{
		Model: "smart",
		Messages: []kernel.ChatMessage{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "find milk"},
			{Role: "assistant", ToolCalls: []kernel.LLMToolCall{{ID: "t0", Name: "vault.read", Arguments: `{"p":"a"}`}, {ID: "t1", Name: "vault.read"}}},
			{Role: "tool", ToolCallID: "t0", Content: "A"},
			{Role: "tool", ToolCallID: "t1", Content: "B"},
			{Role: "user", Content: "and?"},
		},
		Tools: []kernel.ToolSpec{{Name: "memory.search", Description: "search"}, {Name: "vault.read"}},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["system"] != "be brief" || got["model"] != "claude-x" || got["max_tokens"].(float64) != 4096 {
		t.Errorf("request %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user, assistant, user(tool results + text); got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if b := asst[0].(map[string]any); b["type"] != "tool_use" || b["name"] != "vault_read" || b["id"] != "t0" {
		t.Errorf("tool_use block %v", b)
	}
	if b := asst[1].(map[string]any); fmt.Sprint(b["input"]) != "map[]" {
		t.Errorf("empty args should be {}: %v", b["input"])
	}
	last := msgs[2].(map[string]any)["content"].([]any)
	if len(last) != 3 || last[0].(map[string]any)["type"] != "tool_result" || last[1].(map[string]any)["tool_use_id"] != "t1" ||
		last[2].(map[string]any)["text"] != "and?" {
		t.Errorf("merged user message %v", last)
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "memory_search" || tool["input_schema"].(map[string]any)["type"] != "object" {
		t.Errorf("tool %v", tool)
	}
	m := res.Response.Message
	if m.Content != "Let me search." || len(m.ToolCalls) != 1 || m.ToolCalls[0].Name != "memory.search" ||
		m.ToolCalls[0].Arguments != `{"q":"milk"}` || m.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("message %+v", m)
	}
	if res.StopReason != StopToolCalls {
		t.Errorf("stop %q", res.StopReason)
	}
	// (0.9M+0.1M)*3 + 0.1M*15 = 4.50
	if res.Response.Cost.InputTokens != 1000000 || res.Response.Cost.Amount.MinorUnits != 450 {
		t.Errorf("cost %+v", res.Response.Cost)
	}
}

func TestAnthropicJSONMode(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{\"type\":\"message\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"```json\\n{\\\"ok\\\":true}\\n```\"}]}")
	}))
	defer srv.Close()
	res, err := anthropicClient(t, srv.URL).ChatEx(context.Background(), kernel.ChatRequest{
		Model: "smart", JSON: true, Messages: []kernel.ChatMessage{{Role: "user", Content: "json pls"}},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["system"].(string), "JSON") {
		t.Errorf("system %v", got["system"])
	}
	if res.Response.Message.Content != `{"ok":true}` || res.StopReason != StopEnd {
		t.Fatalf("content %q stop %q", res.Response.Message.Content, res.StopReason)
	}
}

func antSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for _, e := range events {
		var typ struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
		f.Flush()
	}
}

func TestAnthropicStreamToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		antSSE(w,
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":25,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"ask_user","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"text\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ok?\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_10","name":"noargs","input":{}}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
			`{"type":"message_stop"}`,
		)
	}))
	defer srv.Close()
	var deltas []string
	res, err := anthropicClient(t, srv.URL).ChatEx(context.Background(), kernel.ChatRequest{
		Model: "smart", Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []kernel.ToolSpec{{Name: "ask_user"}, {Name: "noargs"}},
	}, true, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if got["stream"] != true {
		t.Errorf("stream flag %v", got["stream"])
	}
	m := res.Response.Message
	if strings.Join(deltas, "|") != "Hel|lo" || m.Content != "Hello" {
		t.Fatalf("deltas %v content %q", deltas, m.Content)
	}
	if len(m.ToolCalls) != 2 || m.ToolCalls[0].ID != "toolu_9" || m.ToolCalls[0].Arguments != `{"text":"ok?"}` ||
		m.ToolCalls[1].Name != "noargs" || m.ToolCalls[1].Arguments != "{}" {
		t.Fatalf("tool calls %+v", m.ToolCalls)
	}
	if res.StopReason != StopToolCalls || res.Response.Cost.InputTokens != 25 || res.Response.Cost.OutputTokens != 42 {
		t.Fatalf("stop %q cost %+v", res.StopReason, res.Response.Cost)
	}
	if res.Deltas != 2 {
		t.Errorf("deltas count %d", res.Deltas)
	}
}

func TestAnthropicErrors(t *testing.T) {
	t.Run("stream error event", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			antSSE(w, `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
				`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		}))
		defer srv.Close()
		_, err := anthropicClient(t, srv.URL).ChatStream(context.Background(), kernel.ChatRequest{Model: "smart"}, nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !strings.Contains(err.Error(), "Overloaded") || apiErr.Provider != "cloud" {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("401", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
		}))
		defer srv.Close()
		_, err := anthropicClient(t, srv.URL).Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Message != "invalid x-api-key" {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("no embeddings", func(t *testing.T) {
		_, err := anthropicClient(t, "http://127.0.0.1:1").Embed(context.Background(), "smart", []string{"x"})
		if !errors.Is(err, ErrNoEmbeddings) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestAnthropicListModelsPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.URL.Query().Get("after_id") == "" {
			io.WriteString(w, `{"data":[{"id":"claude-b","display_name":"B","type":"model"}],"has_more":true,"last_id":"claude-b"}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"claude-a","display_name":"A","type":"model"}],"has_more":false,"last_id":"claude-a"}`)
	}))
	defer srv.Close()
	ms, err := anthropicClient(t, srv.URL).ListModels(context.Background(), "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "claude-a" || ms[1].DisplayName != "B" {
		t.Fatalf("models %+v", ms)
	}
}
