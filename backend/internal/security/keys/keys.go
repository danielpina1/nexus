// Package keys holds the shared key and signature encodings used by every
// Nexus security component (envelopes, approvals, owner statements, request
// signatures, audit checkpoints, pairing).
//
// # Wire encodings (clients MUST match)
//
//   - P-256 public keys are exchanged as the 65-byte uncompressed X9.63 point
//     (0x04 || X || Y) — CryptoKit `x963Representation`. Parsers also accept
//     the 64-byte raw form (X || Y, CryptoKit `rawRepresentation`), the
//     33-byte compressed form and PKIX/SPKI DER, but Nexus always emits the
//     65-byte form.
//   - X25519 public keys are the 32-byte raw form (CryptoKit
//     `Curve25519.KeyAgreement.PublicKey.rawRepresentation`).
//   - ECDSA signatures are ECDSA P-256 with SHA-256 over a message M:
//     the signed digest is SHA256(M). In CryptoKit that is
//     `key.signature(for: M)` (or `signature(for: SHA256.hash(data: M))`).
//     Nexus emits ASN.1 DER signatures; verifiers accept both DER and the
//     64-byte raw r||s form (CryptoKit `rawRepresentation`).
//   - Key ids are lowercase hex of the first 16 bytes of SHA256(key bytes),
//     where key bytes are the 65-byte X9.63 form for P-256 keys and the
//     32-byte raw form for X25519 keys (32 hex chars).
package keys

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// Errors.
var (
	ErrInvalidKey       = errors.New("keys: invalid P-256 public key")
	ErrInvalidSignature = errors.New("keys: invalid signature")
)

