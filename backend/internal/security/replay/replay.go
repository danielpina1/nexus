// Package replay implements ReplayGuard: timestamp window + nonce cache +
// per-sender monotonic counter, shared by sealed envelopes and signed
// requests.
package replay

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Defaults.
const (
	DefaultWindow     = 60 * time.Second
	DefaultMaxEntries = 1 << 20
	MinNonceLen       = 16
)

// Errors returned by Check. All of them mean "reject the message".
var (
	ErrStale        = errors.New("replay: timestamp outside the allowed window")
	ErrNonceReused  = errors.New("replay: nonce already seen")
	ErrShortNonce   = errors.New("replay: nonce must be at least 16 bytes")
	ErrCounter      = errors.New("replay: counter not greater than the last seen")
	ErrCounterUnset = errors.New("replay: counter required")
	ErrFull         = errors.New("replay: nonce cache full")
)

// Guard rejects replayed or reordered messages. It is safe for concurrent use.
//
// A message is accepted iff:
//   - |now - ts| <= Window,
//   - len(nonce) >= 16 and (sender, nonce) was not seen in the last 2*Window
//     (nonces older than that are outside the timestamp window anyway),
//   - counter > last accepted counter for sender (when counter != 0; a zero
//     counter is rejected if RequireCounter is set, otherwise only the nonce
//     and timestamp checks apply).
//
// Check is atomic: on success the nonce and counter are recorded.
type Guard struct {
	Window         time.Duration
	RequireCounter bool
	MaxEntries     int
	Now            func() time.Time

	mu       sync.Mutex
	nonces   map[string]time.Time // sender\x00nonce → expiry
	counters map[string]uint64
	checks   int
}

// New returns a guard with the given window (DefaultWindow if <= 0).
func New(window time.Duration) *Guard {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Guard{Window: window}
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Check validates and records a message from sender.
func (g *Guard) Check(sender string, nonce []byte, counter uint64, ts time.Time) error {
	if len(nonce) < MinNonceLen {
		return ErrShortNonce
	}
	window := g.Window
	if window <= 0 {
		window = DefaultWindow
	}
	now := g.now()
	if d := now.Sub(ts); d > window || d < -window {
		return fmt.Errorf("%w (skew %s)", ErrStale, d.Round(time.Second))
	}
	if counter == 0 && g.RequireCounter {
		return ErrCounterUnset
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nonces == nil {
		g.nonces = map[string]time.Time{}
		g.counters = map[string]uint64{}
	}
	g.checks++
	max := g.MaxEntries
	if max <= 0 {
		max = DefaultMaxEntries
	}
	if g.checks%1024 == 0 || len(g.nonces) >= max {
		g.pruneLocked(now)
	}
	key := sender + "\x00" + string(nonce)
	if exp, ok := g.nonces[key]; ok && now.Before(exp) {
		return ErrNonceReused
	}
	if counter != 0 {
		if last, ok := g.counters[sender]; ok && counter <= last {
			return fmt.Errorf("%w (got %d, last %d)", ErrCounter, counter, last)
		}
	}
	if len(g.nonces) >= max {
		return ErrFull // fail closed rather than forget nonces
	}
	g.nonces[key] = now.Add(2 * window)
	if counter != 0 {
		g.counters[sender] = counter
	}
	return nil
}

func (g *Guard) pruneLocked(now time.Time) {
	for k, exp := range g.nonces {
		if !now.Before(exp) {
			delete(g.nonces, k)
		}
	}
}

// Counters returns a snapshot of the last accepted counter per sender, for
// persistence across restarts.
func (g *Guard) Counters() map[string]uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]uint64, len(g.counters))
	for k, v := range g.counters {
		out[k] = v
	}
	return out
}

// RestoreCounters seeds counters (e.g. loaded from disk). Existing higher
// values are kept.
func (g *Guard) RestoreCounters(m map[string]uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counters == nil {
		g.nonces = map[string]time.Time{}
		g.counters = map[string]uint64{}
	}
	for k, v := range m {
		if v > g.counters[k] {
			g.counters[k] = v
		}
	}
}

// Forget drops the counter for a sender (e.g. after revocation/re-pairing).
func (g *Guard) Forget(sender string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.counters, sender)
}
