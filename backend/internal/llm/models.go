package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ModelInfo is one model reported by a provider.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

const (
	discoveryTimeout    = 15 * time.Second
	discoveryRetryAfter = 30 * time.Second
	discoveryRefresh    = 10 * time.Minute
)

// ListModels lists the models a provider serves: GET {base}/models for both
// API flavours (OpenAI {data:[{id}]}, Anthropic {data:[{id,display_name}],
// has_more,last_id}; Ollama-style {models:[{name}]} is accepted too).
func (c *Client) ListModels(ctx context.Context, provider string) ([]ModelInfo, error) {
	pc, ok := c.cfg.Providers[provider]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
	if isCLIAPI(pc.api()) {
		var out []ModelInfo
		for _, id := range cliModels[pc.api()] {
			out = append(out, ModelInfo{ID: id})
		}
		return out, nil
	}
	var out []ModelInfo
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 20; page++ {
		path := "/models"
		if pc.api() == APIAnthropic {
			q := url.Values{"limit": {"1000"}}
			if after != "" {
				q.Set("after_id", after)
			}
			path += "?" + q.Encode()
		}
		resp, err := c.do(ctx, provider, path, nil)
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		err = json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("llm: %s: decode model list (is base_url the API root?): %w", provider, err)
		}
		models, hasMore, last, err := parseModelList(raw)
		if err != nil {
			return nil, fmt.Errorf("llm: %s: %w", provider, err)
		}
		for _, m := range models {
			if m.ID != "" && !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
		if !hasMore || last == "" || last == after {
			break
		}
		after = last
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	c.st.mu.Lock()
	c.st.discovered[provider] = out
	c.st.mu.Unlock()
	return out, nil
}

func parseModelList(raw json.RawMessage) (models []ModelInfo, hasMore bool, last string, err error) {
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Model       string `json:"model"`
		DisplayName string `json:"display_name"`
		OwnedBy     string `json:"owned_by"`
	}
	conv := func(items []item) []ModelInfo {
		out := make([]ModelInfo, 0, len(items))
		for _, it := range items {
			id := it.ID
			if id == "" {
				id = it.Model
			}
			if id == "" {
				id = it.Name
			}
			out = append(out, ModelInfo{ID: id, DisplayName: it.DisplayName, OwnedBy: it.OwnedBy})
		}
		return out
	}
	var obj struct {
		Data    []item          `json:"data"`
		Models  []item          `json:"models"`
		HasMore bool            `json:"has_more"`
		LastID  string          `json:"last_id"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if len(obj.Error) > 0 && string(obj.Error) != "null" {
			return nil, false, "", &APIError{Message: errorMessage(raw)}
		}
		if obj.Data != nil || obj.Models != nil {
			return conv(append(obj.Data, obj.Models...)), obj.HasMore, obj.LastID, nil
		}
	}
	var arr []item
	if json.Unmarshal(raw, &arr) == nil {
		return conv(arr), false, "", nil
	}
	return nil, false, "", errors.New("unrecognised model list (expected {\"data\":[{\"id\":...}]})")
}

// DiscoveredModels returns the models found by the last listing of a provider.
func (c *Client) DiscoveredModels(provider string) []ModelInfo {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	return append([]ModelInfo(nil), c.st.discovered[provider]...)
}

// providerOrder is the default provider first, then the rest by name.
func (c *Client) providerOrder() []string {
	out := []string{}
	if c.defaultProvider != "" {
		out = append(out, c.defaultProvider)
	}
	for _, n := range c.cfg.providerNames() {
		if n != c.defaultProvider {
			out = append(out, n)
		}
	}
	return out
}

// autoCandidates lists the aliases aliases_auto may fill.
func (c *Client) autoCandidates() []string {
	set := map[string]bool{}
	for a := range DefaultAliasPreferences {
		set[a] = true
	}
	for a := range c.cfg.AliasPreferences {
		set[a] = true
	}
	for a, al := range c.cfg.Aliases {
		if isPlaceholder(al.Model) {
			set[a] = true
		} else {
			delete(set, a) // explicit alias wins
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Discover lists every provider's models and (with aliases_auto) selects
// models for unset aliases. It returns the first listing error, if any;
// aliases are still filled from the providers that answered.
func (c *Client) Discover(ctx context.Context) (map[string]Target, error) {
	c.st.discMu.Lock()
	defer c.st.discMu.Unlock()
	return c.discoverLocked(ctx)
}

func (c *Client) discoverLocked(ctx context.Context) (map[string]Target, error) {
	var errs []error
	lists := map[string][]ModelInfo{}
	for _, p := range c.providerOrder() {
		ms, err := c.ListModels(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		lists[p] = ms
	}
	auto := map[string]Target{}
	if c.cfg.AliasesAuto {
		for _, alias := range c.autoCandidates() {
			if t, ok := c.pickAuto(alias, lists); ok {
				auto[alias] = t
			}
		}
	}
	err := errors.Join(errs...)
	c.st.mu.Lock()
	for k, v := range auto {
		c.st.auto[k] = v
	}
	c.st.discErr = err
	c.st.discAt = time.Now()
	c.st.mu.Unlock()
	for a, t := range auto {
		c.log.Debug("llm: auto alias", "alias", a, "target", t.String())
	}
	return auto, err
}

func (c *Client) pickAuto(alias string, lists map[string][]ModelInfo) (Target, bool) {
	prefs := c.cfg.preferences(alias)
	if len(prefs) == 0 {
		return Target{}, false
	}
	providers := c.providerOrder()
	if a, ok := c.cfg.Aliases[alias]; ok && a.Provider != "" {
		providers = []string{a.Provider}
	}
	wantEmbed := strings.Contains(strings.ToLower(alias), "embed")
	for _, pref := range prefs {
		pref = strings.ToLower(pref)
		for _, p := range providers {
			if m := bestMatch(lists[p], pref, wantEmbed); m != "" {
				return Target{Provider: p, Model: m}, true
			}
		}
	}
	return Target{}, false
}

// bestMatch returns the greatest model id containing pref; embedding models
// are only eligible for embedding aliases and vice versa.
func bestMatch(models []ModelInfo, pref string, wantEmbed bool) string {
	best := ""
	for _, m := range models {
		id := strings.ToLower(m.ID)
		if strings.Contains(id, "embed") != wantEmbed {
			continue
		}
		if strings.Contains(id, pref) && m.ID > best {
			best = m.ID
		}
	}
	return best
}

func (c *Client) autoTarget(ctx context.Context, alias string, discover bool) (Target, bool) {
	lookup := func() (Target, bool, time.Time, error) {
		c.st.mu.Lock()
		defer c.st.mu.Unlock()
		t, ok := c.st.auto[alias]
		return t, ok, c.st.discAt, c.st.discErr
	}
	t, ok, _, _ := lookup()
	if ok || !discover {
		return t, ok
	}
	// One discovery at a time; re-check once we hold the lock.
	c.st.discMu.Lock()
	defer c.st.discMu.Unlock()
	t, ok, at, derr := lookup()
	if ok {
		return t, ok
	}
	if !at.IsZero() {
		since := time.Since(at)
		if (derr == nil && since < discoveryRefresh) || (derr != nil && since < discoveryRetryAfter) {
			return Target{}, false
		}
	}
	dctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	if _, err := c.discoverLocked(dctx); err != nil {
		c.log.Warn("llm: model discovery failed", "err", err)
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	t, ok = c.st.auto[alias]
	return t, ok
}

func (c *Client) discoveryErr() error {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	return c.st.discErr
}

// AliasInfo describes how an alias currently resolves.
type AliasInfo struct {
	Name    string
	Targets []Target // primary first, then fallbacks
	Auto    bool     // primary chosen by aliases_auto
	Err     error
}

// AliasTable resolves every configured alias (and, with aliases_auto, the
// auto-fillable ones), running discovery when needed.
func (c *Client) AliasTable(ctx context.Context) []AliasInfo {
	names := map[string]bool{}
	for a := range c.cfg.Aliases {
		names[a] = true
	}
	if c.cfg.AliasesAuto {
		for _, a := range c.autoCandidates() {
			names[a] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for a := range names {
		sorted = append(sorted, a)
	}
	sort.Strings(sorted)
	out := make([]AliasInfo, 0, len(sorted))
	for _, a := range sorted {
		ts, err := c.chain(ctx, a, true)
		info := AliasInfo{Name: a, Targets: ts, Err: err}
		if err == nil {
			al, configured := c.cfg.Aliases[a]
			c.st.mu.Lock()
			auto, isAuto := c.st.auto[a]
			c.st.mu.Unlock()
			info.Auto = isAuto && (!configured || isPlaceholder(al.Model)) && ts[0] == auto
			if !configured && !isAuto {
				// Not configured and nothing discovered: it would be sent as a raw model name.
				info.Err = fmt.Errorf("no model discovered for %q (preferences %v)", a, c.cfg.preferences(a))
				info.Targets = nil
			}
		}
		out = append(out, info)
	}
	return out
}
