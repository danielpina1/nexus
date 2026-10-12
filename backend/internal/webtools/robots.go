package webtools

import (
	"bufio"
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// robotsRules are the Allow/Disallow lines of the group that applies to us.
type robotsRules struct {
	allow, disallow []string
	fetched         time.Time
}

type robotsCache struct {
	mu    sync.Mutex
	hosts map[string]*robotsRules
	ttl   time.Duration
}

func newRobotsCache() *robotsCache {
	return &robotsCache{hosts: map[string]*robotsRules{}, ttl: 12 * time.Hour}
}

// robotsAgent is the product token matched against User-agent lines.
const robotsAgent = "nexusresearch"

func (c *robotsCache) allowed(ctx context.Context, f *Fetcher, u *url.URL) (bool, error) {
	origin := u.Scheme + "://" + u.Host
	c.mu.Lock()
	r, ok := c.hosts[origin]
	c.mu.Unlock()
	if !ok || time.Since(r.fetched) > c.ttl {
		r = &robotsRules{fetched: time.Now()}
		resp, body, _, err := f.download(ctx, origin+"/robots.txt", "text/plain")
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			// Unreachable robots.txt: treat as allow-all.
		} else if resp.StatusCode == 200 {
			r = parseRobots(body, robotsAgent)
			r.fetched = time.Now()
		}
		c.mu.Lock()
		c.hosts[origin] = r
		c.mu.Unlock()
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return r.allows(p), nil
}

// parseRobots extracts the rules for agent (falling back to "*").
func parseRobots(b []byte, agent string) *robotsRules {
	type group struct {
		agents          []string
		allow, disallow []string
	}
	var groups []*group
	var cur *group
	lastWasAgent := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 512<<10)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if !lastWasAgent || cur == nil {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(v))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if cur == nil {
				continue
			}
			if k == "allow" {
				cur.allow = append(cur.allow, v)
			} else if v != "" {
				cur.disallow = append(cur.disallow, v)
			}
		default:
			lastWasAgent = false
		}
	}
	var star, mine *group
	for _, g := range groups {
		for _, a := range g.agents {
			if a == "*" && star == nil {
				star = g
			} else if a != "*" && strings.Contains(agent, a) && mine == nil {
				mine = g
			}
		}
	}
	if mine == nil {
		mine = star
	}
	if mine == nil {
		return &robotsRules{}
	}
	return &robotsRules{allow: mine.allow, disallow: mine.disallow}
}

// allows applies longest-match semantics (Allow wins ties), with * and $.
func (r *robotsRules) allows(path string) bool {
	best, allowed := -1, true
	for _, p := range r.allow {
		if p != "" && robotsMatch(p, path) && len(p) >= best {
			best, allowed = len(p), true
		}
	}
	for _, p := range r.disallow {
		if robotsMatch(p, path) && len(p) > best {
			best, allowed = len(p), false
		}
	}
	return allowed
}

func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	for _, part := range parts[1:] {
		i := strings.Index(path[pos:], part)
		if i < 0 {
			return false
		}
		pos += i + len(part)
	}
	if anchored {
		if len(parts) > 1 && parts[len(parts)-1] == "" {
			return true
		}
		return pos == len(path) || strings.HasSuffix(path, parts[len(parts)-1])
	}
	return true
}
