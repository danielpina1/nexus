package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultHubURL is the recommended hub URL (docs/NETWORK.md).
const DefaultHubURL = "https://nexus.home.arpa:7700"

// urlList is a repeatable, comma-separated flag.
type urlList []string

func (u *urlList) String() string { return strings.Join(*u, ",") }

func (u *urlList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*u = append(*u, p)
		}
	}
	return nil
}

// netConfig is what `nexusd init` writes into the server/security sections.
type netConfig struct {
	Listen      string
	HubURLs     []string
	ServerNames []string
	TLS         bool
}

// newNetConfig validates -hub-url / -listen and derives server_names (the
// URL hosts + localhost/127.0.0.1 + the listen IP).
func newNetConfig(hubURLs []string, listen string) (netConfig, error) {
	if len(hubURLs) == 0 {
		hubURLs = []string{DefaultHubURL}
	}
	if listen == "" {
		listen = ":7700"
	}
	if _, port, err := net.SplitHostPort(listen); err != nil || port == "" {
		return netConfig{}, fmt.Errorf("-listen %q: want host:port or :port", listen)
	}
	nc := netConfig{Listen: listen}
	add := func(h string) {
		if h != "" && !slices.Contains(nc.ServerNames, h) {
			nc.ServerNames = append(nc.ServerNames, h)
		}
	}
	httpsCount := 0
	for _, raw := range hubURLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return netConfig{}, fmt.Errorf("-hub-url %q: want https://host:port", raw)
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
			return netConfig{}, fmt.Errorf("-hub-url %q: only scheme://host:port is allowed", raw)
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return netConfig{}, fmt.Errorf("-hub-url %q: bad port", raw)
			}
		}
		if u.Scheme == "https" {
			httpsCount++
		}
		nc.HubURLs = append(nc.HubURLs, u.Scheme+"://"+u.Host)
		add(u.Hostname())
	}
	if httpsCount != 0 && httpsCount != len(nc.HubURLs) {
		return netConfig{}, fmt.Errorf("-hub-url: mix of http:// and https:// URLs")
	}
	nc.TLS = httpsCount > 0
	add("localhost")
	add("127.0.0.1")
	if h, _, _ := net.SplitHostPort(listen); h != "" {
		if ip := net.ParseIP(h); ip != nil && !ip.IsUnspecified() {
			add(h)
		}
	}
	return nc, nil
}

var (
	reListen      = regexp.MustCompile(`(?m)^  listen: .*$`)
	reTLSAuto     = regexp.MustCompile(`(?m)^    auto: (true|false)$`)
	reHubURLs     = regexp.MustCompile(`(?m)^  hub_urls: .*$`)
	reServerNames = regexp.MustCompile(`(?m)^  server_names: .*$`)
)

func flowList(v []string) string {
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// apply rewrites the server/security lines of the example config (keeping
// its comments) and checks the result.
func (nc netConfig) apply(body []byte) ([]byte, error) {
	repl := []struct {
		re  *regexp.Regexp
		val string
	}{
		{reListen, "  listen: " + strconv.Quote(nc.Listen)},
		{reTLSAuto, "    auto: " + strconv.FormatBool(nc.TLS)},
		{reHubURLs, "  hub_urls: " + flowList(nc.HubURLs)},
		{reServerNames, "  server_names: " + flowList(nc.ServerNames)},
	}
	for _, r := range repl {
		loc := r.re.FindIndex(body)
		if loc == nil {
			return nil, fmt.Errorf("init: example config has no line matching %s", r.re)
		}
		body = append(append(append([]byte(nil), body[:loc[0]]...), r.val...), body[loc[1]:]...)
	}
	var check struct {
		Server struct {
			Listen string `yaml:"listen"`
			TLS    struct {
				Auto bool `yaml:"auto"`
			} `yaml:"tls"`
		} `yaml:"server"`
		Security struct {
			HubURLs     []string `yaml:"hub_urls"`
			ServerNames []string `yaml:"server_names"`
		} `yaml:"security"`
	}
	if err := yaml.Unmarshal(body, &check); err != nil {
		return nil, fmt.Errorf("init: generated config: %w", err)
	}
	if check.Server.Listen != nc.Listen || check.Server.TLS.Auto != nc.TLS ||
		!slices.Equal(check.Security.HubURLs, nc.HubURLs) || !slices.Equal(check.Security.ServerNames, nc.ServerNames) {
		return nil, fmt.Errorf("init: generated config does not round-trip the network settings")
	}
	return body, nil
}

// printNextSteps prints the docs/NETWORK.md checklist for this config.
func printNextSteps(w io.Writer, cfgPath string, nc netConfig) {
	scheme := "http"
	if nc.TLS {
		scheme = "https"
	}
	_, port, _ := net.SplitHostPort(nc.Listen)
	fmt.Fprintf(w, `
  hub URLs (pairing QR): %s
  certificate names:     %s
  listen:                %s   TLS: %v

Next steps (docs/NETWORK.md):
  [ ] UniFi: put the hub on the "nexus" network with a fixed IP
  [ ] UniFi: local DNS record for %s → that IP
  [ ] UniFi: WireGuard VPN server + iPhone client with On-Demand (or Teleport)
  [ ] UniFi firewall: allow TCP %s to the hub from home LAN, VPN clients and the nexus VLAN
  [ ] Start the hub:          nexusd serve -config %s
  [ ] Pair the iPhone:        nexusd pair -config %s        (scan on home Wi-Fi)
  [ ] Pair each machine:      nexusd pair -kind node -config %s
                              then on the machine: nexus-node pair '<nexus://pair URL>'
  [ ] CLI on this machine:    export NEXUS_URL=%s://localhost:%s NEXUS_CA_FILE=<data_dir>/security/pki/ca.crt
  [ ] Test once on cellular with the VPN on
`, strings.Join(nc.HubURLs, ", "), strings.Join(nc.ServerNames, ", "), nc.Listen, nc.TLS,
		firstHost(nc.HubURLs), port, cfgPath, cfgPath, cfgPath, scheme, port)
}

func firstHost(urls []string) string {
	for _, raw := range urls {
		if u, err := url.Parse(raw); err == nil {
			return u.Hostname()
		}
	}
	return "nexus.home.arpa"
}
