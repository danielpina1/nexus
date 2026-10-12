package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Multi-store shopping (docs/SHOPPING.md).
//
// Stores are vault notes (shops/<id>.md) served by the hub tools shops.list
// and shops.get (internal/shops). The node cannot read the vault, so the
// browser runtime learns a store when the agent fetches it: the run's
// shops.get tool is wrapped (wrapShopsGet) and every store it returns is
// registered with the run — its domains join the run's allow-list, its
// checkout_labels join the checkout (SPEND) patterns, and navigations to
// its domains use the store's own persistent profile (profile key = store
// id), so each store's login survives independently.
//
// Any other domain needs browser.allow_domain {domain, reason}, a
// WRITE_EXTERNAL tool: the agent loop gates it (APPROVE by default), so the
// user decides.

// Hub tool names (internal/shops).
const (
	HubToolShopsGet  = "shops.get"
	HubToolShopsList = "shops.list"
)

// ToolAllowDomain asks the user to extend the run's allow-list.
const ToolAllowDomain = "browser.allow_domain"

// ShopInfo is what the browser runtime needs to know about a store.
type ShopInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name,omitempty"`
	Domains        []string `json:"domains"`
	CheckoutLabels []string `json:"checkout_labels,omitempty"`
	ResearchOnly   bool     `json:"research_only,omitempty"`
}

// shopRegistry holds the stores known to one run.
type shopRegistry struct {
	policy DomainPolicy
	trust  bool // add shop domains to the allow-list
	log    *slog.Logger

	mu       sync.RWMutex
	byID     map[string]ShopInfo
	patterns []*regexp.Regexp
	patSrc   map[string]bool
}

func newShopRegistry(policy DomainPolicy, trust bool, log *slog.Logger) *shopRegistry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &shopRegistry{policy: policy, trust: trust, log: log, byID: map[string]ShopInfo{}, patSrc: map[string]bool{}}
}

// register records a store; it returns the domains newly allowed.
func (r *shopRegistry) register(s ShopInfo) (added []string, err error) {
	s.ID = strings.ToLower(strings.TrimSpace(s.ID))
	if s.ID == "" {
		return nil, fmt.Errorf("shop without id")
	}
	var doms []string
	for _, d := range s.Domains {
		if nd := normDomain(d); nd != "" {
			doms = append(doms, nd)
		}
	}
	s.Domains = doms
	r.mu.Lock()
	r.byID[s.ID] = s
	for _, l := range s.CheckoutLabels {
		src := LabelPattern(l)
		if src == "" || r.patSrc[src] {
			continue
		}
		re, cerr := regexp.Compile("(?i)" + src)
		if cerr != nil {
			r.log.Warn("browser: bad checkout label in shop note", "shop", s.ID, "label", l, "err", cerr)
			continue
		}
		r.patSrc[src] = true
		r.patterns = append(r.patterns, re)
	}
	r.mu.Unlock()
	if !r.trust {
		return nil, nil
	}
	return r.policy.Add(s.Domains...)
}

// LabelPattern turns a shop note's checkout label into a regexp source
// matched against folded (lower-case, accent-free) element labels. A label
// written as /regexp/ is used as is (folded); anything else is a literal
// phrase matched on word boundaries.
func LabelPattern(label string) string {
	l := strings.TrimSpace(label)
	if len(l) > 2 && strings.HasPrefix(l, "/") && strings.HasSuffix(l, "/") {
		return Fold(l[1 : len(l)-1])
	}
	f := Fold(l)
	if f == "" {
		return ""
	}
	return `(^|\W)` + regexp.QuoteMeta(f) + `($|\W)`
}

// match reports whether a label matches a store's checkout pattern.
func (r *shopRegistry) match(labels ...string) (label, pattern string, ok bool) {
	if r == nil {
		return "", "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range labels {
		f := Fold(l)
		if f == "" {
			continue
		}
		for _, re := range r.patterns {
			if re.MatchString(f) {
				return l, re.String(), true
			}
		}
	}
	return "", "", false
}

