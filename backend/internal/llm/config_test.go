package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielpina1/nexus/backend/kernel"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct{ in, want, err string }{
		{"http://192.168.1.20:4000", "http://192.168.1.20:4000/v1", ""},
		{"http://192.168.1.20:4000/", "http://192.168.1.20:4000/v1", ""},
		{"http://gw.home.arpa:8080/v1", "http://gw.home.arpa:8080/v1", ""},
		{"http://gw.home.arpa:8080/v1/", "http://gw.home.arpa:8080/v1", ""},
		{"http://gw/api/openai/v1", "http://gw/api/openai/v1", ""},
		{"http://gw:4000/v1/chat/completions", "http://gw:4000/v1", ""},
		{"https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1", ""},
		{"http://localhost:11434", "http://localhost:11434/v1", ""},
		{"localhost:11434/v1", "http://localhost:11434/v1", ""},
		{"", "", "empty"},
		{"ftp://x", "", "scheme"},
		{"http://", "", "no host"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.in)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err %v want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v want %q", got, err, tt.want)
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"insecure refused", Config{Providers: map[string]ProviderConfig{"gw": {BaseURL: "https://gw", InsecureSkipVerify: true}}}, "ca_file"},
		{"bad api", Config{Providers: map[string]ProviderConfig{"gw": {BaseURL: "http://gw", API: "grpc"}}}, "unknown api"},
		{"bad auth", Config{Providers: map[string]ProviderConfig{"gw": {BaseURL: "http://gw", Auth: "magic"}}}, "unknown auth"},
		{"fallback unknown provider", Config{
			Providers: map[string]ProviderConfig{"gw": {BaseURL: "http://gw"}},
			Aliases:   map[string]Alias{"smart": {Provider: "gw", Model: "m", Fallback: []Target{{Provider: "nope", Model: "x"}}}},
		}, "unknown provider"},
		{"fallback without model", Config{
			Providers: map[string]ProviderConfig{"gw": {BaseURL: "http://gw"}},
			Aliases:   map[string]Alias{"smart": {Provider: "gw", Model: "m", Fallback: []Target{{Provider: "gw"}}}},
		}, "without a model"},
		{"missing ca file", Config{Providers: map[string]ProviderConfig{"gw": {BaseURL: "https://gw", CAFile: "/nonexistent/ca.pem"}}}, "ca_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v want %q", err, tt.want)
			}
		})
	}
}

func TestAuthModes(t *testing.T) {
	tests := []struct {
		name     string
		pc       ProviderConfig
		header   string
		want     string
		absentOK []string
	}{
		{"bearer default", ProviderConfig{APIKeyEnv: "K"}, "Authorization", "Bearer sekrit", nil},
		{"custom header", ProviderConfig{APIKeyEnv: "K", AuthHeader: "X-Gateway-Key"}, "X-Gateway-Key", "sekrit", []string{"Authorization"}},
		{"none", ProviderConfig{APIKeyEnv: "K", Auth: "none"}, "Authorization", "", nil},
		{"anthropic default", ProviderConfig{APIKeyEnv: "K", API: "anthropic"}, "X-Api-Key", "sekrit", []string{"Authorization"}},
		{"anthropic bearer", ProviderConfig{APIKeyEnv: "K", API: "anthropic", Auth: "bearer"}, "Authorization", "Bearer sekrit", []string{"X-Api-Key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hdr http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hdr = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"data":[]}`))
			}))
			defer srv.Close()
			pc := tt.pc
			pc.BaseURL = srv.URL
			c, err := New(Config{Providers: map[string]ProviderConfig{"p": pc}},
				WithGetenv(func(string) string { return "sekrit" }))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ListModels(context.Background(), "p"); err != nil {
				t.Fatal(err)
			}
			if got := hdr.Get(tt.header); got != tt.want {
				t.Fatalf("%s = %q want %q", tt.header, got, tt.want)
			}
			for _, h := range tt.absentOK {
				if hdr.Get(h) != "" {
					t.Fatalf("unexpected %s header", h)
				}
			}
			if pc.api() == APIAnthropic && hdr.Get("anthropic-version") != DefaultAnthropicVersion {
				t.Fatalf("anthropic-version %q", hdr.Get("anthropic-version"))
			}
		})
	}
}

func TestPlaceholderAliasWithoutAuto(t *testing.T) {
	c, err := New(Config{
		Providers: map[string]ProviderConfig{"ollama": {BaseURL: "http://localhost:11434"}},
		Aliases:   map[string]Alias{"smart": {Provider: "ollama", Model: "CHANGE_ME"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
	if err == nil || !strings.Contains(err.Error(), "aliases_auto") {
		t.Fatalf("err %v", err)
	}
}
