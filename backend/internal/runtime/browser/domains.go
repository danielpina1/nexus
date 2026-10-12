package browser

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	"google.golang.org/protobuf/types/known/structpb"
)

// DomainPolicy restricts top-level navigation. An unrestricted policy allows
// every http(s) host (navigations are logged).
//
// The allow-list can grow during a run (Add): domains of shop notes fetched
// with shops.get and domains the user approved with browser.allow_domain.
// Copies of a DomainPolicy share the same list. Whether a policy is
// restricted is fixed at creation: a policy created with domains, or with
// NewStrictDomainPolicy, stays restricted; an unrestricted one never becomes
// restricted (Add is then a no-op).
type DomainPolicy struct {
	st *domainState
}

type domainState struct {
	mu         sync.RWMutex
	domains    []string
	restricted bool
}

// NewDomainPolicy normalises entries: "continente.pt", "*.continente.pt",
// "https://www.continente.pt/" and "127.0.0.1:8080" are all accepted. A
// domain also allows its subdomains. No entries = unrestricted.
func NewDomainPolicy(entries []string) DomainPolicy {
	p := DomainPolicy{st: &domainState{}}
	p.add(entries)
	p.st.restricted = len(p.st.domains) > 0
	return p
}

// NewStrictDomainPolicy is like NewDomainPolicy but restricted even when
// entries is empty (nothing is reachable until domains are added).
func NewStrictDomainPolicy(entries []string) DomainPolicy {
	p := NewDomainPolicy(entries)
	p.st.restricted = true
	return p
}

func (p DomainPolicy) add(entries []string) (added []string) {
	seen := map[string]bool{}
	for _, d := range p.st.domains {
		seen[d] = true
	}
	for _, e := range entries {
		d := normDomain(e)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		p.st.domains = append(p.st.domains, d)
		added = append(added, d)
	}
	return added
}

// Add extends a restricted policy's allow-list with valid domains (see
// ValidAllowDomain) and returns the ones that were new. Invalid entries are
// skipped and reported in err. On an unrestricted policy it does nothing.
func (p DomainPolicy) Add(entries ...string) (added []string, err error) {
	if p.st == nil {
		return nil, nil
	}
	var ok []string
	var bad []string
	for _, e := range entries {
		if verr := ValidAllowDomain(e); verr != nil {
			bad = append(bad, verr.Error())
			continue
		}
		ok = append(ok, e)
	}
	if len(bad) > 0 {
		err = fmt.Errorf("%s", strings.Join(bad, "; "))
	}
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	if !p.st.restricted {
		return nil, err
	}
	return p.add(ok), err
}

// commonSuffixes are public second-level suffixes that must never be added
// as a whole (they would allow every site under them).
var commonSuffixes = map[string]bool{
	"com.pt": true, "org.pt": true, "gov.pt": true, "edu.pt": true, "net.pt": true,
	"co.uk": true, "org.uk": true, "com.br": true, "com.es": true, "com.fr": true,
	"co.jp": true, "com.au": true, "co.nz": true, "com.mx": true, "github.io": true,
	"herokuapp.com": true, "appspot.com": true, "blogspot.com": true, "web.app": true,
	"pages.dev": true, "netlify.app": true, "vercel.app": true, "workers.dev": true,
}

// ValidAllowDomain rejects entries that cannot be added to a run's
// allow-list at runtime: empty, a bare TLD ("pt"), a public suffix
// ("com.pt") or something that is not a host name.
func ValidAllowDomain(e string) error {
	d := normDomain(e)
	switch {
	case d == "":
		return fmt.Errorf("empty domain %q", e)
	case net.ParseIP(d) != nil, d == "localhost":
		return nil
	case !strings.Contains(d, "."):
		return fmt.Errorf("%q is not a registrable domain", d)
	case commonSuffixes[d]:
		return fmt.Errorf("%q is a public suffix; name the site (e.g. shop.%s)", d, d)
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return fmt.Errorf("%q is not a valid domain", d)
		}
	}
	return nil
}

func normDomain(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return ""
	}
	if strings.Contains(e, "://") {
		if u, err := url.Parse(e); err == nil {
			e = u.Host
		}
	}
	e = strings.TrimPrefix(e, "*.")
	e = strings.TrimPrefix(e, ".")
	if i := strings.IndexByte(e, '/'); i >= 0 {
		e = e[:i]
	}
	if h, _, err := net.SplitHostPort(e); err == nil {
		e = h
	}
	return strings.TrimSuffix(e, ".")
}

// Restricted reports whether the policy has an allow-list.
func (p DomainPolicy) Restricted() bool {
	if p.st == nil {
		return false
	}
	p.st.mu.RLock()
	defer p.st.mu.RUnlock()
	return p.st.restricted
}

// Domains returns the normalised allow-list.
func (p DomainPolicy) Domains() []string {
	if p.st == nil {
		return nil
	}
	p.st.mu.RLock()
	defer p.st.mu.RUnlock()
	return append([]string(nil), p.st.domains...)
}

// AllowHost reports whether host (optionally with port) is allowed.
func (p DomainPolicy) AllowHost(host string) bool {
	if !p.Restricted() {
		return true
	}
	h := strings.ToLower(host)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	p.st.mu.RLock()
	defer p.st.mu.RUnlock()
	for _, d := range p.st.domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// CheckURL validates a URL for navigation: http(s) only, host allowed.
// about:blank is always allowed.
func (p DomainPolicy) CheckURL(raw string) error {
	if raw == "about:blank" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http(s) URLs can be opened (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	if !p.AllowHost(u.Host) {
		allowed := strings.Join(p.Domains(), ", ")
		if allowed == "" {
			allowed = "none yet"
		}
		return fmt.Errorf("domain %q is not allowed for this run (allowed: %s). For a shop, call shops.get with the store id first; otherwise ask for it with browser.allow_domain {domain, reason} (the user must approve)", u.Hostname(), allowed)
	}
	return nil
}

// domainsFromContext reads browser.allowed_domains from the run context: either
// {"browser": {"allowed_domains": [...]}} or {"browser.allowed_domains": [...]}.
// A comma-separated string is accepted too. ok is false when absent.
func domainsFromContext(c *structpb.Struct) (out []string, ok bool) {
	if c == nil {
		return nil, false
	}
	m := c.AsMap()
	var v any
	if b, isMap := m["browser"].(map[string]any); isMap {
		v, ok = b["allowed_domains"]
	}
	if !ok {
		v, ok = m["browser.allowed_domains"]
	}
	if !ok {
		return nil, false
	}
	switch t := v.(type) {
	case string:
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, e := range t {
			if s, isStr := e.(string); isStr && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out, true
}
