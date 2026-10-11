package replay

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func n(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }

func TestGuard(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := New(time.Minute)
	g.Now = func() time.Time { return now }

	steps := []struct {
		name    string
		sender  string
		nonce   []byte
		counter uint64
		ts      time.Time
		want    error
	}{
		{"ok", "a", n(1), 1, now, nil},
		{"replayed nonce", "a", n(1), 2, now, ErrNonceReused},
		{"same nonce other sender", "b", n(1), 1, now, nil},
		{"counter not increasing", "a", n(2), 1, now, ErrCounter},
		{"counter increases", "a", n(3), 5, now, nil},
		{"counter gap ok, lower rejected", "a", n(4), 4, now, ErrCounter},
		{"too old", "a", n(5), 6, now.Add(-61 * time.Second), ErrStale},
		{"too new", "a", n(6), 6, now.Add(61 * time.Second), ErrStale},
		{"edge of window", "a", n(7), 6, now.Add(-60 * time.Second), nil},
		{"short nonce", "a", []byte("short"), 7, now, ErrShortNonce},
		{"zero counter allowed", "a", n(8), 0, now, nil},
	}
	for _, s := range steps {
		err := g.Check(s.sender, s.nonce, s.counter, s.ts)
		if !errors.Is(err, s.want) {
			t.Fatalf("%s: got %v want %v", s.name, err, s.want)
		}
	}
}

func TestGuardRequireCounterAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := New(time.Minute)
	g.RequireCounter = true
	g.Now = func() time.Time { return now }
	if err := g.Check("a", n(1), 0, now); !errors.Is(err, ErrCounterUnset) {
		t.Fatal(err)
	}
	if err := g.Check("a", n(1), 1, now); err != nil {
		t.Fatal(err)
	}
	// After 2*window the nonce entry expires; the timestamp check covers it.
	now = now.Add(3 * time.Minute)
	if err := g.Check("a", n(1), 2, now.Add(-2*time.Minute)); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	g.pruneLocked(now)
	if len(g.nonces) != 0 {
		t.Fatal("expected prune")
	}
}

func TestGuardFullFailsClosed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := New(time.Minute)
	g.MaxEntries = 2
	g.Now = func() time.Time { return now }
	_ = g.Check("a", n(1), 0, now)
	_ = g.Check("a", n(2), 0, now)
	if err := g.Check("a", n(3), 0, now); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
}

func TestCountersSnapshot(t *testing.T) {
	g := New(0)
	now := time.Now()
	_ = g.Check("a", n(1), 9, now)
	snap := g.Counters()
	g2 := New(0)
	g2.RestoreCounters(snap)
	if err := g2.Check("a", n(2), 9, now); !errors.Is(err, ErrCounter) {
		t.Fatal("restored counter not enforced")
	}
	g2.Forget("a")
	if err := g2.Check("a", n(3), 1, now); err != nil {
		t.Fatal(err)
	}
}
