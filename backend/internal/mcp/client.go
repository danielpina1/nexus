package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// Client is a minimal Streamable HTTP MCP client (JSON responses only). It
// is used by tests and diagnostics; production clients are the CLIs.
type Client struct {
	URL  string
	HTTP *http.Client

	id      atomic.Int64
	session atomic.Value // string
}

// NewClient creates a client for url.
func NewClient(url string) *Client { return &Client{URL: url, HTTP: http.DefaultClient} }

// Call sends a request and returns its result.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "id": c.id.Add(1), "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, err := c.post(ctx, msg)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mcp client: decode response: %w (%s)", err, truncate(string(body), 200))
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// Notify sends a notification.
func (c *Client) Notify(ctx context.Context, method string) error {
	_, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	return err
}

// Initialize performs the initialize handshake.
func (c *Client) Initialize(ctx context.Context) (json.RawMessage, error) {
	res, err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "nexus-mcp-client", "version": "1"},
	})
	if err != nil {
		return nil, err
	}
	return res, c.Notify(ctx, "notifications/initialized")
}

// CallTool calls a tool.
func (c *Client) CallTool(ctx context.Context, name string, args any) (*CallResult, error) {
	res, err := c.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var out CallResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, msg any) ([]byte, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	if s, _ := c.session.Load().(string); s != "" {
		req.Header.Set("Mcp-Session-Id", s)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session.Store(s)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return body, nil
	}
	return nil, fmt.Errorf("mcp client: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
}
