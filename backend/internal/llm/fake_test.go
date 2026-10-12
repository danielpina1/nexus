package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeGateway is an in-memory model server speaking the OpenAI and
// Anthropic wire formats under /v1.
type fakeGateway struct {
	t       *testing.T
	models  []string
	key     string // required key ("" = no auth)
	noTools bool   // answer tool requests with text
	mu      sync.Mutex
	calls   map[string]int
	lastReq map[string]any
	lastHdr http.Header
}

func newFakeGateway(t *testing.T, models ...string) *fakeGateway {
	return &fakeGateway{t: t, models: models, calls: map[string]int{}}
}

func (g *fakeGateway) count(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[path]
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.calls[r.URL.Path]++
	g.lastHdr = r.Header.Clone()
	g.mu.Unlock()
	if g.key != "" {
		if r.Header.Get("Authorization") != "Bearer "+g.key && r.Header.Get("X-Api-Key") != g.key {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.lastReq = body
		g.mu.Unlock()
	}
	switch r.URL.Path {
	case "/v1/models":
		data := make([]map[string]string, len(g.models))
		for i, m := range g.models {
			data[i] = map[string]string{"id": m, "object": "model"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case "/v1/embeddings":
		in, _ := body["input"].([]any)
		data := make([]map[string]any, len(in))
		for i := range in {
			data[i] = map[string]any{"index": i, "embedding": []float32{0.1, 0.2, 0.3}}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "/v1/chat/completions":
		g.openaiChat(w, body)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `404 page not found`)
	}
}

func (g *fakeGateway) openaiChat(w http.ResponseWriter, body map[string]any) {
	model, _ := body["model"].(string)
	known := false
	for _, m := range g.models {
		known = known || m == model
	}
	if !known {
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error":{"message":"model %q not found"}}`, model)
		return
	}
	_, hasTools := body["tools"]
	_, jsonMode := body["response_format"]
	stream, _ := body["stream"].(bool)
	switch {
	case hasTools && !g.noTools:
		if stream {
			sse(w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"nexus_probe","arguments":""}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"word\":\"ping\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"nexus_probe","arguments":"{\"word\":\"ping\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
	case jsonMode:
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true,\"n\":3}"},"finish_reason":"stop"}]}`)
	case stream:
		sse(w,
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"1 2"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":" 3 4"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":" 5"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":5}}`, `[DONE]`)
	default:
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1}}`)
	}
}

func contains(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}
