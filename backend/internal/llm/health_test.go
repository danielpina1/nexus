package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

func chainClient(t *testing.T, primary, fallback string) *Client {
	t.Helper()
	zero := 0
	c, err := New(Config{
		MaxRetries: &zero,
		Health:     HealthConfig{FailureThreshold: 2, Cooldown: time.Minute, MaxCooldown: 3 * time.Minute},
		Providers: map[string]ProviderConfig{
			"cloud":  {BaseURL: primary},
			"ollama": {BaseURL: fallback},
		},
		Aliases: map[string]Alias{
			"smart": {Provider: "cloud", Model: "big", Fallback: []Target{{Provider: "ollama", Model: "qwen2.5:7b"}}},
			"embed": {Provider: "cloud", Model: "e1", Fallback: []Target{{Provider: "ollama", Model: "other-embed"}}},
		},
	}, WithBackoff(func(int) time.Duration { return 0 }))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFallbackChainAndCooldown(t *testing.T) {
	var primaryCalls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"upstream down"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"from primary"}}]}`)
	}))
	defer primary.Close()
	local := newFakeGateway(t, "qwen2.5:7b")
	fb := httptest.NewServer(local)
	defer fb.Close()

	c := chainClient(t, primary.URL, fb.URL)
	now := time.Unix(1_000_000, 0)
	c.st.health.now = func() time.Time { return now }
	req := kernel.ChatRequest{Model: "smart", Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}}}

	// Two failures fall back and trip the threshold.
	for i := 0; i < 2; i++ {
		res, err := c.ChatEx(context.Background(), req, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Target.Provider != "ollama" || res.Fallbacks != 1 || res.Response.Message.Content != "OK" {
			t.Fatalf("res %+v", res)
		}
	}
	st := statusOf(c, "cloud")
	if st.Healthy || st.ConsecutiveFailures != 2 || st.Cooldown != time.Minute || st.Fallbacks != 2 || !strings.Contains(st.LastError, "upstream down") {
		t.Fatalf("status %+v", st)
	}
	// While cooling down the primary is skipped entirely.
	before := primaryCalls.Load()
	if _, err := c.ChatEx(context.Background(), req, false, nil); err != nil {
		t.Fatal(err)
	}
	if primaryCalls.Load() != before {
		t.Fatal("degraded provider was called during cool-down")
	}
	// After the cool-down a failed half-open probe doubles it.
	now = now.Add(61 * time.Second)
	if _, err := c.ChatEx(context.Background(), req, false, nil); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(c, "cloud"); st.Healthy || st.Cooldown != 2*time.Minute {
		t.Fatalf("cooldown not doubled: %+v", st)
	}
	now = now.Add(121 * time.Second)
	_, _ = c.ChatEx(context.Background(), req, false, nil)
	if st := statusOf(c, "cloud"); st.Cooldown != 3*time.Minute {
		t.Fatalf("cooldown not capped: %+v", st)
	}
	// Recovery resets everything.
	now = now.Add(181 * time.Second)
	fail.Store(false)
	res, err := c.ChatEx(context.Background(), req, false, nil)
	if err != nil || res.Target.Provider != "cloud" || res.Fallbacks != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if st := statusOf(c, "cloud"); !st.Healthy || st.ConsecutiveFailures != 0 || st.Cooldown != 0 || st.LastSuccessAt.IsZero() {
		t.Fatalf("status after recovery %+v", st)
	}
}

func statusOf(c *Client, name string) ProviderStatus {
	for _, s := range c.Status() {
		if s.Name == name {
			return s
		}
	}
	return ProviderStatus{}
}

func TestFallbackRules(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantFB   bool
		wantDegr bool // counted as provider failure
	}{
		{"400 no fallback", 400, false, false},
		{"404 model missing falls back", 404, true, false},
		{"401 falls back and counts", 401, true, true},
		{"500 falls back and counts", 500, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, `{"error":{"message":"nope"}}`)
			}))
			defer primary.Close()
			fb := httptest.NewServer(newFakeGateway(t, "qwen2.5:7b"))
			defer fb.Close()
			c := chainClient(t, primary.URL, fb.URL)
			res, err := c.ChatEx(context.Background(), kernel.ChatRequest{Model: "smart"}, false, nil)
			if tt.wantFB != (err == nil) {
				t.Fatalf("res %+v err %v", res, err)
			}
			if got := statusOf(c, "cloud").ConsecutiveFailures; (got > 0) != tt.wantDegr {
				t.Fatalf("consecutive failures %d", got)
			}
		})
	}
}

func TestAllTargetsFail(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer down.Close()
	c := chainClient(t, down.URL, down.URL)
	_, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
	var ce *ChainError
	var apiErr *APIError
	if !errors.As(err, &ce) || len(ce.Errs) != 2 || !errors.As(err, &apiErr) || apiErr.StatusCode != 502 {
		t.Fatalf("err %v", err)
	}
}

func TestStreamDoesNotFallBackAfterOutput(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`, `{"error":{"message":"died midway"}}`)
	}))
	defer primary.Close()
	g := newFakeGateway(t, "qwen2.5:7b")
	fb := httptest.NewServer(g)
	defer fb.Close()
	c := chainClient(t, primary.URL, fb.URL)
	var out strings.Builder
	_, err := c.ChatStream(context.Background(), kernel.ChatRequest{Model: "smart"}, func(s string) { out.WriteString(s) })
	if err == nil || !strings.Contains(err.Error(), "died midway") {
		t.Fatalf("err %v", err)
	}
	if out.String() != "partial" || g.count("/v1/chat/completions") != 0 {
		t.Fatalf("fell back after output: %q, %d", out.String(), g.count("/v1/chat/completions"))
	}
}

func TestEmbedFallbackOnlySameModel(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	g := newFakeGateway(t, "other-embed")
	fb := httptest.NewServer(g)
	defer fb.Close()
	c := chainClient(t, down.URL, fb.URL)
	if _, err := c.Embed(context.Background(), "embed", []string{"x"}); err == nil {
		t.Fatal("embedding fell back to a different model")
	}
	if g.count("/v1/embeddings") != 0 {
		t.Fatal("fallback provider called")
	}
}

func TestContextCancelNotCountedAsFailure(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c := chainClient(t, srv.URL, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _ = c.Chat(ctx, kernel.ChatRequest{Model: "smart"})
	if st := statusOf(c, "cloud"); st.ConsecutiveFailures != 0 {
		t.Fatalf("status %+v", st)
	}
}
