// Package approval signs and verifies device-signed approval decisions
// (nexusv1.SignedApproval) and computes canonical action hashes
// (SECURITY.md §5 "what you see is what you sign"; Layer 5 edge verification).
//
// # Signed message (clients MUST match byte for byte)
//
//	M = ASCII "nexus/v1/approval" || 0x00
//	    || u32be(len(approval_id)) || approval_id      (UTF-8)
//	    || u8(approve ? 0x01 : 0x00)
//	    || u32be(len(challenge))   || challenge
//	    || action_sha256                               (exactly 32 bytes)
//	    || i64be(expires_at.seconds)                   (Unix seconds; nanos are
//	                                                    ignored and not signed)
//	signature = ECDSA P-256 / SHA-256 over M, i.e. over digest SHA256(M),
//	            with the device *approval* key (Secure Enclave, biometry-bound).
//	            DER preferred; raw 64-byte r||s accepted.
//
// u32be/i64be are big-endian 4/8-byte integers. Swift:
//
//	var m = Data("nexus/v1/approval".utf8); m.append(0)
//	m.append(u32be(id.utf8.count)); m.append(Data(id.utf8))
//	m.append(approve ? 1 : 0)
//	m.append(u32be(challenge.count)); m.append(challenge)
//	m.append(actionSHA256)                      // 32 bytes
//	m.append(i64be(Int64(expiresAt.timeIntervalSince1970)))
//	let sig = try approvalKey.signature(for: m).derRepresentation
//
// # Action hash
//
// action_sha256 = SHA256(RFC 8785 JCS canonical JSON of the action preview);
// see CanonicalJSON / ActionHash. For an Approval, the hash input is
// Approval.payload (google.protobuf.Struct) as JSON.
package approval

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// Tag is the domain-separation prefix of the signed message.
const Tag = "nexus/v1/approval"

// MaxLifetime bounds how far in the future a signed expiry may be.
var MaxLifetime = 24 * time.Hour

// Errors. Verify wraps one of these.
var (
	ErrMalformed     = errors.New("approval: malformed decision")
	ErrHashMismatch  = errors.New("approval: action hash does not match the action to execute")
	ErrExpired       = errors.New("approval: expired")
	ErrTooLong       = errors.New("approval: expiry too far in the future")
	ErrBadSignature  = errors.New("approval: bad signature")
	ErrRevoked       = errors.New("approval: device key revoked")
	ErrUnknownDevice = errors.New("approval: unknown device")
	ErrMismatch      = errors.New("approval: decision does not belong to this approval")
)

// KeyLookup resolves a device id to its approval public key. Implementations
// return an error wrapping ErrRevoked or ErrUnknownDevice when appropriate;
// any error rejects the decision.
type KeyLookup interface {
	ApprovalKey(ctx context.Context, deviceID string) (*ecdsa.PublicKey, error)
}

// KeyLookupFunc adapts a function to KeyLookup.
type KeyLookupFunc func(ctx context.Context, deviceID string) (*ecdsa.PublicKey, error)

// ApprovalKey implements KeyLookup.
func (f KeyLookupFunc) ApprovalKey(ctx context.Context, id string) (*ecdsa.PublicKey, error) {
	return f(ctx, id)
}

// Message returns the exact bytes that are signed (see package doc).
func Message(d *nexusv1.SignedApproval) []byte {
	b := keys.NewBuilder(Tag).String(d.GetApprovalId())
	if d.GetApprove() {
		b.U8(1)
	} else {
		b.U8(0)
	}
	b.Bytes(d.GetChallenge()).Fixed(d.GetActionSha256())
	var secs int64
	if d.GetExpiresAt() != nil {
		secs = d.GetExpiresAt().GetSeconds()
	}
	return b.I64(secs).Message()
}

// Sign fills d.Signature using signer (tests, CLI, web/desktop clients).
func Sign(d *nexusv1.SignedApproval, signer crypto.Signer) error {
	if err := wellFormed(d); err != nil {
		return err
	}
	sig, err := keys.Sign(signer, Message(d))
	if err != nil {
		return err
	}
	d.Signature = sig
	return nil
}

func wellFormed(d *nexusv1.SignedApproval) error {
	switch {
	case d == nil:
		return ErrMalformed
	case d.ApprovalId == "", d.DeviceId == "":
		return fmt.Errorf("%w: approval_id and device_id required", ErrMalformed)
	case len(d.Challenge) == 0:
		return fmt.Errorf("%w: challenge required", ErrMalformed)
	case len(d.ActionSha256) != 32:
		return fmt.Errorf("%w: action_sha256 must be 32 bytes", ErrMalformed)
	case d.ExpiresAt == nil:
		return fmt.Errorf("%w: expires_at required", ErrMalformed)
	}
	return nil
}

// Verify checks a decision for the action whose hash is expectedActionHash:
// well-formedness, hash equality, expiry (now < expires_at <= now+MaxLifetime),
// device key (via lookup: unknown/revoked rejected) and signature.
//
// A valid *denial* also verifies; callers must check d.Approve.
func Verify(ctx context.Context, d *nexusv1.SignedApproval, expectedActionHash []byte, lookup KeyLookup, now time.Time) error {
	if err := wellFormed(d); err != nil {
		return err
	}
	if len(expectedActionHash) != 32 || subtle.ConstantTimeCompare(d.ActionSha256, expectedActionHash) != 1 {
		return ErrHashMismatch
	}
	exp := time.Unix(d.ExpiresAt.GetSeconds(), 0)
	if !now.Before(exp) {
		return ErrExpired
	}
	if exp.Sub(now) > MaxLifetime {
		return ErrTooLong
	}
	if lookup == nil {
		return errors.New("approval: no key lookup")
	}
	pub, err := lookup.ApprovalKey(ctx, d.DeviceId)
	if err != nil {
		return fmt.Errorf("approval: device %q: %w", d.DeviceId, err)
	}
	if pub == nil {
		return ErrUnknownDevice
	}
	if !keys.Verify(pub, Message(d), d.Signature) {
		return ErrBadSignature
	}
	return nil
}

// VerifyFor verifies a decision against a pending Approval: approval id,
// challenge and payload_hash must match, the approval itself must not have
// expired, then Verify runs with a.PayloadHash.
func VerifyFor(ctx context.Context, d *nexusv1.SignedApproval, a *nexusv1.Approval, lookup KeyLookup, now time.Time) error {
	if d == nil || a == nil {
		return ErrMalformed
	}
	if d.ApprovalId != a.Id {
		return fmt.Errorf("%w: approval id", ErrMismatch)
	}
	if len(a.Challenge) == 0 || subtle.ConstantTimeCompare(d.Challenge, a.Challenge) != 1 {
		return fmt.Errorf("%w: challenge", ErrMismatch)
	}
	if a.ExpiresAt != nil && !now.Before(a.ExpiresAt.AsTime()) {
		return ErrExpired
	}
	return Verify(ctx, d, a.PayloadHash, lookup, now)
}
