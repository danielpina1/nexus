package pairing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEmojiTable(t *testing.T) {
	seen := map[string]bool{}
	for i, e := range Emojis {
		if e == "" || seen[e] || utf8.RuneCountInString(e) != 1 {
			t.Fatalf("emoji %d %q invalid or duplicate", i, e)
		}
		seen[e] = true
	}
}

func TestProof(t *testing.T) {
	_, secret, _ := NewSecret()
	a, b, c := []byte("auth"), []byte("appr"), []byte("enc")
	p, err := Proof(secret, a, b, c)
	if err != nil || len(p) != 32 {
		t.Fatal(err)
	}
	if err := VerifyProof(secret, a, b, c, p); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProof(secret, []byte("Auth"), b, c, p); !errors.Is(err, ErrBadProof) {
		t.Fatal("swapped key accepted")
	}
	_, other, _ := NewSecret()
	if err := VerifyProof(other, a, b, c, p); !errors.Is(err, ErrBadProof) {
		t.Fatal("other secret accepted")
	}
}

func TestSafetyCode(t *testing.T) {
	a, b := []byte{1, 2, 3}, []byte{9, 9}
	c1 := SafetyCode(a, b)
	c2 := SafetyCode(b, nil, a)
	if c1 != c2 {
		t.Fatal("order-dependent")
	}
	if n := len(strings.Split(c1, " ")); n != 6 {
		t.Fatalf("got %d emojis", n)
	}
	if SafetyCode(a, []byte{9, 8}) == c1 {
		t.Fatal("different keys, same code (unlikely)")
	}
}

func TestQRRoundTrip(t *testing.T) {
	tok, _, _ := NewSecret()
	q := &QR{Version: 1, Kind: KindDevice, HubURLs: []string{"https://hub.ts.net:7700", "https://10.0.0.2:7700"},
		CAFingerprint: strings.Repeat("ab", 32), Token: tok, HubKey: "aGs", Expires: 1800000000}
	for _, s := range []string{q.URL(), q.Base64()} {
		got, err := ParseQR(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got.Token != tok || len(got.HubURLs) != 2 || got.HubURLs[1] != q.HubURLs[1] || got.Expires != q.Expires || got.Kind != KindDevice {
			t.Fatalf("mismatch %+v", got)
		}
	}
	if !strings.HasPrefix(q.URL(), "nexus://pair?") {
		t.Fatal(q.URL())
	}
	for _, bad := range []string{"nexus://other?v=1", "!!!", (&QR{Version: 1}).Base64()} {
		if _, err := ParseQR(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestManagerSingleUseAndTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	m := &Manager{Store: &MemoryStore{}, Now: func() time.Time { return now }}
	tok, exp, err := m.New(ctx, KindDevice, "iphone")
	if err != nil || exp.Sub(now) != DefaultTTL {
		t.Fatal(err)
	}
	if _, _, err := m.Redeem(ctx, tok, KindNode); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("wrong kind accepted")
	}
	// Wrong-kind attempt consumed it (fail closed).
	if _, _, err := m.Redeem(ctx, tok, KindDevice); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("token reusable after failed attempt")
	}

	tok, _, _ = m.New(ctx, KindDevice, "")
	secret, p, err := m.Redeem(ctx, tok, KindDevice)
	if err != nil || len(secret) != SecretLen || p.Kind != KindDevice {
		t.Fatal(err)
	}
	if _, _, err := m.Redeem(ctx, tok, KindDevice); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("double use")
	}

	tok, _, _ = m.New(ctx, KindDevice, "")
	now = now.Add(DefaultTTL)
	if _, _, err := m.Redeem(ctx, tok, KindDevice); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired accepted")
	}
	if _, _, err := m.Redeem(ctx, "garbage", ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("garbage accepted")
	}
	if _, _, err := m.New(ctx, "admin", ""); err == nil {
		t.Fatal("bad kind accepted")
	}
}