// forHost returns the store whose domains cover host (longest match).
func (r *shopRegistry) forHost(host string) (ShopInfo, bool) {
	if r == nil || host == "" {
		return ShopInfo{}, false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best ShopInfo
	bestLen := 0
	for _, s := range r.byID {
		for _, d := range s.Domains {
			if (h == d || strings.HasSuffix(h, "."+d)) && len(d) > bestLen {
				best, bestLen = s, len(d)
			}
		}
	}
	return best, bestLen > 0
}

func (r *shopRegistry) ids() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// parseShopResult extracts the store from a shops.get result: the store
// object itself or {"shop": {...}}.
func parseShopResult(out json.RawMessage) (ShopInfo, bool) {
	var wrap struct {
		Shop *ShopInfo `json:"shop"`
	}
	if json.Unmarshal(out, &wrap) == nil && wrap.Shop != nil && wrap.Shop.ID != "" {
		return *wrap.Shop, true
	}
	var s ShopInfo
	if json.Unmarshal(out, &s) == nil && s.ID != "" {
		return s, true
	}
	return ShopInfo{}, false
}

// wrapShopsGet registers the store returned by the hub's shops.get with the
// run and tells the model which domains are now reachable.
func wrapShopsGet(t kernel.Tool, reg *shopRegistry) kernel.Tool {
	return kernel.ToolFunc{S: t.Spec(), F: func(ctx context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
		res, err := t.Invoke(ctx, c)
		if err != nil {
			return res, err
		}
		s, ok := parseShopResult(res.Output)
		if !ok {
			return res, nil
		}
		added, aerr := reg.register(s)
		note := map[string]any{"shop": s.ID, "allowed_domains": reg.policy.Domains()}
		if len(added) > 0 {
			note["newly_allowed"] = added
		}
		if aerr != nil {
			note["skipped"] = aerr.Error()
		}
		if !reg.trust {
			note["hint"] = "shop domains are not added automatically on this node; use browser.allow_domain"
		}
		var m map[string]any
		if json.Unmarshal(res.Output, &m) == nil && m != nil {
			m["browser"] = note
			if b, err := json.Marshal(m); err == nil {
				res.Output = b
			}
		}
		return res, nil
	}}
}

// allowDomainTool is browser.allow_domain. Its static class is
// WRITE_EXTERNAL, so the agent loop asks the user before it runs.
func allowDomainTool(policy DomainPolicy, log *slog.Logger) kernel.Tool {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return kernel.ToolFunc{
		S: kernel.ToolSpec{
			Name:        ToolAllowDomain,
			Description: "Ask the user to let this run open another site (domain and its subdomains). Use only when the task needs a site that is not allowed yet and is not a known shop (for shops call shops.get). The user must approve.",
			InputSchema: schema(`{"type":"object","properties":{"domain":{"type":"string","description":"e.g. example.pt"},"reason":{"type":"string","description":"why the task needs it"}},"required":["domain","reason"]}`),
			ActionClass: writeExternal,
		},
		F: func(_ context.Context, c kernel.ToolCall) (kernel.ToolResult, error) {
			var in struct {
				Domain string `json:"domain"`
				Reason string `json:"reason"`
			}
			if err := kernel.ToolInput(c, &in); err != nil {
				return kernel.ToolResult{}, err
			}
			if strings.TrimSpace(in.Reason) == "" {
				return kernel.ToolResult{}, fmt.Errorf("reason is required")
			}
			if err := ValidAllowDomain(in.Domain); err != nil {
				return kernel.ToolResult{}, err
			}
			if !policy.Restricted() {
				return kernel.ToolOutput(map[string]any{"ok": true, "note": "this run has no domain restriction"})
			}
			added, err := policy.Add(in.Domain)
			if err != nil {
				return kernel.ToolResult{}, err
			}
			log.Info("browser: domain allowed by user", "domain", normDomain(in.Domain), "reason", in.Reason)
			return kernel.ToolOutput(map[string]any{"ok": true, "added": added, "allowed_domains": policy.Domains()})
		},
	}
}

// ---------------------------------------------------------------- run context

// browserCtx returns the "browser" map of the run context (also accepting
// flat "browser.<key>" entries).
func browserCtx(c *structpb.Struct) map[string]any {
	out := map[string]any{}
	if c == nil {
		return out
	}
	m := c.AsMap()
	if b, ok := m["browser"].(map[string]any); ok {
		for k, v := range b {
			out[k] = v
		}
	}
	for k, v := range m {
		if rest, ok := strings.CutPrefix(k, "browser."); ok {
			if _, dup := out[rest]; !dup {
				out[rest] = v
			}
		}
	}
	return out
}

// profileFromContext reads browser.profile.
func profileFromContext(c *structpb.Struct) string {
	s, _ := browserCtx(c)["profile"].(string)
	return strings.TrimSpace(s)
}

// restrictFromContext reads browser.restrict_domains (set=false when absent).
func restrictFromContext(c *structpb.Struct) (v, set bool) {
	b, ok := browserCtx(c)["restrict_domains"].(bool)
	return b, ok
}

// shopsFromContext reads the stores a run is for: context "shops" (a list
// of ids or of {id, domains, checkout_labels} objects) and "shop" (one id or
// object). Ids without domains are returned in ids only.
func shopsFromContext(c *structpb.Struct) (ids []string, infos []ShopInfo) {
	if c == nil {
		return nil, nil
	}
	m := c.AsMap()
	var items []any
	switch v := m["shops"].(type) {
	case []any:
		items = append(items, v...)
	case string:
		for _, s := range strings.Split(v, ",") {
			items = append(items, s)
		}
	case map[string]any:
		items = append(items, v)
	}
	if v, ok := m["shop"]; ok {
		items = append(items, v)
	}
	seen := map[string]bool{}
	for _, it := range items {
		switch v := it.(type) {
		case string:
			id := strings.ToLower(strings.TrimSpace(v))
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		case map[string]any:
			b, _ := json.Marshal(v)
			var s ShopInfo
			if json.Unmarshal(b, &s) == nil && strings.TrimSpace(s.ID) != "" {
				id := strings.ToLower(strings.TrimSpace(s.ID))
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
				if len(s.Domains) > 0 {
					infos = append(infos, s)
				}
			}
		}
	}
	return ids, infos
}

// ShopGuidance is appended to the instructions of runs that can use shops.
func ShopGuidance(ids []string, hasShopsGet bool, p DomainPolicy) string {
	var b strings.Builder
	b.WriteString("\n## Stores\n")
	if hasShopsGet {
		b.WriteString("- Stores are vault notes. Before opening a store, call shops.get {id} (shops.list {category} finds stores): it returns the store's playbook and unlocks its domains for this run. Each store uses its own logged-in browser profile.\n")
	}
	if len(ids) > 0 {
		fmt.Fprintf(&b, "- This run is for: %s.\n", strings.Join(ids, ", "))
	}
	if p.Restricted() {
		b.WriteString("- Any other site needs browser.allow_domain {domain, reason}; the user must approve it. Do not ask for it unless the task really needs that site.\n")
	}
	return b.String()
}

// ---------------------------------------------------------------- cart summary

var (
	rePrice     = regexp.MustCompile(`(?:€\s*\d{1,3}(?:[.\s]\d{3})*(?:,\d{1,2})?|\d{1,3}(?:[.\s]\d{3})*,\d{2}\s*€|\d+(?:[.,]\d{2})\s*(?:€|eur\b))`)
	reTotalLine = regexp.MustCompile(`(?i)^(?:.*\b)?(total(?: a pagar| da encomenda| do pedido| da compra| final| geral| com iva)?|a pagar|valor total|order total|grand total|montante total)\b`)
	reNotItem   = regexp.MustCompile(`(?i)\b(sub-?total|total|portes|entrega|envio|desconto|poupan|cup[aã]o|iva|taxa|saldo|cart[aã]o|pontos|shipping|delivery|discount|tax)\b`)
)

// CartSummary pulls the total and the likely item lines from a cart or
// checkout page's text. It is best effort and is shown to the user next to
// the screenshot and the raw page text, never trusted for decisions.
func CartSummary(text string) (total string, items []string) {
	prev := "" // previous non-price line (product names often sit above their price)
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(raw)
		if l == "" || len(l) > 200 {
			continue
		}
		f := Fold(l)
		price := rePrice.FindString(l)
		priceOnly := price != "" && strings.TrimSpace(rePrice.ReplaceAllString(l, "")) == ""
		label := f
		if priceOnly {
			label = Fold(prev)
		}
		isTotal := reTotalLine.MatchString(label) && !strings.HasPrefix(label, "subtotal") && !strings.HasPrefix(label, "sub-total")
		switch {
		case price == "":
			prev = l
			continue
		case isTotal:
			total = strings.TrimSpace(price) // the last total on the page wins
		case priceOnly:
			if prev != "" && !reNotItem.MatchString(label) && len(items) < 40 {
				items = append(items, oneLine(prev+" — "+strings.TrimSpace(price), 160))
			}
		default:
			if !reNotItem.MatchString(f) && len(items) < 40 {
				items = append(items, oneLine(l, 160))
			}
		}
		prev = ""
	}
	return total, items
}
