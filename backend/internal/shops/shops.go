// Package shops is the hub module "shops": the store registry for shopping
// agents (docs/SHOPPING.md).
//
// Stores are vault notes in shops/<id>.md:
//
//	---
//	type: shop
//	id: worten
//	name: Worten
//	domains: [worten.pt]          # browser allow-list for this store (subdomains included)
//	country: PT
//	currency: EUR
//	categories: [electronics, appliances, gaming]
//	search_url: "https://www.worten.pt/search?query={q}"
//	login_required: true          # checkout needs an account (log in once in the store profile)
//	checkout_labels: ["Finalizar encomenda", "/pagar agora/"]   # extra SPEND labels; /re/ = regexp
//	research_only: false          # true = price research only (no cart, no checkout)
//	priority: 10                  # lower first in shops.list
//	enabled: true
//	notes: short hint for the agent
//	---
//	# Worten
//	Playbook: how search, cart, checkout and delivery work on this site.
//
// It registers two READ hub tools:
//
//	shops.list {category?, ids?, buyable?, limit?} → {shops: [summary…]}
//	shops.get  {id, query?}                         → the store with its playbook
//
// shops.get is also how a browser run learns a store: the browser runtime
// on the node wraps it and adds the store's domains to the run's allow-list,
// its checkout_labels to the checkout patterns and uses a per-store
// browser profile (internal/runtime/browser/shop.go).
package shops

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/danielpina1/nexus/backend/internal/vault/note"
)

// DefaultDir is the vault folder holding shop notes.
const DefaultDir = "shops"

// Shop is a parsed shops/*.md note.
type Shop struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Domains        []string `json:"domains"`
	Country        string   `json:"country,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	Categories     []string `json:"categories,omitempty"`
	SearchURL      string   `json:"search_url,omitempty"`
	LoginRequired  bool     `json:"login_required"`
	CheckoutLabels []string `json:"checkout_labels,omitempty"`
	ResearchOnly   bool     `json:"research_only,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	Enabled        bool     `json:"enabled"`
	Notes          string   `json:"notes,omitempty"`
	Playbook       string   `json:"playbook,omitempty"`
	Errors         []string `json:"errors,omitempty"`
}

// Valid reports whether the shop parsed without errors.
func (s *Shop) Valid() bool { return len(s.Errors) == 0 }

// SearchLink fills the search_url template ({q}) with query.
func (s *Shop) SearchLink(query string) string {
	if s.SearchURL == "" || strings.TrimSpace(query) == "" || !strings.Contains(s.SearchURL, "{q}") {
		return ""
	}
	return strings.ReplaceAll(s.SearchURL, "{q}", url.QueryEscape(strings.TrimSpace(query)))
}

// Parse parses a shop note. It never fails; problems go to Shop.Errors.
// ok is false when the note is not a shop (type set to something else).
func Parse(p string, src []byte) (s *Shop, ok bool) {
	n := note.Parse(p, src)
	if n.Type != "" && n.Type != "shop" {
		return nil, false
	}
	fm := n.Frontmatter
	s = &Shop{Path: p, Enabled: true}
	addErr := func(f string, a ...any) { s.Errors = append(s.Errors, fmt.Sprintf(f, a...)) }
	if n.FrontmatterErr != nil {
		addErr("%v", n.FrontmatterErr)
	}
	if !n.HasFrontmatter {
		addErr("missing frontmatter")
	}
	s.ID = strings.ToLower(strings.TrimSpace(n.ID))
	if s.ID == "" {
		s.ID = strings.ToLower(strings.TrimSuffix(path.Base(filepath.ToSlash(p)), ".md"))
	}
	s.Name = str(fm["name"])
	if s.Name == "" {
		s.Name = n.Title
	}
	for _, d := range note.StringList(fm["domains"]) {
		if nd := normDomain(d); nd != "" {
			s.Domains = append(s.Domains, nd)
		} else {
			addErr("invalid domain %q", d)
		}
	}
	if len(s.Domains) == 0 {
		addErr("missing domains")
	}
	s.Country = strings.ToUpper(str(fm["country"]))
	s.Currency = strings.ToUpper(str(fm["currency"]))
	for _, c := range note.StringList(fm["categories"]) {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			s.Categories = append(s.Categories, c)
		}
	}
	s.SearchURL = str(fm["search_url"])
	if s.SearchURL != "" {
		u, err := url.Parse(strings.ReplaceAll(s.SearchURL, "{q}", "x"))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !hostIn(u.Hostname(), s.Domains) {
			addErr("search_url %q must be an http(s) URL on the shop's domains", s.SearchURL)
		}
	}
	s.LoginRequired, _ = fm["login_required"].(bool)
	s.ResearchOnly, _ = fm["research_only"].(bool)
	if v, isBool := fm["checkout"].(bool); isBool && !v {
		s.ResearchOnly = true
	}
	s.CheckoutLabels = note.StringList(fm["checkout_labels"])
	if v, ok := fm["priority"]; ok && v != nil {
		i, err := strconv.Atoi(str(v))
		if err != nil {
			addErr("priority must be an integer")
		}
		s.Priority = i
	}
	if v, ok := fm["enabled"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			addErr("enabled must be true or false")
		}
		s.Enabled = b
	}
	s.Notes = strings.Join(strings.Fields(str(fm["notes"])), " ")
	s.Playbook = strings.TrimSpace(n.Body)
	return s, true
}

