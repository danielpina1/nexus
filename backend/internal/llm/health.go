package llm

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ProviderStatus is a diagnostic snapshot of one provider.
type ProviderStatus struct {
	Name                string        `json:"name"`
	API                 string        `json:"api"`
	BaseURL             string        `json:"base_url"`
	Healthy             bool          `json:"healthy"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	DegradedUntil       time.Time     `json:"degraded_until,omitempty"`
	Cooldown            time.Duration `json:"cooldown,omitempty"`
	LastError           string        `json:"last_error,omitempty"`
	LastErrorAt         time.Time     `json:"last_error_at,omitempty"`
	LastSuccessAt       time.Time     `json:"last_success_at,omitempty"`
	LastLatency         time.Duration `json:"last_latency,omitempty"`
	Requests            int64         `json:"requests"`
	Failures            int64         `json:"failures"`
	Fallbacks           int64         `json:"fallbacks"`
}

type providerHealth struct {
	consecutive   int
	degradedUntil time.Time
	cooldown      time.Duration // last applied cool-down
	lastErr       string
	lastErrAt     time.Time
	lastOK        time.Time
	lastLatency   time.Duration
	requests      int64
	failures      int64
	fallbacks     int64
}

type healthTracker struct {
	mu  sync.Mutex
	cfg HealthConfig
	now func() time.Time
	m   map[string]*providerHealth
}

func newHealthTracker(cfg HealthConfig, names []string) *healthTracker {
	h := &healthTracker{cfg: cfg, now: time.Now, m: map[string]*providerHealth{}}
	for _, n := range names {
		h.m[n] = &providerHealth{}
	}
	return h
}

func (h *healthTracker) get(name string) *providerHealth {
	ph, ok := h.m[name]
	if !ok {
		ph = &providerHealth{}
		h.m[name] = ph
	}
	return ph
}

// available reports whether the provider is not cooling down.
func (h *healthTracker) available(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.now().Before(h.get(name).degradedUntil)
}

func (h *healthTracker) success(name string, latency time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ph := h.get(name)
	ph.requests++
	ph.consecutive = 0
	ph.degradedUntil = time.Time{}
	ph.cooldown = 0
	ph.lastOK = h.now()
	ph.lastLatency = latency
}

// failure records a provider-level failure. It returns true when this
// failure (re)starts a cool-down.
func (h *healthTracker) failure(name string, err error) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ph := h.get(name)
	now := h.now()
	ph.requests++
	ph.failures++
	ph.consecutive++
	ph.lastErr = err.Error()
	ph.lastErrAt = now
	if ph.consecutive < h.cfg.threshold() {
		return false
	}
	// Threshold reached (or a half-open probe failed): exponential cool-down.
	if ph.cooldown == 0 {
		ph.cooldown = h.cfg.cooldown()
	} else {
		ph.cooldown *= 2
		if ph.cooldown > h.cfg.maxCooldown() {
			ph.cooldown = h.cfg.maxCooldown()
		}
	}
	ph.degradedUntil = now.Add(ph.cooldown)
	return true
}

// neutral records a request that neither proves nor disproves health
// (e.g. 400/404 for one model).
func (h *healthTracker) neutral(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.get(name).requests++
}

func (h *healthTracker) fellBack(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.get(name).fallbacks++
}

func (h *healthTracker) snapshot(name string) ProviderStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	ph := h.get(name)
	return ProviderStatus{
		Name:                name,
		Healthy:             !h.now().Before(ph.degradedUntil),
		ConsecutiveFailures: ph.consecutive,
		DegradedUntil:       ph.degradedUntil,
		Cooldown:            ph.cooldown,
		LastError:           ph.lastErr,
		LastErrorAt:         ph.lastErrAt,
		LastSuccessAt:       ph.lastOK,
		LastLatency:         ph.lastLatency,
		Requests:            ph.requests,
		Failures:            ph.failures,
		Fallbacks:           ph.fallbacks,
	}
}

// Status returns a health snapshot of every provider, sorted by name.
func (c *Client) Status() []ProviderStatus {
	names := c.cfg.providerNames()
	out := make([]ProviderStatus, 0, len(names))
	for _, n := range names {
		s := c.st.health.snapshot(n)
		s.API = c.cfg.Providers[n].api()
		s.BaseURL = c.baseURL(n)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isProviderFailure reports whether err says the provider (not the request)
// is broken: network errors, timeouts, 5xx, 429, 401/403.
func isProviderFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized, apiErr.StatusCode == http.StatusForbidden:
			return true
		case apiErr.StatusCode == http.StatusTooManyRequests, apiErr.StatusCode >= 500:
			return true
		case apiErr.StatusCode == 0: // error object inside a 2xx body / stream
			return true
		}
		return false
	}
	return true
}

// shouldFallback reports whether another target may succeed where this one
// failed. Malformed requests (400/422) would fail everywhere.
func shouldFallback(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusUnprocessableEntity
	}
	return true
}
