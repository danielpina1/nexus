import CryptoKit
import Foundation
import NexusAPI
import SwiftProtobuf

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  SEALED ENVELOPE FORMAT — MUST MATCH backend/internal/security/envelope                  ║
// ║  (docs/WIRE_FORMATS.md "Sealed envelope")                                                ║
// ║                                                                                          ║
// ║  From proto/nexus/v1/security.proto:                                                     ║
// ║    ciphertext = HPKE.Seal(recipient_pub, info="nexus/v1/envelope", aad=header_bytes, pt) ║
// ║    signature  = Sign(sender_key, SHA256(header_bytes || encapsulated_key || ciphertext)) ║
// ║                                                                                          ║
// ║  • HPKE mode BASE, suite X25519 / HKDF-SHA256 / ChaCha20-Poly1305 (RFC 9180).            ║
// ║  • header_bytes = serialized `EnvelopeHeader` (protobuf binary), used verbatim as AAD.   ║
// ║  • Receiver checks: purpose, recipient key id (hex of first 16 bytes of SHA256(our       ║
// ║    X25519 pub)), nonce ≥ 16 bytes and not seen before. Timestamp: the spec says ±60 s;   ║
// ║    APNs can deliver late, so notifications allow `maxAge` instead (deliberate deviation).║
// ║  • Notification push: APNs payload key `nexus_envelope` (top level, next to `aps`).      ║
// ║    Value = base64 (std, padded) of the protobuf-binary `SealedEnvelope`.                 ║
// ║    Also accepted: base64 of protojson, or an inline protojson object.                    ║
// ║  • Notification plaintext = UTF-8 JSON `NotificationPlaintext` (snake_case keys) below.  ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum EnvelopeFormat {
    public static let hpkeInfo = Data("nexus/v1/envelope".utf8)
    public static let ciphersuite = HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly
    public static let suiteName = "hpke-x25519-sha256-chacha20poly1305+p256-ecdsa"
    public static let pushPayloadKey = "nexus_envelope"
    /// Notifications older than this are rejected (APNs may hold pushes while offline).
    public static let maxAge: TimeInterval = 24 * 3600
    /// Allowed clock skew into the future.
    public static let maxSkew: TimeInterval = 60
    public static let minNonceLength = 16
}

/// Decrypted notification content (JSON inside the envelope).
public struct NotificationPlaintext: Codable, Sendable {
    public struct Item: Codable, Sendable, Hashable {
        public var name: String
        public var detail: String?
        public var amount: String?
    }

    public var title: String
    public var subtitle: String?
    public var body: String
    public var category: String?
    public var threadID: String?
    public var taskID: String?
    public var approvalID: String?
    public var agentID: String?
    public var agentName: String?
    /// Hub-issued blob URL (must be on the paired Hub's host) for a screenshot / diff image.
    public var attachmentURL: String?
    public var choices: [String]?
    public var items: [Item]?

    enum CodingKeys: String, CodingKey {
        case title, subtitle, body, category, choices, items
        case threadID = "thread_id"
        case taskID = "task_id"
        case approvalID = "approval_id"
        case agentID = "agent_id"
        case agentName = "agent_name"
        case attachmentURL = "attachment_url"
    }
}

public enum EnvelopeCrypto {
    public enum EnvelopeError: Error {
        case undecodable
        case wrongPurpose
        case expired
        case badSignature
        case wrongRecipient
        case replayed
    }

    /// Parses the `nexus_envelope` value from a push `userInfo`.
    public static func envelope(fromPushValue value: Any) throws -> Nexus_V1_SealedEnvelope {
        if let string = value as? String {
            if let data = Data(base64Encoded: string) {
                if let env = try? Nexus_V1_SealedEnvelope(serializedBytes: data), !env.ciphertext.isEmpty {
                    return env
                }
                if let json = String(data: data, encoding: .utf8),
                   let env = try? Nexus_V1_SealedEnvelope(jsonString: json) {
                    return env
                }
            }
            if let env = try? Nexus_V1_SealedEnvelope(jsonString: string) {
                return env
            }
        } else if let dict = value as? [String: Any],
                  JSONSerialization.isValidJSONObject(dict),
                  let data = try? JSONSerialization.data(withJSONObject: dict),
                  let json = String(data: data, encoding: .utf8),
                  let env = try? Nexus_V1_SealedEnvelope(jsonString: json) {
            return env
        }
        throw EnvelopeError.undecodable
    }

