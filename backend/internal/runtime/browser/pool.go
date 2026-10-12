package browser

import (
	"sync"
)

// sessionPool holds a run's browser sessions, one per profile. A run that
// shops at several stores gets one Chrome per store profile (started lazily
// on first use), so each store's login lives in its own persistent profile.
// Tools act on the current session; browser.navigate switches to the
// session of the target host's profile.
type sessionPool struct {
	// mk creates (but does not start) the session for a profile.
	mk func(profile string) *session
	// profileFor maps a host to a profile ("" = the default profile).
	profileFor func(host string) string
	def        string

	mu     sync.Mutex
	byName map[string]*session
	order  []*session
	cur    *session
	closed bool

	once sync.Once
	done chan struct{}
}

func newSessionPool(def string, mk func(string) *session, profileFor func(string) string) *sessionPool {
	return &sessionPool{mk: mk, profileFor: profileFor, def: def, byName: map[string]*session{}, done: make(chan struct{})}
}

// getLocked returns the session for profile, creating it. Caller holds p.mu.
func (p *sessionPool) getLocked(profile string) *session {
	if profile == "" {
		profile = p.def
	}
	if s, ok := p.byName[profile]; ok {
		return s
	}
	s := p.mk(profile)
	if p.closed {
		s.shutdown(false) // unusable: actions fail with errClosed
	}
	p.byName[profile] = s
	p.order = append(p.order, s)
	return s
}

// current returns the session tools act on (the default profile's until a
// navigation switched it).
func (p *sessionPool) current() *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		p.cur = p.getLocked(p.def)
	}
	return p.cur
}

// forHost switches to (and returns) the session of host's profile.
func (p *sessionPool) forHost(host string) (s *session, profile string) {
	profile = ""
	if p.profileFor != nil && host != "" {
		profile = p.profileFor(host)
	}
	if profile == "" {
		profile = p.def
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur = p.getLocked(profile)
	return p.cur, profile
}

// profiles returns the profile names used so far.
func (p *sessionPool) profiles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.byName))
	for n := range p.byName {
		out = append(out, n)
	}
	return out
}

// shutdown closes (graceful) or kills every session and waits until all
// Chromes are gone and their profiles released.
func (p *sessionPool) shutdown(graceful bool) {
	p.mu.Lock()
	p.closed = true
	all := append([]*session(nil), p.order...)
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			s.shutdown(graceful)
			<-s.done
		}(s)
	}
	wg.Wait()
	p.once.Do(func() { close(p.done) })
}

// Close shuts every browser down gracefully.
func (p *sessionPool) Close() { p.shutdown(true) }

// Kill terminates every browser.
func (p *sessionPool) Kill() { p.shutdown(false) }
