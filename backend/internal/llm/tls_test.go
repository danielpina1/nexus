package llm

import (
	"context"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/danielpina1/nexus/backend/kernel"
)

func TestTLSWithCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(newFakeGateway(t, "qwen2.5:7b"))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	mk := func(caFile string) *Client {
		c, err := New(Config{
			MaxRetries: &zero,
			Providers:  map[string]ProviderConfig{"gw": {BaseURL: srv.URL + "/v1", CAFile: caFile}},
			Aliases:    map[string]Alias{"smart": {Provider: "gw", Model: "qwen2.5:7b"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("trusted via ca_file", func(t *testing.T) {
		resp, err := mk(ca).Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
		if err != nil || resp.Message.Content != "OK" {
			t.Fatalf("resp %+v err %v", resp, err)
		}
	})
	t.Run("untrusted without ca_file", func(t *testing.T) {
		c := mk("")
		_, err := c.Chat(context.Background(), kernel.ChatRequest{Model: "smart"})
		if err == nil || !isTLSError(err) {
			t.Fatalf("err %v", err)
		}
		kind, _, hint := diagnose(c, "gw", "qwen2.5:7b", err)
		if kind != diagTLS || !strings.Contains(hint, "ca_file") {
			t.Fatalf("diagnose %v %q", kind, hint)
		}
	})
	t.Run("bad pem", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.pem")
		os.WriteFile(bad, []byte("not a cert"), 0o600)
		_, err := New(Config{Providers: map[string]ProviderConfig{"gw": {BaseURL: srv.URL, CAFile: bad}}})
		if err == nil || !strings.Contains(err.Error(), "no PEM") {
			t.Fatalf("err %v", err)
		}
	})
}