// Load reads every shop note in dir (non-recursive). Missing dir = none.
func Load(dir string) ([]*Shop, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Shop
	seen := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		s, ok := Parse(path.Join(filepath.Base(dir), e.Name()), b)
		if !ok {
			continue
		}
		if seen[s.ID] {
			s.Errors = append(s.Errors, fmt.Sprintf("duplicate id %q", s.ID))
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	sortShops(out)
	return out, nil
}

func sortShops(s []*Shop) {
	sort.SliceStable(s, func(i, j int) bool {
		pi, pj := prio(s[i]), prio(s[j])
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(s[i].Name) < strings.ToLower(s[j].Name)
	})
}

func prio(s *Shop) int {
	if s.Priority == 0 {
		return 100
	}
	return s.Priority
}

// Filter selects shops for shops.list.
type Filter struct {
	Category string
	IDs      []string
	Buyable  bool // exclude research_only stores
	Limit    int
	// IncludeDisabled also lists disabled and invalid shops.
	IncludeDisabled bool
}

// Select applies f to shops (already sorted).
func Select(shops []*Shop, f Filter) []*Shop {
	ids := map[string]int{}
	for i, id := range f.IDs {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			if _, dup := ids[id]; !dup {
				ids[id] = i
			}
		}
	}
	cat := fold(f.Category)
	var out []*Shop
	for _, s := range shops {
		if !f.IncludeDisabled && (!s.Enabled || !s.Valid()) {
			continue
		}
		if len(ids) > 0 {
			if _, ok := ids[s.ID]; !ok {
				continue
			}
		}
		if f.Buyable && s.ResearchOnly {
			continue
		}
		if cat != "" && !hasCategory(s, cat) {
			continue
		}
		out = append(out, s)
	}
	if len(ids) > 0 { // keep the caller's order
		sort.SliceStable(out, func(i, j int) bool { return ids[out[i].ID] < ids[out[j].ID] })
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

func hasCategory(s *Shop, cat string) bool {
	for _, c := range s.Categories {
		fc := fold(c)
		if fc == cat || strings.Contains(fc, cat) || strings.Contains(cat, fc) {
			return true
		}
	}
	return false
}

// Find looks a shop up by id, name or domain (any host on its domains).
func Find(shops []*Shop, key string) *Shop {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return nil
	}
	for _, s := range shops {
		if s.ID == k {
			return s
		}
	}
	fk := fold(k)
	for _, s := range shops {
		if fold(s.Name) == fk {
			return s
		}
	}
	host := normDomain(k)
	var best *Shop
	bestLen := 0
	for _, s := range shops {
		for _, d := range s.Domains {
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > bestLen {
				best, bestLen = s, len(d)
			}
		}
	}
	return best
}

func hostIn(h string, domains []string) bool {
	h = strings.ToLower(h)
	for _, d := range domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// normDomain normalises "https://www.x.pt/", "*.x.pt", "X.PT" → "www.x.pt" / "x.pt".
func normDomain(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if strings.Contains(e, "://") {
		if u, err := url.Parse(e); err == nil {
			e = u.Host
		}
	}
	e = strings.TrimPrefix(strings.TrimPrefix(e, "*."), ".")
	if i := strings.IndexAny(e, "/:"); i >= 0 {
		e = e[:i]
	}
	e = strings.TrimSuffix(e, ".")
	if e == "" || !strings.Contains(e, ".") && e != "localhost" {
		return ""
	}
	for _, r := range e {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return ""
		}
	}
	return e
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// fold lower-cases and strips accents ("Eletrodomésticos" → "eletrodomesticos").
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
