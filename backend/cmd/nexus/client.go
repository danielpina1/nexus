package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
)

// client bundles the generated Connect clients.
type client struct {
	baseURL   string
	http      *http.Client
	agents    nexusv1connect.AgentServiceClient
	tasks     nexusv1connect.TaskServiceClient
	approvals nexusv1connect.ApprovalServiceClient
	nodes     nexusv1connect.NodeServiceClient
	events    nexusv1connect.EventServiceClient
}

// bearer adds the Authorization header to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}

func newClient(baseURL, token string) *client {
	return newClientRT(baseURL, token, http.DefaultTransport)
}

func newClientRT(baseURL, token string, rt http.RoundTripper) *client {
	baseURL = strings.TrimRight(baseURL, "/")
	hc := &http.Client{Transport: bearer{token: token, next: rt}}
	return &client{
		baseURL:   baseURL,
		http:      hc,
		agents:    nexusv1connect.NewAgentServiceClient(hc, baseURL),
		tasks:     nexusv1connect.NewTaskServiceClient(hc, baseURL),
		approvals: nexusv1connect.NewApprovalServiceClient(hc, baseURL),
		nodes:     nexusv1connect.NewNodeServiceClient(hc, baseURL),
		events:    nexusv1connect.NewEventServiceClient(hc, baseURL),
	}
}

// health calls /healthz and returns the reported version.
func (c *client) health(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("healthz: %s", resp.Status)
	}
	var h struct{ Version string }
	_ = json.NewDecoder(resp.Body).Decode(&h)
	return h.Version, nil
}

// notAvailable reports whether err means the service is not enabled on the hub.
func notAvailable(err error) bool {
	return connect.CodeOf(err) == connect.CodeUnimplemented
}

// describe turns Connect errors into short, actionable messages.
func describe(err error) string {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated:
		return "unauthenticated: set NEXUS_TOKEN (security.dev_token in nexus.yaml)"
	case connect.CodeUnimplemented:
		return "the hub does not provide this service (module not enabled?)"
	case connect.CodeUnavailable:
		var ce *connect.Error
		if errors.As(err, &ce) {
			return "hub unavailable: " + ce.Message()
		}
	}
	return err.Error()
}
