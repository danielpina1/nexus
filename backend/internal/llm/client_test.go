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
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
	"github.com/danielpina1/nexus/backend/kernel/kerneltest"
)

func testClient(t *testing.T, url string, mut func(*Config)) *Client {
	t.Helper()
	zero := 2
	cfg := Config{
		MaxRetries: &zero,
		Providers: map[string]ProviderConfig{
			"unify":  {BaseURL: url + "/v1", APIKeyEnv: "TEST_KEY", Headers: map[string]string{"X-Client": "nexus"}},
			"ollama": {BaseURL: url + "/ollama/v1"},
		},
		Aliases: map[string]Alias{
			"smart":      {Provider: "unify", Model: "gpt-x"},
			"fast-local": {Provider: "ollama", Model: "qwen:7b"},
		},
		Prices: map[string]Price{"gpt-x": {Input: 2, Output: 8}},
	}
	if mut != nil {
		mut(&cfg)
	}
	c, err := New(cfg,
		WithGetenv(func(k string) string {
			if k == "TEST_KEY" {
				return "secret"
			}
			return ""
		}),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolve(t *testing.T) {
	c := testClient(t, "http://x", nil)
	tests := []struct {
		name, model, wantProv, wantModel string
		scoped                           string
	}{
		{"alias", "smart", "unify", "gpt-x", ""},
		{"local alias", "fast-local", "ollama", "qwen:7b", ""},
		{"raw model default provider", "llama3", "unify", "llama3", ""},
		{"raw model scoped", "llama3", "ollama", "llama3", "ollama"},
		{"alias wins over scope", "smart", "unify", "gpt-x", "ollama"},
		{"unknown scope ignored", "llama3", "unify", "llama3", "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := c
			if tt.scoped != "" {
				cl = c.WithDefaultProvider(tt.scoped).(*Client)
			}
			p, m, err := cl.Resolve(tt.model)
			if err != nil {
				t.Fatal(err)
			}
			if p != tt.wantProv || m != tt.wantModel {
				t.Fatalf("got %s/%s want %s/%s", p, m, tt.wantProv, tt.wantModel)
			}
		})
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for no providers")
	}
	_, err := New(Config{
		Providers: map[string]ProviderConfig{"a": {BaseURL: "http://x"}},
		Aliases:   map[string]Alias{"smart": {Provider: "b", Model: "m"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown alias provider")
	}
}

func TestChatToolCallsAndJSON(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Client") != "nexus" {
			t.Errorf("headers %v", r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"memory_search","arguments":"{\"q\":\"milk\"}"}}]},
			"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1000000,"completion_tokens":500000}}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, nil)
	temp := 0.2
	resp, err := c.Chat(context.Background(), kernel.ChatRequest{
		Model: "smart",
		Messages: []kernel.ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []kernel.LLMToolCall{{ID: "c0", Name: "vault.read", Arguments: ""}}},
			{Role: "tool", ToolCallID: "c0", Content: `{"ok":true}`},
		},
		Tools: []kernel.ToolSpec{
			{Name: "memory.search", Description: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
			{Name: "vault.read"},
		},
		JSON:        true,
		MaxTokens:   100,
		Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-x" {
		t.Errorf("model %v", got["model"])
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format %v", got["response_format"])
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "memory_search" {
		t.Errorf("tool name %v", fn["name"])
	}
	if params := tools[1].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any); params["type"] != "object" {
		t.Errorf("default params %v", params)
	}
	msgs := got["messages"].([]any)
	asst := msgs[2].(map[string]any)
	if asst["content"] != nil {
		t.Errorf("assistant tool-call content should be null, got %v", asst["content"])
	}
	tc := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "vault_read" || tc["arguments"] != "{}" {
		t.Errorf("history tool call %v", tc)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("tool msg %v", msgs[3])
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "memory.search" ||
		resp.Message.ToolCalls[0].Arguments != `{"q":"milk"}` {
		t.Fatalf("tool calls %+v", resp.Message.ToolCalls)
	}
	// 1M in * 2 + 0.5M out * 8 = 6.00 EUR
	if resp.Cost == nil || resp.Cost.Amount.MinorUnits != 600 || resp.Cost.Amount.Currency != "EUR" ||
		resp.Cost.InputTokens != 1000000 || resp.Cost.OutputTokens != 500000 || resp.Cost.Model != "gpt-x" {
		t.Fatalf("cost %+v", resp.Cost)
	}
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		f.Flush()
	}
}

func TestChatStreamContentAndToolCalls(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": keep-alive\n\n")
		sse(w,
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"memory_search","arguments":""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"ask_user","arguments":"{\"text\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"eggs\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"ok?\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`[DONE]`,
		)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, nil)
	var deltas []string
	resp, err := c.ChatStream(context.Background(), kernel.ChatRequest{
		Model:    "smart",
		Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}},
		Tools:    []kernel.ToolSpec{{Name: "memory.search"}, {Name: "ask_user"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if body["stream"] != true || body["stream_options"] == nil {
		t.Errorf("stream flags %v %v", body["stream"], body["stream_options"])
	}
	if strings.Join(deltas, "|") != "Hel|lo" || resp.Message.Content != "Hello" {
		t.Fatalf("deltas %v content %q", deltas, resp.Message.Content)
	}
	tcs := resp.Message.ToolCalls
	if len(tcs) != 2 {
		t.Fatalf("tool calls %+v", tcs)
	}
	if tcs[0].ID != "call_a" || tcs[0].Name != "memory.search" || tcs[0].Arguments != `{"q":"eggs"}` {
		t.Errorf("tc0 %+v", tcs[0])
	}
	if tcs[1].ID != "call_b" || tcs[1].Name != "ask_user" || tcs[1].Arguments != `{"text":"ok?"}` {
		t.Errorf("tc1 %+v", tcs[1])
	}
	if resp.Cost == nil || resp.Cost.InputTokens != 10 || resp.Cost.OutputTokens != 5 || resp.Cost.Amount.MinorUnits != 1 {
		t.Errorf("cost %+v", resp.Cost)
	}
}

func TestChatStreamNoIndexAndNoUsageOption(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"x1","function":{"name":"a","arguments":"{}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"x2","function":{"name":"b","arguments":"{"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"}"}}]}}]}`,
		) // no [DONE]: EOF ends the stream
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, func(cfg *Config) {
		p := cfg.Providers["ollama"]
		p.NoStreamUsage = true
		cfg.Providers["ollama"] = p
	})
	resp, err := c.ChatStream(context.Background(), kernel.ChatRequest{Model: "fast-local"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["stream_options"]; ok {
		t.Errorf("stream_options sent despite no_stream_usage")
	}
	tcs := resp.Message.ToolCalls
	if len(tcs) != 2 || tcs[0].Name != "a" || tcs[1].Name != "b" || tcs[1].Arguments != "{}" {
		t.Fatalf("tool calls %+v", tcs)
	}
	if resp.Cost != nil {
		t.Errorf("expected nil cost without usage, got %+v", resp.Cost)
	}
}

func TestChatStreamErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"error chunk", func(w http.ResponseWriter, r *http.Request) {
			sse(w, `{"choices":[{"index":0,"delta":{"content":"a"}}]}`, `{"error":{"message":"overloaded"}}`)
		}, "overloaded"},
		{"malformed chunk", func(w http.ResponseWriter, r *http.Request) {
			sse(w, `{not json`)
		}, "bad chunk"},
		{"400", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"bad model"}}`)
		}, "bad model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			c := testClient(t, srv.URL, nil)
			_, err := c.ChatStream(context.Background(), kernel.ChatRequest{Model: "smart"}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
}

func TestChatStreamJSONFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"whole"}}]}`)
	}))
	defer srv.Close()
	var deltas []string
	resp, err := testClient(t, srv.URL, nil).ChatStream(context.Background(), kernel.ChatRequest{Model: "smart"},
		func(s string) { deltas = append(deltas, s) })
	if err != nil || resp.Message.Content != "whole" || len(deltas) != 1 {
		t.Fatalf("resp %+v deltas %v err %v", resp, deltas, err)
	}
}

