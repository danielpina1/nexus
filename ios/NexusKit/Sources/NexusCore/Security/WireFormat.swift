import CryptoKit
import Foundation

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  WIRE FORMATS — MUST MATCH backend/internal/security  (docs/WIRE_FORMATS.md)             ║
// ║                                                                                          ║
// ║  docs/WIRE_FORMATS.md is the source of truth. Every byte-level convention shared with    ║
// ║  the Hub/Node lives in this file, plus:                                                  ║
// ║    • ApprovalSigner.swift  — SignedApproval message + key choice                         ║
// ║    • CanonicalJSON.swift   — RFC 8785 JCS action hash                                    ║
// ║    • EnvelopeCrypto.swift  — sealed envelope / notification payload                     ║
// ║    • PairingService.swift  — QR payload fields                                           ║
// ║  If the backend changes a format, change it in these files and nowhere else.             ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum WireFormat {

    // MARK: Builder primitives  (Go: keys.NewBuilder)

    /// `TAG || 0x00` — ASCII domain tag followed by one zero byte.
    public static func tagged(_ tag: String) -> Data {
        var data = Data(tag.utf8)
        data.append(0)
        return data
    }

    /// `lp(x) = u32be(len(x)) || x`.
    public static func appendLengthPrefixed(_ bytes: Data, to data: inout Data) {
        appendBigEndian(UInt32(bytes.count), to: &data)
        data.append(bytes)
    }

    public static func appendLengthPrefixed(_ string: String, to data: inout Data) {
        appendLengthPrefixed(Data(string.utf8), to: &data)
    }

    public static func appendBigEndian<T: FixedWidthInteger>(_ value: T, to data: inout Data) {
        var bigEndian = value.bigEndian
        withUnsafeBytes(of: &bigEndian) { data.append(contentsOf: $0) }
    }

    // MARK: Public keys

    /// P-256 public keys: **65-byte X9.63 uncompressed** (0x04 || X || Y).
    public static func encodeP256PublicKey(_ key: P256.Signing.PublicKey) -> Data {
        key.x963Representation
    }

    /// X25519 public keys: **32 raw bytes**.
    public static func encodeX25519PublicKey(_ key: Curve25519.KeyAgreement.PublicKey) -> Data {
        key.rawRepresentation
    }

    /// Key id = lowercase hex of the first 16 bytes of SHA256(public key bytes as encoded above).
    public static func keyID(_ publicKeyBytes: Data) -> String {
        hex(Data(SHA256.hash(data: publicKeyBytes).prefix(16)))
    }

    // MARK: Signatures

    /// "Signature over M" = ECDSA P-256 / SHA-256 over M (CryptoKit hashes M itself).
    /// We send **ASN.1 DER**; the backend accepts DER or raw 64-byte r||s.
    public static func encodeSignature(_ signature: P256.Signing.ECDSASignature) -> Data {
        signature.derRepresentation
    }

    // MARK: Authenticate (device auth key)

    public static let authenticateTag = "nexus/v1/authenticate"

    /// `"nexus/v1/authenticate" 0x00 || lp(device_id) || lp(challenge)`
    public static func authenticationMessage(deviceID: String, challenge: Data) -> Data {
        var message = tagged(authenticateTag)
        appendLengthPrefixed(deviceID, to: &message)
        appendLengthPrefixed(challenge, to: &message)
        return message
    }

    // MARK: Signed requests (SECURITY.md Layer 3; device auth key)

    public enum RequestHeader {
        public static let device = "X-Nexus-Device"
        public static let timestamp = "X-Nexus-Timestamp"
        public static let nonce = "X-Nexus-Nonce"
        public static let counter = "X-Nexus-Counter"
        public static let signature = "X-Nexus-Signature"
    }

    /// Signed string: `METHOD\nTARGET\nhex(sha256(body))\nTIMESTAMP\nNONCE\nDEVICE_ID\nCOUNTER`
    /// (`COUNTER` = "" when the header is absent; `TARGET` = path + "?query").
    public static func requestSigningString(
        method: String,
        target: String,
        body: Data,
        timestamp: Int64,
        nonce: String,
        deviceID: String,
        counter: UInt64?
    ) -> String {
        [
            method.uppercased(),
            target,
            hex(Data(SHA256.hash(data: body))),
            String(timestamp),
            nonce,
            deviceID,
            counter.map(String.init) ?? "",
        ].joined(separator: "\n")
    }

    /// Builds the Layer-3 headers for one request. (Not yet wired into the connect-swift
    /// transport — the Hub only enforces it with `require_signed_requests`. TODO: interceptor.)
    public static func signedRequestHeaders(
        method: String,
        target: String,
        body: Data,
        deviceID: String,
        counter: UInt64?,
        key: SigningKey,
        now: Date = Date()
    ) throws -> [String: String] {
        var nonceBytes = [UInt8](repeating: 0, count: 16)
        for i in nonceBytes.indices { nonceBytes[i] = UInt8.random(in: 0...255) }
        let nonce = base64URL(Data(nonceBytes))
        let timestamp = Int64(now.timeIntervalSince1970)
        let string = requestSigningString(
            method: method, target: target, body: body, timestamp: timestamp,
            nonce: nonce, deviceID: deviceID, counter: counter
        )
        let signature = try key.signature(for: Data(string.utf8))
        var headers = [
            RequestHeader.device: deviceID,
            RequestHeader.timestamp: String(timestamp),
            RequestHeader.nonce: nonce,
            RequestHeader.signature: encodeSignature(signature).base64EncodedString(),
        ]
        if let counter { headers[RequestHeader.counter] = String(counter) }
        return headers
    }

    // MARK: Pairing

    /// HKDF-SHA256 info for the pairing-proof MAC key.
    public static let pairingHKDFInfo = Data("nexus/v1/pairing".utf8)
    /// HKDF salt: empty.
    public static let pairingHKDFSalt = Data()
    public static let pairingKeyLength = 32
    /// Raw pairing secret length; the QR `s` token is base64url (no padding) of these bytes.
    public static let pairingSecretLength = 32

    /// Decodes the QR `s` token into the 32-byte secret (HKDF input keying material).
    public static func pairingSecret(fromToken token: String) -> Data? {
        guard let data = base64URLDecode(token), data.count == pairingSecretLength else { return nil }
        return data
    }

    /// `proof = HMAC-SHA256(key, auth_pub || approval_pub || encryption_pub)` (bytes as sent).
    public static func pairingProofMessage(authPublicKey: Data, approvalPublicKey: Data, encryptionPublicKey: Data) -> Data {
        var message = Data()
        message.append(authPublicKey)
        message.append(approvalPublicKey)
        message.append(encryptionPublicKey)
        return message
    }

    // MARK: Safety code

    public static let safetyCodeTag = "nexus/v1/safety-code"

    /// 64-entry table — copied verbatim, in order, from backend/internal/security/pairing/pairing.go.
    public static let safetyCodeEmojis: [String] = [
        "🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼",
        "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
        "🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺",
        "🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
        "🐢", "🐍", "🦎", "🐙", "🦑", "🦀", "🐡", "🐠",
        "🐟", "🐬", "🐳", "🦈", "🐊", "🐘", "🦒", "🌵",
        "🌲", "🌴", "🍄", "🌻", "🌙", "🍎", "🍌", "🍇",
        "🍓", "🍒", "🍍", "🥕", "🌽", "🍕", "🎈", "🔑",
    ]

    /// keys sorted bytewise (empty skipped); `d = SHA256(tag 0x00 || lp(k1) || lp(k2) ...)`;
    /// emoji i (0..5) = table[d[i] >> 2], joined with single spaces.
    public static func safetyCode(publicKeys: [Data]) -> String {
        let sorted = publicKeys.filter { !$0.isEmpty }.sorted { $0.lexicographicallyPrecedes($1) }
        var message = tagged(safetyCodeTag)
        for key in sorted { appendLengthPrefixed(key, to: &message) }
        let digest = Array(SHA256.hash(data: message))
        return (0..<6).map { safetyCodeEmojis[Int(digest[$0] >> 2)] }.joined(separator: " ")
    }

    // MARK: Encoding helpers

    public static func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }

    /// base64url without padding.
    public static func base64URL(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    /// Decodes base64url (padding optional; standard alphabet also accepted).
    public static func base64URLDecode(_ string: String) -> Data? {
        var s = string.trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        while s.count % 4 != 0 { s.append("=") }
        return Data(base64Encoded: s)
    }
}
