package browser

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
)

// DomainPolicy restricts top-level navigation. An empty policy allows every
// http(s) host (navigations are logged).
type DomainPolicy struct {
	domains []string
}

// NewDomainPolicy normalises entries: "continente.pt", "*.continente.pt",
// "https://www.continente.pt/" and "127.0.0.1:8080" are all accepted. A
// domain also allows its subdomains.
func NewDomainPolicy(entries []string) DomainPolicy {
	var p DomainPolicy
	seen := map[string]bool{}
	for _, e := range entries {
		d := normDomain(e)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		p.domains = append(p.domains, d)
	}
	return p
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
func (p DomainPolicy) Restricted() bool { return len(p.domains) > 0 }

// Domains returns the normalised allow-list.
func (p DomainPolicy) Domains() []string { return append([]string(nil), p.domains...) }

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
	for _, d := range p.domains {
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
		return fmt.Errorf("domain %q is not allowed for this agent (allowed: %s)", u.Hostname(), strings.Join(p.domains, ", "))
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
