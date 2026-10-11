package envelope

import (
	"crypto/ecdsa"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
)

type fixture struct {
	signer   *ecdsa.PrivateKey
	recvPub  []byte
	recvPriv []byte
	guard    *replay.Guard
}

func setup(t *testing.T) *fixture {
	t.Helper()
	s, _ := keys.Generate()
	pub, priv, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{signer: s, recvPub: pub, recvPriv: priv, guard: replay.New(time.Minute)}
}

func (f *fixture) opts() OpenOptions {
	return OpenOptions{
		RecipientPrivateKey: f.recvPriv,
		Purpose:             nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_COMMAND,
		Sender: func(id string) (*ecdsa.PublicKey, error) {
			if id == keys.KeyID(&f.signer.PublicKey) {
				return &f.signer.PublicKey, nil
			}
			return nil, errors.New("unknown sender")
		},
		Replay: f.guard,
	}
}

func (f *fixture) seal(t *testing.T, counter uint64, pt string) *nexusv1.SealedEnvelope {
	t.Helper()
	h, err := NewHeader("", "", nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_COMMAND, counter, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	env, err := Seal(h, []byte(pt), f.recvPub, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestRoundTrip(t *testing.T) {
	f := setup(t)
	env := f.seal(t, 1, "ls -la")
	hdr, pt, err := Open(env, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "ls -la" || hdr.CorrelationId != "run_1" || hdr.Counter != 1 {
		t.Fatalf("got %q %+v", pt, hdr)
	}
	if hdr.SenderKeyId != keys.KeyID(&f.signer.PublicKey) || hdr.RecipientKeyId != keys.KeyIDBytes(f.recvPub) {
		t.Fatal("key ids not filled")
	}
	// X25519Public derives the same public key.
	if p, _ := X25519Public(f.recvPriv); string(p) != string(f.recvPub) {
		t.Fatal("X25519Public mismatch")
	}
}

func TestTamperDetection(t *testing.T) {
	f := setup(t)
	mutate := map[string]func(e *nexusv1.SealedEnvelope){
		"ciphertext": func(e *nexusv1.SealedEnvelope) { e.Ciphertext[0] ^= 1 },
		"enc":        func(e *nexusv1.SealedEnvelope) { e.EncapsulatedKey[0] ^= 1 },
		"signature":  func(e *nexusv1.SealedEnvelope) { e.Signature[len(e.Signature)-1] ^= 1 },
		"header": func(e *nexusv1.SealedEnvelope) {
			h, _ := ParseHeader(e)
			h.CorrelationId = "run_2"
			e.Header, _ = proto.Marshal(h)
		},
		"suite": func(e *nexusv1.SealedEnvelope) { e.Suite = "other" },
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			env := f.seal(t, 1, "secret")
			m(env)
			if _, _, err := Open(env, f.opts()); err == nil {
				t.Fatal("tampered envelope opened")
			}
		})
	}
}

func TestResignedByAttackerRejected(t *testing.T) {
	f := setup(t)
	env := f.seal(t, 1, "x")
	attacker, _ := keys.Generate()
	sig, _ := keys.Sign(attacker, signedMessage(env.Header, env.EncapsulatedKey, env.Ciphertext))
	env.Signature = sig
	if _, _, err := Open(env, f.opts()); !errors.Is(err, ErrSignature) {
		t.Fatal(err)
	}
}

func TestReplayAndCounter(t *testing.T) {
	f := setup(t)
	env := f.seal(t, 1, "x")
	if _, _, err := Open(env, f.opts()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(env, f.opts()); !errors.Is(err, replay.ErrNonceReused) {
		t.Fatalf("replay: %v", err)
	}
	old := f.seal(t, 1, "y") // fresh nonce, stale counter
	if _, _, err := Open(old, f.opts()); !errors.Is(err, replay.ErrCounter) {
		t.Fatalf("counter: %v", err)
	}
	zero := f.seal(t, 0, "z")
	if _, _, err := Open(zero, f.opts()); !errors.Is(err, replay.ErrCounterUnset) {
		t.Fatalf("zero counter: %v", err)
	}
}

func TestStaleTimestamp(t *testing.T) {
	f := setup(t)
	h, _ := NewHeader("", "", nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_COMMAND, 1, "")
	h.CreatedAt = timestamppb.New(time.Now().Add(-2 * time.Minute))
	env, _ := Seal(h, []byte("x"), f.recvPub, f.signer)
	if _, _, err := Open(env, f.opts()); !errors.Is(err, replay.ErrStale) {
		t.Fatal(err)
	}
}

func TestWrongPurposeRecipientSender(t *testing.T) {
	f := setup(t)
	env := f.seal(t, 1, "x")

	o := f.opts()
	o.Purpose = nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_SECRET
	if _, _, err := Open(env, o); !errors.Is(err, ErrPurpose) {
		t.Fatal(err)
	}

	_, otherPriv, _ := GenerateX25519()
	o = f.opts()
	o.RecipientPrivateKey = otherPriv
	if _, _, err := Open(env, o); !errors.Is(err, ErrRecipient) {
		t.Fatal(err)
	}
	// Even if the recipient id check is bypassed, decryption fails.
	o.RecipientKeyID = keys.KeyIDBytes(f.recvPub)
	if _, _, err := Open(env, o); !errors.Is(err, ErrDecrypt) {
		t.Fatal(err)
	}

	o = f.opts()
	o.Sender = func(string) (*ecdsa.PublicKey, error) { return nil, errors.New("revoked") }
	if _, _, err := Open(env, o); err == nil {
		t.Fatal("revoked sender accepted")
	}
}