func TestRetries(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		wantCalls int32
		wantErr   bool
	}{
		{"503 then ok", []int{503, 200}, 2, false},
		{"429 then ok", []int{429, 200}, 2, false},
		{"400 not retried", []int{400}, 1, true},
		{"exhausted", []int{500, 502, 503, 504}, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(n.Add(1)) - 1
				st := tt.statuses[min(i, len(tt.statuses)-1)]
				if st != 200 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(st)
					io.WriteString(w, "nope")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			}))
			defer srv.Close()
			resp, err := testClient(t, srv.URL, nil).Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err %v", err)
			}
			if err == nil && resp.Message.Content != "ok" {
				t.Fatalf("content %q", resp.Message.Content)
			}
			var apiErr *APIError
			if err != nil && !errors.As(err, &apiErr) {
				t.Fatalf("want APIError, got %T", err)
			}
			if n.Load() != tt.wantCalls {
				t.Fatalf("calls %d want %d", n.Load(), tt.wantCalls)
			}
		})
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, nil)
	c.backoff = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Chat(ctx, kernel.ChatRequest{Model: "smart"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ollama/v1/embeddings" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "nomic" || len(body.Input) != 2 {
			t.Errorf("body %+v", body)
		}
		io.WriteString(w, `{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL, func(cfg *Config) { cfg.Aliases["embed"] = Alias{Provider: "ollama", Model: "nomic"} })
	vecs, err := c.Embed(context.Background(), "embed", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][1] != 0.4 {
		t.Fatalf("vecs %v", vecs)
	}
}

func TestSanitizeCollisions(t *testing.T) {
	nm := newNameMap([]kernel.ToolSpec{{Name: "a.b"}, {Name: "a_b"}, {Name: "a/b"}})
	seen := map[string]bool{}
	for _, n := range []string{"a.b", "a_b", "a/b"} {
		w := nm.toWire[n]
		if seen[w] {
			t.Fatalf("collision %s", w)
		}
		seen[w] = true
		if nm.back(w) != n {
			t.Fatalf("roundtrip %s -> %s -> %s", n, w, nm.back(w))
		}
	}
}

func TestModule(t *testing.T) {
	t.Run("no providers", func(t *testing.T) {
		h := kerneltest.New(t)
		m := &Module{name: "llm", side: kernel.SideHub}
		if err := m.Init(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.Lookup(kernel.SvcLLM); ok {
			t.Fatal("service provided without providers")
		}
	})
	t.Run("configured", func(t *testing.T) {
		h := kerneltest.New(t)
		h.SideValue = kernel.SideNode
		h.ConfigYAML = `
providers:
  ollama: { base_url: "http://localhost:11434/v1", timeout: 30s }
aliases:
  fast-local: { provider: ollama, model: "qwen" }
`
		m := &Module{name: "node.llm", side: kernel.SideNode}
		if err := m.Init(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		l, err := kernel.Resolve[kernel.LLM](h, kernel.SvcLLM)
		if err != nil {
			t.Fatal(err)
		}
		if m.Client.cfg.Providers["ollama"].Timeout != 30*time.Second {
			t.Errorf("timeout not decoded")
		}
		if _, ok := l.(ProviderScoper); !ok {
			t.Error("client should implement ProviderScoper")
		}
	})
}
