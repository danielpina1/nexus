// Package envelope seals and opens nexusv1.SealedEnvelope (SECURITY.md
// Layer 4): end-to-end encrypted, signed phone <-> node payloads that the
// Hub routes but cannot read or forge.
//
// # Exact construction (clients MUST match)
//
//	header_bytes = protobuf serialization of EnvelopeHeader (sent verbatim in
//	               SealedEnvelope.header; receivers never re-serialize it)
//	suite        = "hpke-x25519-sha256-chacha20poly1305+p256-ecdsa"
//	HPKE         = RFC 9180 base mode, KEM DHKEM(X25519, HKDF-SHA256) 0x0020,
//	               KDF HKDF-SHA256 0x0001, AEAD ChaCha20-Poly1305 0x0003
//	info         = ASCII "nexus/v1/envelope" (17 bytes)
//	enc, ct      = HPKE single-shot Seal(pkR = recipient X25519 key, info,
//	               aad = header_bytes, pt = plaintext)
//	signature    = ECDSA P-256 / SHA-256 over M = header_bytes || enc || ct
//	               (i.e. the signed digest is SHA256(header_bytes || enc || ct));
//	               DER (raw 64-byte r||s also accepted)
//
// CryptoKit: `HPKE.Sender(recipientKey:, ciphersuite: .Curve25519_SHA256_ChachaPoly,
// info: Data("nexus/v1/envelope".utf8))`, `sender.seal(pt, authenticating: header)`,
// enc = `sender.encapsulatedKey`; sign with `key.signature(for: header+enc+ct)`.
//
// Header rules enforced by Open: purpose matches, recipient_key_id matches
// (when expected), nonce >= 16 bytes, created_at within ±60 s, nonce unused,
// counter strictly increasing per sender_key_id (counter must be >= 1).
// Key ids: see package keys (hex of first 16 bytes of SHA256(key)).
package envelope

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
	"github.com/danielpina1/nexus/backend/internal/security/replay"
)

// Constants of the construction.
const (
	Suite = "hpke-x25519-sha256-chacha20poly1305+p256-ecdsa"
	Info  = "nexus/v1/envelope"
)

var hpkeSuite = hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)

// Errors.
var (
	ErrSuite     = errors.New("envelope: unsupported suite")
	ErrMalformed = errors.New("envelope: malformed")
	ErrSignature = errors.New("envelope: bad signature")
	ErrPurpose   = errors.New("envelope: unexpected purpose")
	ErrRecipient = errors.New("envelope: not addressed to this key")
	ErrDecrypt   = errors.New("envelope: decryption failed")
)

// X25519 key helpers (32-byte raw keys, CryptoKit rawRepresentation).

// GenerateX25519 returns a new (public, private) X25519 key pair.
func GenerateX25519() (pub, priv []byte, err error) {
	pk, sk, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	pub, _ = pk.MarshalBinary()
	priv, _ = sk.MarshalBinary()
	return pub, priv, nil
}

// X25519Public derives the public key of a 32-byte private key.
func X25519Public(priv []byte) ([]byte, error) {
	sk, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return sk.Public().MarshalBinary()
}

// NewHeader fills a header with a fresh 16-byte nonce and the current time.
func NewHeader(senderKeyID, recipientKeyID string, purpose nexusv1.EnvelopePurpose, counter uint64, correlationID string) (*nexusv1.EnvelopeHeader, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &nexusv1.EnvelopeHeader{
		SenderKeyId:    senderKeyID,
		RecipientKeyId: recipientKeyID,
		Purpose:        purpose,
		CreatedAt:      timestamppb.Now(),
		Nonce:          nonce,
		Counter:        counter,
		CorrelationId:  correlationID,
	}, nil
}

// Seal encrypts plaintext to recipientPub (32-byte X25519) and signs with
// signer (P-256). Missing nonce / created_at are filled in.
func Seal(hdr *nexusv1.EnvelopeHeader, plaintext, recipientPub []byte, signer crypto.Signer) (*nexusv1.SealedEnvelope, error) {
	if hdr == nil || signer == nil {
		return nil, ErrMalformed
	}
	hdr = proto.Clone(hdr).(*nexusv1.EnvelopeHeader)
	if len(hdr.Nonce) == 0 {
		hdr.Nonce = make([]byte, 16)
		if _, err := rand.Read(hdr.Nonce); err != nil {
			return nil, err
		}
	}
	if hdr.CreatedAt == nil {
		hdr.CreatedAt = timestamppb.Now()
	}
	if hdr.RecipientKeyId == "" {
		hdr.RecipientKeyId = keys.KeyIDBytes(recipientPub)
	}
	if hdr.SenderKeyId == "" {
		if pk, ok := signer.Public().(*ecdsa.PublicKey); ok {
			hdr.SenderKeyId = keys.KeyID(pk)
		}
	}
	headerBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	pkR, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("envelope: recipient key: %w", err)
	}
	sender, err := hpkeSuite.NewSender(pkR, []byte(Info))
	if err != nil {
		return nil, err
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, err
	}
	ct, err := sealer.Seal(plaintext, headerBytes)
	if err != nil {
		return nil, err
	}
	sig, err := keys.Sign(signer, signedMessage(headerBytes, enc, ct))
	if err != nil {
		return nil, err
	}
	return &nexusv1.SealedEnvelope{
		Header:          headerBytes,
		EncapsulatedKey: enc,
		Ciphertext:      ct,
		Signature:       sig,
		Suite:           Suite,
	}, nil
}

