import CryptoKit
import Foundation

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  WIRE FORMATS — MUST MATCH backend/internal/security                                     ║
// ║                                                                                          ║
// ║  Every byte-level convention shared with the Hub/Node lives in this file (plus the       ║
// ║  approval message layout in ApprovalSigner.swift and the envelope constants in           ║
// ║  EnvelopeCrypto.swift). If the backend changes a format, change it HERE and nowhere      ║
// ║  else. Each constant documents the exact Go-side expectation it assumes.                 ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum WireFormat {

    // MARK: Public keys

    /// P-256 public keys (device auth key, approval key) are sent as **uncompressed SEC1 / X9.63
    /// points: 0x04 || X(32) || Y(32) — 65 bytes**. This is what Go's
    /// `ecdh.P256().NewPublicKey`, `elliptic.Unmarshal` and `ecdsa.ParseUncompressedPublicKey`
    /// accept. (CryptoKit's `rawRepresentation` would be 64 bytes without the 0x04 prefix.)
    public static func encodeP256PublicKey(_ key: P256.Signing.PublicKey) -> Data {
        key.x963Representation
    }

    /// X25519 public keys are the **32-byte raw** representation (RFC 7748), same as Go's
    /// `ecdh.X25519().NewPublicKey(b)`.
    public static func encodeX25519PublicKey(_ key: Curve25519.KeyAgreement.PublicKey) -> Data {
        key.rawRepresentation
    }

    // MARK: Signatures

    /// ECDSA P-256 signatures are **ASN.1 DER** encoded (Go: `ecdsa.VerifyASN1`).
    /// CryptoKit hashes the message with SHA-256 internally, so the signed digest is
    /// SHA256(message) — the backend must verify with `ecdsa.VerifyASN1(pub, sha256(msg), sig)`.
    public static func encodeSignature(_ signature: P256.Signing.ECDSASignature) -> Data {
        signature.derRepresentation
    }

    // MARK: Pairing

    /// HKDF-SHA256 `info` for deriving the pairing-proof MAC key from the QR secret.
    public static let pairingHKDFInfo = Data("nexus/v1/pairing".utf8)
    /// HKDF salt: **empty** (Go: `hkdf.New(sha256.New, secret, nil, info)`).
    public static let pairingHKDFSalt = Data()
    /// Derived MAC key length in bytes.
    public static let pairingKeyLength = 32

    /// The HKDF input keying material is the **UTF-8 bytes of the `secret` query parameter
    /// exactly as it appears in the QR URL after percent-decoding** (no base64/hex decoding).
    public static func pairingSecretBytes(_ secret: String) -> Data {
        Data(secret.utf8)
    }

    /// Pairing proof = HMAC-SHA256(key, auth_pub || approval_pub || encryption_pub) where the
    /// keys use the encodings above (65 + 65 + 32 bytes, no length prefixes).
    public static func pairingProofMessage(authPublicKey: Data, approvalPublicKey: Data, encryptionPublicKey: Data) -> Data {
        var message = Data()
        message.append(authPublicKey)
        message.append(approvalPublicKey)
        message.append(encryptionPublicKey)
        return message
    }

    // MARK: Session authentication

    /// `DeviceService.Authenticate`: the device signs the **raw challenge bytes** returned by
    /// `GetChallenge` with the device auth key (ECDSA P-256, SHA-256, DER).
    public static func authenticationMessage(challenge: Data) -> Data {
        challenge
    }

    // MARK: Helpers

    public static func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }

    public static func appendBigEndian(_ value: Int64, to data: inout Data) {
        var bigEndian = value.bigEndian
        withUnsafeBytes(of: &bigEndian) { data.append(contentsOf: $0) }
    }
}
