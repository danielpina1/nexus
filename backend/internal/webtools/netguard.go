package webtools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned when the SSRF guard refuses a destination.
var ErrBlocked = errors.New("destination blocked")

// extraBlocked are non-public ranges not covered by netip's predicates.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (Tailscale, carrier NAT)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved + broadcast
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
}

// IsPublicAddr reports whether ip is a globally routable unicast address.
func IsPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return false
	}
	for _, p := range extraBlocked {
		if p.Contains(ip) {
			return false
		}
	}
	// NAT64 / 6to4 / Teredo embed IPv4 addresses: check the embedded one.
	if ip.Is6() {
		b := ip.As16()
		switch {
		case netip.MustParsePrefix("64:ff9b::/96").Contains(ip):
			return IsPublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case netip.MustParsePrefix("2002::/16").Contains(ip):
			return IsPublicAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return true
}

// Guard decides which destinations web.fetch may reach.
type Guard struct {
	AllowPrivate bool
	allowNets    []netip.Prefix
	allowNames   []string
	denyNets     []netip.Prefix
	denyNames    []string
	// AddrOK overrides the address check (tests). Called with the resolved
	// address and port; nil = IsPublicAddr.
	AddrOK func(ip netip.Addr, port int) bool
}

// NewGuard builds a guard from host lists (names, "*.suffix" or CIDRs).
func NewGuard(allowPrivate bool, allow, deny []string) *Guard {
	g := &Guard{AllowPrivate: allowPrivate}
	parse := func(list []string, nets *[]netip.Prefix, names *[]string) {
		for _, h := range list {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			if p, err := netip.ParsePrefix(h); err == nil {
				*nets = append(*nets, p.Masked())
			} else if a, err := netip.ParseAddr(h); err == nil {
				*nets = append(*nets, netip.PrefixFrom(a, a.BitLen()))
			} else {
				*names = append(*names, h)
			}
		}
	}
	parse(allow, &g.allowNets, &g.allowNames)
	parse(deny, &g.denyNets, &g.denyNames)
	return g
}

func matchName(host string, pats []string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, p := range pats {
		if suf, ok := strings.CutPrefix(p, "*."); ok {
			if host == suf || strings.HasSuffix(host, "."+suf) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

func inNets(ip netip.Addr, nets []netip.Prefix) bool {
	ip = ip.Unmap()
	for _, p := range nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckURL validates the scheme and host name of u (before resolution).
func (g *Guard) CheckURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http and https URLs are allowed (got %q)", ErrBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: URL has no host", ErrBlocked)
	}
	if u.User != nil {
		return fmt.Errorf("%w: URLs with credentials are not allowed", ErrBlocked)
	}
	if matchName(host, g.denyNames) {
		return fmt.Errorf("%w: host %s is denied by config", ErrBlocked, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return g.checkAddr(host, ip, portOf(u))
	}
	return nil
}

func portOf(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

// checkAddr validates one resolved address for host.
func (g *Guard) checkAddr(host string, ip netip.Addr, port int) error {
	ip = ip.Unmap()
	if inNets(ip, g.denyNets) {
		return fmt.Errorf("%w: %s (%s) is denied by config", ErrBlocked, host, ip)
	}
	if g.AllowPrivate || inNets(ip, g.allowNets) || (host != "" && matchName(host, g.allowNames)) {
		return nil
	}
	ok := IsPublicAddr(ip)
	if g.AddrOK != nil {
		ok = g.AddrOK(ip, port)
	}
	if !ok {
		return fmt.Errorf("%w: %s resolves to non-public address %s (set tools.web fetch.allow_hosts to allow it)", ErrBlocked, host, ip)
	}
	return nil
}

// CheckResolved resolves u's host and checks every address (used before
// requests that go through a proxy, where dial-time checks are impossible).
func (g *Guard) CheckResolved(ctx context.Context, r *net.Resolver, u *url.URL) error {
	if err := g.CheckURL(u); err != nil {
		return err
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := g.checkAddr(host, a, portOf(u)); err != nil {
			return err
		}
	}
	return nil
}

type hostKey struct{}

// dialControl runs after DNS resolution, right before connect, so it sees the
// real address (no DNS-rebinding window) for every connection, including
// redirects.
func (g *Guard) dialControl(ctx context.Context) func(network, address string, c syscall.RawConn) error {
	host, _ := ctx.Value(hostKey{}).(string)
	return func(network, address string, _ syscall.RawConn) error {
		h, p, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(h)
		if err != nil {
			return fmt.Errorf("%w: unexpected dial address %q", ErrBlocked, address)
		}
		port, _ := strconv.Atoi(p)
		return g.checkAddr(host, ip, port)
	}
}

// NewClient returns an HTTP client whose every connection is checked by g.
// With useProxy, requests go through the environment proxy and are checked
// by resolving the host before each request (and each redirect) instead.
func (g *Guard) NewClient(timeout time.Duration, useProxy bool) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}
	if useProxy {
		tr.Proxy = http.ProxyFromEnvironment
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		tr.DialContext = d.DialContext
	} else {
		tr.Proxy = nil
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			h, _, _ := net.SplitHostPort(addr)
			d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
				Control: g.dialControl(context.WithValue(ctx, hostKey{}, h))}
			return d.DialContext(ctx, network, addr)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", MaxRedirects)
			}
			if useProxy {
				return g.CheckResolved(req.Context(), nil, req.URL)
			}
			return g.CheckURL(req.URL)
		},
	}
}