func signedMessage(header, enc, ct []byte) []byte {
	m := make([]byte, 0, len(header)+len(enc)+len(ct))
	m = append(m, header...)
	m = append(m, enc...)
	return append(m, ct...)
}

// ParseHeader decodes the (not yet authenticated) header, e.g. for routing.
func ParseHeader(env *nexusv1.SealedEnvelope) (*nexusv1.EnvelopeHeader, error) {
	if env == nil || len(env.Header) == 0 {
		return nil, ErrMalformed
	}
	h := &nexusv1.EnvelopeHeader{}
	if err := proto.Unmarshal(env.Header, h); err != nil {
		return nil, fmt.Errorf("%w: header: %v", ErrMalformed, err)
	}
	return h, nil
}

// SenderLookup resolves a sender key id to its P-256 signing key. It must
// return an error for unknown or revoked keys.
type SenderLookup func(senderKeyID string) (*ecdsa.PublicKey, error)

// OpenOptions configure Open.
type OpenOptions struct {
	// RecipientPrivateKey is the 32-byte X25519 private key.
	RecipientPrivateKey []byte
	// RecipientKeyID, when set, must equal header.recipient_key_id.
	// Defaults to the id of the recipient's public key.
	RecipientKeyID string
	// Purpose, when not UNSPECIFIED, must equal header.purpose.
	Purpose nexusv1.EnvelopePurpose
	// Sender resolves header.sender_key_id. Required.
	Sender SenderLookup
	// Replay, when set, enforces window/nonce/counter (strongly recommended).
	Replay *replay.Guard
}

// Open authenticates and decrypts env. Order: suite, header parse, purpose,
// recipient, sender signature, decryption, replay check (recorded only for
// fully valid envelopes).
func Open(env *nexusv1.SealedEnvelope, o OpenOptions) (*nexusv1.EnvelopeHeader, []byte, error) {
	if env == nil {
		return nil, nil, ErrMalformed
	}
	if env.Suite != Suite {
		return nil, nil, ErrSuite
	}
	if len(env.EncapsulatedKey) != 32 || len(env.Ciphertext) == 0 || len(env.Signature) == 0 {
		return nil, nil, ErrMalformed
	}
	if o.Sender == nil {
		return nil, nil, errors.New("envelope: OpenOptions.Sender required")
	}
	hdr, err := ParseHeader(env)
	if err != nil {
		return nil, nil, err
	}
	if o.Purpose != nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_UNSPECIFIED && hdr.Purpose != o.Purpose {
		return nil, nil, ErrPurpose
	}
	sk, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPrivateKey(o.RecipientPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: recipient private key: %w", err)
	}
	wantRecipient := o.RecipientKeyID
	if wantRecipient == "" {
		pub, _ := sk.Public().MarshalBinary()
		wantRecipient = keys.KeyIDBytes(pub)
	}
	if hdr.RecipientKeyId != wantRecipient {
		return nil, nil, ErrRecipient
	}
	senderPub, err := o.Sender(hdr.SenderKeyId)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: sender %q: %w", hdr.SenderKeyId, err)
	}
	if !keys.Verify(senderPub, signedMessage(env.Header, env.EncapsulatedKey, env.Ciphertext), env.Signature) {
		return nil, nil, ErrSignature
	}
	pt, err := decrypt(sk, env)
	if err != nil {
		return nil, nil, err
	}
	if o.Replay != nil {
		if hdr.CreatedAt == nil {
			return nil, nil, ErrMalformed
		}
		if hdr.Counter == 0 {
			return nil, nil, replay.ErrCounterUnset
		}
		if err := o.Replay.Check(hdr.SenderKeyId, hdr.Nonce, hdr.Counter, hdr.CreatedAt.AsTime()); err != nil {
			return nil, nil, err
		}
	}
	return hdr, pt, nil
}

func decrypt(sk kem.PrivateKey, env *nexusv1.SealedEnvelope) ([]byte, error) {
	recv, err := hpkeSuite.NewReceiver(sk, []byte(Info))
	if err != nil {
		return nil, err
	}
	opener, err := recv.Setup(env.EncapsulatedKey)
	if err != nil {
		return nil, ErrDecrypt
	}
	pt, err := opener.Open(env.Ciphertext, env.Header)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
