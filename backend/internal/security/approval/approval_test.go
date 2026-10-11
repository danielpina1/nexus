package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

func TestCanonicalJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{` { "a" : [ 1 , 2.50, true, null, "x" ] } `, `{"a":[1,2.5,true,null,"x"]}`},
		{`{"n":1e21,"m":1e20,"s":1e-7,"t":0.000001,"neg":-0,"f":123.456}`,
			`{"f":123.456,"m":100000000000000000000,"n":1e+21,"neg":0,"s":1e-7,"t":0.000001}`},
		{`"é\/ \u0001"`, "\"é/ \\u0001\""},
		{`{"€":1,"😀":2,"דּ":3}`, "{\"€\":1,\"😀\":2,\"דּ\":3}"}, // UTF-16 order
		{`[4.35, 0.1, 1.7976931348623157e308, 5e-324]`, `[4.35,0.1,1.7976931348623157e+308,5e-324]`},
	}
	for _, c := range cases {
		got, err := CanonicalJSON([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"a":1} x`, `[1,`} {
		if _, err := CanonicalJSON([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestActionHashStructAndMap(t *testing.T) {
	m := map[string]any{"total": 84.3, "items": []any{"milk", "bread"}, "slot": "2026-10-12T10:00"}
	s, _ := structpb.NewStruct(m)
	h1, err := ActionHash(m)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ActionHash(s)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(h1) != hex.EncodeToString(h2) {
		t.Fatal("struct and map hash differ")
	}
	want := sha256.Sum256([]byte(`{"items":["milk","bread"],"slot":"2026-10-12T10:00","total":84.3}`))
	if string(h1) != string(want[:]) {
		t.Fatal("unexpected hash")
	}
	m["total"] = 84.31
	h3, _ := ActionHash(m)
	if string(h3) == string(h1) {
		t.Fatal("hash ignores a cent")
	}
	if _, err := ActionHashProto(s); err != nil {
		t.Fatal(err)
	}
}

func TestMessageLayout(t *testing.T) {
	d := &nexusv1.SignedApproval{
		ApprovalId:   "apr_1",
		Approve:      true,
		Challenge:    []byte{0xAA, 0xBB},
		ActionSha256: make([]byte, 32),
		ExpiresAt:    &timestamppb.Timestamp{Seconds: 0x0102030405},
	}
	got := hex.EncodeToString(Message(d))
	want := hex.EncodeToString([]byte("nexus/v1/approval")) + "00" +
		"00000005" + hex.EncodeToString([]byte("apr_1")) +
		"01" +
		"00000002" + "aabb" +
		hex.EncodeToString(make([]byte, 32)) +
		"0000000102030405"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

type fx struct {
	key    *ecdsa.PrivateKey
	lookup KeyLookup
	now    time.Time
	hash   []byte
}

func newFx(t *testing.T) *fx {
	k, _ := keys.Generate()
	revoked := map[string]bool{"dev_revoked": true}
	h := sha256.Sum256([]byte("action"))
	return &fx{
		key: k,
		lookup: KeyLookupFunc(func(_ context.Context, id string) (*ecdsa.PublicKey, error) {
			if revoked[id] {
				return nil, ErrRevoked
			}
			if id == "dev_1" || id == "dev_revoked" {
				return &k.PublicKey, nil
			}
			return nil, ErrUnknownDevice
		}),
		now:  time.Unix(1_800_000_000, 0),
		hash: h[:],
	}
}

func (f *fx) decision(t *testing.T, mut func(*nexusv1.SignedApproval)) *nexusv1.SignedApproval {
	d := &nexusv1.SignedApproval{
		ApprovalId:   "apr_1",
		Approve:      true,
		Challenge:    []byte("challenge-bytes-123"),
		ActionSha256: append([]byte(nil), f.hash...),
		ExpiresAt:    timestamppb.New(f.now.Add(5 * time.Minute)),
		DeviceId:     "dev_1",
	}
	if mut != nil {
		mut(d)
	}
	if err := Sign(d, f.key); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestVerify(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	other := sha256.Sum256([]byte("other action"))
	otherKey, _ := keys.Generate()

	cases := []struct {
		name  string
		d     func() *nexusv1.SignedApproval
		hash  []byte
		now   time.Time
		want  error
		isErr bool
	}{
		{name: "ok", d: func() *nexusv1.SignedApproval { return f.decision(t, nil) }},
		{name: "ok denial", d: func() *nexusv1.SignedApproval {
			return f.decision(t, func(d *nexusv1.SignedApproval) { d.Approve = false })
		}},
		{name: "wrong hash", d: func() *nexusv1.SignedApproval { return f.decision(t, nil) }, hash: other[:], want: ErrHashMismatch},
		{name: "expired", d: func() *nexusv1.SignedApproval { return f.decision(t, nil) }, now: f.now.Add(5 * time.Minute), want: ErrExpired},
		{name: "too long", d: func() *nexusv1.SignedApproval {
			return f.decision(t, func(d *nexusv1.SignedApproval) { d.ExpiresAt = timestamppb.New(f.now.Add(48 * time.Hour)) })
		}, want: ErrTooLong},
		{name: "revoked", d: func() *nexusv1.SignedApproval {
			return f.decision(t, func(d *nexusv1.SignedApproval) { d.DeviceId = "dev_revoked" })
		}, want: ErrRevoked},
		{name: "unknown device", d: func() *nexusv1.SignedApproval {
			return f.decision(t, func(d *nexusv1.SignedApproval) { d.DeviceId = "dev_x" })
		}, want: ErrUnknownDevice},
		{name: "flipped approve", d: func() *nexusv1.SignedApproval {
			d := f.decision(t, func(d *nexusv1.SignedApproval) { d.Approve = false })
			d.Approve = true
			return d
		}, want: ErrBadSignature},
		{name: "tampered challenge", d: func() *nexusv1.SignedApproval {
			d := f.decision(t, nil)
			d.Challenge[0] ^= 1
			return d
		}, want: ErrBadSignature},
		{name: "extended expiry", d: func() *nexusv1.SignedApproval {
			d := f.decision(t, nil)
			d.ExpiresAt = timestamppb.New(f.now.Add(10 * time.Minute))
			return d
		}, want: ErrBadSignature},
		{name: "signed by another key", d: func() *nexusv1.SignedApproval {
			d := f.decision(t, nil)
			_ = Sign(d, otherKey)
			return d
		}, want: ErrBadSignature},
		{name: "malformed hash", d: func() *nexusv1.SignedApproval {
			d := f.decision(t, nil)
			d.ActionSha256 = d.ActionSha256[:31]
			return d
		}, want: ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hash := c.hash
			if hash == nil {
				hash = f.hash
			}
			now := c.now
			if now.IsZero() {
				now = f.now
			}
			err := Verify(ctx, c.d(), hash, f.lookup, now)
			if c.want == nil && err != nil {
				t.Fatalf("unexpected %v", err)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v want %v", err, c.want)
			}
		})
	}
}

func TestVerifyFor(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	d := f.decision(t, nil)
	a := &nexusv1.Approval{Id: "apr_1", Challenge: []byte("challenge-bytes-123"), PayloadHash: f.hash,
		ExpiresAt: timestamppb.New(f.now.Add(time.Hour))}
	if err := VerifyFor(ctx, d, a, f.lookup, f.now); err != nil {
		t.Fatal(err)
	}
	a2 := &nexusv1.Approval{Id: "apr_2", Challenge: a.Challenge, PayloadHash: f.hash}
	if err := VerifyFor(ctx, d, a2, f.lookup, f.now); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	a3 := &nexusv1.Approval{Id: "apr_1", Challenge: []byte("other"), PayloadHash: f.hash}
	if err := VerifyFor(ctx, d, a3, f.lookup, f.now); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
	a4 := &nexusv1.Approval{Id: "apr_1", Challenge: a.Challenge, PayloadHash: f.hash, ExpiresAt: timestamppb.New(f.now.Add(-time.Second))}
	if err := VerifyFor(ctx, d, a4, f.lookup, f.now); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}