// Generate creates a new P-256 key.
func Generate() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// ParsePublicKey parses a P-256 public key in any accepted encoding
// (65-byte uncompressed, 64-byte raw, 33-byte compressed, PKIX DER).
func ParsePublicKey(b []byte) (*ecdsa.PublicKey, error) {
	switch {
	case len(b) == 65 && b[0] == 0x04:
		return fromUncompressed(b)
	case len(b) == 64:
		u := make([]byte, 65)
		u[0] = 0x04
		copy(u[1:], b)
		return fromUncompressed(u)
	case len(b) == 33 && (b[0] == 0x02 || b[0] == 0x03):
		x, y := elliptic.UnmarshalCompressed(elliptic.P256(), b)
		if x == nil {
			return nil, ErrInvalidKey
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	default:
		k, err := x509.ParsePKIXPublicKey(b)
		if err != nil {
			return nil, ErrInvalidKey
		}
		pk, ok := k.(*ecdsa.PublicKey)
		if !ok || pk.Curve != elliptic.P256() {
			return nil, ErrInvalidKey
		}
		return pk, nil
	}
}

func fromUncompressed(u []byte) (*ecdsa.PublicKey, error) {
	// crypto/ecdh validates that the point is on the curve and not infinity.
	if _, err := ecdh.P256().NewPublicKey(u); err != nil {
		return nil, ErrInvalidKey
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(u[1:33]),
		Y:     new(big.Int).SetBytes(u[33:65]),
	}, nil
}

// MarshalPublicKey returns the 65-byte uncompressed X9.63 encoding.
func MarshalPublicKey(pub *ecdsa.PublicKey) []byte {
	out := make([]byte, 65)
	out[0] = 0x04
	pub.X.FillBytes(out[1:33])
	pub.Y.FillBytes(out[33:65])
	return out
}

// CanonicalPublicKey re-encodes any accepted P-256 encoding to 65 bytes.
func CanonicalPublicKey(b []byte) ([]byte, error) {
	pk, err := ParsePublicKey(b)
	if err != nil {
		return nil, err
	}
	return MarshalPublicKey(pk), nil
}

// KeyID returns the key id of a P-256 public key (see package doc).
func KeyID(pub *ecdsa.PublicKey) string { return KeyIDBytes(MarshalPublicKey(pub)) }

// KeyIDBytes returns the key id of already-encoded key bytes (65-byte P-256
// or 32-byte X25519).
func KeyIDBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// Sign signs SHA256(msg) with signer (an *ecdsa.PrivateKey or a hardware
// crypto.Signer) and returns an ASN.1 DER signature.
func Sign(signer crypto.Signer, msg []byte) ([]byte, error) {
	d := sha256.Sum256(msg)
	return signer.Sign(rand.Reader, d[:], crypto.SHA256)
}

// Verify checks a DER or 64-byte raw signature over SHA256(msg).
func Verify(pub *ecdsa.PublicKey, msg, sig []byte) bool {
	d := sha256.Sum256(msg)
	return VerifyDigest(pub, d[:], sig)
}

// VerifyDigest checks a DER or 64-byte raw signature over a SHA-256 digest.
func VerifyDigest(pub *ecdsa.PublicKey, digest, sig []byte) bool {
	if pub == nil || len(sig) == 0 {
		return false
	}
	if len(sig) == 64 {
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if ecdsa.Verify(pub, digest, r, s) {
			return true
		}
	}
	return ecdsa.VerifyASN1(pub, digest, sig)
}

// --- byte-layout helpers shared by the signed structures ---

// Builder accumulates a signed message. Every variable-length field is
// written as u32be(len) || bytes; fixed-width integers are big-endian.
type Builder struct{ b []byte }

// NewBuilder starts a message with a domain-separation tag written as the
// ASCII tag followed by a single 0x00 byte.
func NewBuilder(tag string) *Builder {
	b := &Builder{}
	b.b = append(b.b, tag...)
	b.b = append(b.b, 0)
	return b
}

// Bytes appends u32be(len(p)) || p.
func (b *Builder) Bytes(p []byte) *Builder {
	b.b = binary.BigEndian.AppendUint32(b.b, uint32(len(p)))
	b.b = append(b.b, p...)
	return b
}

// String appends u32be(len(s)) || UTF-8(s).
func (b *Builder) String(s string) *Builder { return b.Bytes([]byte(s)) }

// Fixed appends p without a length prefix.
func (b *Builder) Fixed(p []byte) *Builder { b.b = append(b.b, p...); return b }

// U8 appends one byte.
func (b *Builder) U8(v byte) *Builder { b.b = append(b.b, v); return b }

// U32 appends a big-endian uint32.
func (b *Builder) U32(v uint32) *Builder { b.b = binary.BigEndian.AppendUint32(b.b, v); return b }

// U64 appends a big-endian uint64.
func (b *Builder) U64(v uint64) *Builder { b.b = binary.BigEndian.AppendUint64(b.b, v); return b }

// I64 appends a big-endian two's-complement int64.
func (b *Builder) I64(v int64) *Builder { return b.U64(uint64(v)) }

// Message returns the accumulated bytes.
func (b *Builder) Message() []byte { return b.b }

// --- PEM persistence ---

// LoadOrCreatePrivateKey loads a PEM "EC PRIVATE KEY"/"PRIVATE KEY" from
// path, or generates a P-256 key and writes it with mode 0600.
func LoadOrCreatePrivateKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return ParsePrivateKeyPEM(b)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	pemBytes, err := MarshalPrivateKeyPEM(k)
	if err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// MarshalPrivateKeyPEM encodes a key as PKCS#8 PEM.
func MarshalPrivateKeyPEM(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParsePrivateKeyPEM parses a PKCS#8 or SEC1 P-256 private key.
func ParsePrivateKeyPEM(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("keys: no PEM block")
	}
	switch blk.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(blk.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, err
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok || ek.Curve != elliptic.P256() {
			return nil, errors.New("keys: not a P-256 key")
		}
		return ek, nil
	default:
		return nil, fmt.Errorf("keys: unexpected PEM type %q", blk.Type)
	}
}

// WriteFileAtomic writes data to a temp file in the same directory and
// renames it over path. The directory is created with 0700.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
