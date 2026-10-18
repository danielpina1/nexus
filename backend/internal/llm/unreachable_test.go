package llm

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/danielpina1/nexus/backend/kernel"
)

// A provider with nothing listening fails at once instead of retrying with backoff.
func TestRefusedConnectionFailsFast(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // the port is now closed: connections are refused

	cfg := Config{Providers: map[string]ProviderConfig{"ollama": {BaseURL: "http://" + addr + "/v1"}}}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.Chat(context.Background(), kernel.ChatRequest{Model: "ollama/llama3", Messages: []kernel.ChatMessage{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("refused connection took %v (retried?)", d)
	}
}