    /// Opens an envelope addressed to this device's encryption key.
    ///
    /// - Parameter senderPublicKey: if known, the sender's P-256 signing key; the signature is
    ///   then verified. (Hub/Node signing keys are not yet distributed to the phone — TODO.)
    public static func open(
        _ envelope: Nexus_V1_SealedEnvelope,
        expectedPurpose: Nexus_V1_EnvelopePurpose,
        recipientKey: Curve25519.KeyAgreement.PrivateKey,
        senderPublicKey: P256.Signing.PublicKey? = nil,
        now: Date = Date()
    ) throws -> (header: Nexus_V1_EnvelopeHeader, plaintext: Data) {
        let header = try Nexus_V1_EnvelopeHeader(serializedBytes: envelope.header)
        guard header.purpose == expectedPurpose else { throw EnvelopeError.wrongPurpose }
        let ownKeyID = WireFormat.keyID(WireFormat.encodeX25519PublicKey(recipientKey.publicKey))
        if !header.recipientKeyID.isEmpty && header.recipientKeyID.lowercased() != ownKeyID {
            throw EnvelopeError.wrongRecipient
        }
        if header.hasCreatedAt {
            let age = now.timeIntervalSince(header.createdAt.date)
            if age > EnvelopeFormat.maxAge || age < -EnvelopeFormat.maxSkew { throw EnvelopeError.expired }
        }
        guard header.nonce.count >= EnvelopeFormat.minNonceLength else { throw EnvelopeError.replayed }

        if let senderPublicKey {
            var signed = Data()
            signed.append(envelope.header)
            signed.append(envelope.encapsulatedKey)
            signed.append(envelope.ciphertext)
            let signature = try P256.Signing.ECDSASignature(derRepresentation: envelope.signature)
            guard senderPublicKey.isValidSignature(signature, for: signed) else {
                throw EnvelopeError.badSignature
            }
        }

        var recipient = try HPKE.Recipient(
            privateKey: recipientKey,
            ciphersuite: EnvelopeFormat.ciphersuite,
            info: EnvelopeFormat.hpkeInfo,
            encapsulatedKey: envelope.encapsulatedKey
        )
        let plaintext = try recipient.open(envelope.ciphertext, authenticating: envelope.header)
        // Record the nonce only after successful authentication.
        guard NonceCache.insert(header.nonce) else { throw EnvelopeError.replayed }
        return (header, plaintext)
    }

    /// Convenience for the Notification Service Extension.
    public static func openNotification(userInfo: [AnyHashable: Any]) throws -> NotificationPlaintext {
        guard let value = userInfo[EnvelopeFormat.pushPayloadKey] else { throw EnvelopeError.undecodable }
        let envelope = try envelope(fromPushValue: value)
        let key = try KeyManager.shared.encryptionKey()
        let opened = try open(envelope, expectedPurpose: .notification, recipientKey: key)
        return try JSONDecoder().decode(NotificationPlaintext.self, from: opened.plaintext)
    }
}

/// Small persistent set of recently seen envelope nonces (App Group, shared by the app and
/// the notification service extension).
enum NonceCache {
    private static let key = "nexus.envelope.nonces"
    private static let capacity = 500
    private static let lock = NSLock()

    /// Returns false if the nonce was already seen.
    static func insert(_ nonce: Data) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        let hex = WireFormat.hex(nonce)
        var seen = AppGroup.defaults.stringArray(forKey: key) ?? []
        if seen.contains(hex) { return false }
        seen.append(hex)
        if seen.count > capacity { seen.removeFirst(seen.count - capacity) }
        AppGroup.defaults.set(seen, forKey: key)
        return true
    }
}
