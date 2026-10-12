// Package apns is a minimal Apple Push Notification service client: HTTP/2,
// token-based (ES256 JWT) authentication, retries for 429/5xx. Standard
// library only.
package apns

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Hosts.
const (
	ProductionURL = "https://api.push.apple.com"
	SandboxURL    = "https://api.sandbox.push.apple.com"
)

// Push types.
const (
	PushTypeAlert        = "alert"
	PushTypeLiveActivity = "liveactivity"
	PushTypeBackground   = "background"
)

// LiveActivityTopicSuffix is appended to the bundle id for liveactivity pushes.
const LiveActivityTopicSuffix = ".push-type.liveactivity"

// Request is one push to one device token.
type Request struct {
	// DeviceToken is the hex device / activity token.
	DeviceToken string
	// Environment selects the host: "sandbox" (or "development") or
	// "production". Empty = client default.
	Environment string
	Topic       string
	PushType    string // alert | liveactivity | background
	Priority    int    // 10 or 5 (1 allowed); 0 = omit header
	CollapseID  string // ≤ 64 bytes
	Expiration  time.Time
	ID          string // apns-id (UUID); empty = let Apple assign
	Payload     []byte
}

// Response is the APNs answer.
type Response struct {
	StatusCode int
	ApnsID     string
	Reason     string
	// Timestamp is set on 410: when the token stopped being valid.
	Timestamp time.Time
}

// OK reports a 200.
func (r *Response) OK() bool { return r != nil && r.StatusCode == http.StatusOK }

// Unregistered reports that the token is no longer valid (410 Unregistered).
// 400 BadDeviceToken is not treated as unregistered: it usually means a
// sandbox/production mismatch.
func (r *Response) Unregistered() bool {
	return r != nil && (r.StatusCode == http.StatusGone || r.Reason == "Unregistered")
}

// Config configures a Client.
type Config struct {
	Tokens *TokenSource
	// Environment is the default environment ("sandbox" | "production").
	Environment string
	// BaseURL overrides the host for every environment (tests, proxies).
	BaseURL string
	// HTTPClient defaults to an HTTP/2 client with a 30 s timeout.
	HTTPClient *http.Client
	// MaxRetries for 429/5xx/transport errors (default 3).
	MaxRetries int
	// Backoff returns the wait before retry n (1-based). Default 500ms·2^(n-1)
	// capped at 10 s.
	Backoff func(n int) time.Duration
}

// Client sends pushes.
type Client struct {
	cfg Config
}

// NewHTTPClient returns an HTTP/2-capable client (ALPN h2 over TLS).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			ForceAttemptHTTP2:   true,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     10 * time.Minute,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

// New creates a client.
func New(cfg Config) (*Client, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("apns: token source required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewHTTPClient()
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Backoff == nil {
		cfg.Backoff = func(n int) time.Duration {
			d := 500 * time.Millisecond << (n - 1)
			if d > 10*time.Second {
				d = 10 * time.Second
			}
			return d
		}
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) host(env string) string {
	if c.cfg.BaseURL != "" {
		return strings.TrimRight(c.cfg.BaseURL, "/")
	}
	if env == "" {
		env = c.cfg.Environment
	}
	switch strings.ToLower(env) {
	case "sandbox", "development", "dev":
		return SandboxURL
	default:
		return ProductionURL
	}
}

// Push sends one notification. 429/5xx and transport errors are retried
// with backoff; an ExpiredProviderToken 403 refreshes the JWT once. A
// non-nil Response is returned for every HTTP answer (check OK); err is
// set only when no answer was obtained.
func (c *Client) Push(ctx context.Context, r Request) (*Response, error) {
	if r.DeviceToken == "" {
		return nil, errors.New("apns: empty device token")
	}
	refreshed := false
	var lastErr error
	var last *Response
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(c.cfg.Backoff(attempt))
			select {
			case <-ctx.Done():
				t.Stop()
				if last != nil {
					return last, nil
				}
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		resp, err := c.do(ctx, r)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt >= c.cfg.MaxRetries {
				if last != nil {
					return last, nil
				}
				return nil, lastErr
			}
			continue
		}
		last = resp
		switch {
		case resp.StatusCode == http.StatusForbidden && !refreshed &&
			(resp.Reason == "ExpiredProviderToken" || resp.Reason == "InvalidProviderToken"):
			refreshed = true
			c.cfg.Tokens.Invalidate()
			attempt-- // the refresh retry does not count
			continue
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			if attempt >= c.cfg.MaxRetries {
				return resp, nil
			}
			continue
		default:
			return resp, nil
		}
	}
}

func (c *Client) do(ctx context.Context, r Request) (*Response, error) {
	tok, err := c.cfg.Tokens.Token()
	if err != nil {
		return nil, err
	}
	url := c.host(r.Environment) + "/3/device/" + r.DeviceToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(r.Payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("content-type", "application/json")
	if r.Topic != "" {
		req.Header.Set("apns-topic", r.Topic)
	}
	if r.PushType != "" {
		req.Header.Set("apns-push-type", r.PushType)
	}
	if r.Priority != 0 {
		req.Header.Set("apns-priority", strconv.Itoa(r.Priority))
	}
	if r.CollapseID != "" {
		req.Header.Set("apns-collapse-id", r.CollapseID)
	}
	if !r.Expiration.IsZero() {
		req.Header.Set("apns-expiration", strconv.FormatInt(r.Expiration.Unix(), 10))
	}
	if r.ID != "" {
		req.Header.Set("apns-id", r.ID)
	}
	hr, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer hr.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(hr.Body, 64<<10))
	out := &Response{StatusCode: hr.StatusCode, ApnsID: hr.Header.Get("apns-id")}
	if len(body) > 0 {
		var e struct {
			Reason    string `json:"reason"`
			Timestamp int64  `json:"timestamp"` // ms
		}
		if json.Unmarshal(body, &e) == nil {
			out.Reason = e.Reason
			if e.Timestamp > 0 {
				out.Timestamp = time.UnixMilli(e.Timestamp)
			}
		}
	}
	return out, nil
}

// String is a compact description for logs.
func (r *Response) String() string {
	if r == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d %s", r.StatusCode, r.Reason)
}
