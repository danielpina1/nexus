import CryptoKit
import Foundation
import LocalAuthentication
import NexusAPI
import SwiftProtobuf

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  APPROVAL SIGNATURE — MUST MATCH backend/internal/security/approval                      ║
// ║  (docs/WIRE_FORMATS.md "SignedApproval")                                                 ║
// ║                                                                                          ║
// ║  The Node verifies this itself (edge verification, SECURITY.md Layer 5 / §5).           ║
// ║                                                                                          ║
// ║    M = "nexus/v1/approval" 0x00                                                          ║
// ║        || lp(approval_id)            (UTF-8; lp(x) = u32be(len(x)) || x)                 ║
// ║        || u8(approve ? 1 : 0)                                                            ║
// ║        || lp(challenge)                                                                  ║
// ║        || action_sha256              (exactly 32 bytes = SHA256(JCS(Approval.payload)))  ║
// ║        || i64be(expires_at.seconds)  (must be in the future and ≤ 24 h ahead)            ║
// ║    signature = ECDSA P-256 / SHA-256 over M, DER.                                        ║
// ║                                                                                          ║
// ║  Key: the backend (approval.VerifyFor → Security.ApprovalKey / TrustStore.ApprovalKey)   ║
// ║  verifies EVERY decision (approve and deny, any level) with Device.approval_public_key,  ║
// ║  i.e. the Face ID-bound Secure Enclave key. Consequently decisions cannot be signed from ║
// ║  a background context (Live Activity / notification action) today; those surfaces       ║
// ║  deep-link to the in-app sheet. If the backend starts accepting the device AUTH key for  ║
// ║  TrustLevel.APPROVE (as IOS_LIVE.md §2.4 intends), set `backgroundDecisionsUseAuthKey`.  ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum ApprovalSignature {
    public static let tag = "nexus/v1/approval"

    public static func message(
        approvalID: String,
        approve: Bool,
        challenge: Data,
        actionSHA256: Data,
        expiresAtUnixSeconds: Int64
    ) -> Data {
        var message = WireFormat.tagged(tag)
        WireFormat.appendLengthPrefixed(approvalID, to: &message)
        message.append(approve ? 0x01 : 0x00)
        WireFormat.appendLengthPrefixed(challenge, to: &message)
        message.append(actionSHA256)
        WireFormat.appendBigEndian(expiresAtUnixSeconds, to: &message)
        return message
    }

    /// See the box above. `false` matches the current backend.
    public static let backgroundDecisionsUseAuthKey = false

    /// True when normal-level decisions can be sent without UI (Face ID).
    public static var backgroundDecisionsSupported: Bool { backgroundDecisionsUseAuthKey }

    public enum KeyKind: Sendable { case auth, approval }

    public static func keyKind(level: Nexus_V1_TrustLevel, approve: Bool) -> KeyKind {
        if backgroundDecisionsUseAuthKey && !(approve && level == .biometric) { return .auth }
        return .approval
    }

    /// Default validity of a decision when the approval itself carries no expiry.
    public static let defaultValidity: TimeInterval = 5 * 60
    /// The backend rejects decisions expiring more than 24 h ahead.
    public static let maxValidity: TimeInterval = 24 * 3600 - 60
}

/// Signs approval decisions. Biometric approvals unlock the Secure Enclave approval key with
/// Face ID through `LAContext`.
public struct ApprovalSigner: Sendable {
    public enum SignerError: Error, LocalizedError {
        case notPaired
        case biometryUnavailable(String)
        case authenticationFailed
        case missingActionHash
        case actionHashMismatch

        public var errorDescription: String? {
            switch self {
            case .notPaired: return "This device is not paired with a Hub."
            case .biometryUnavailable(let why): return "Face ID unavailable: \(why)"
            case .authenticationFailed: return "Authentication failed."
            case .missingActionHash: return "The approval has no action hash; refusing to sign."
            case .actionHashMismatch: return "The action shown does not match the hash to be signed. Refusing to sign."
            }
        }
    }

    private let keys: KeyManager

    public init(keys: KeyManager = .shared) {
        self.keys = keys
    }

    /// Builds and signs a `SignedApproval` for `approval`.
    ///
    /// - Parameters:
    ///   - actionSHA256: the hash the phone recomputed from the sealed preview, if available.
    ///     Falls back to `approval.payloadHash`.
    ///   - reason: the Face ID prompt text (only used for biometric approvals).
    public func sign(
        approval: Nexus_V1_Approval,
        approve: Bool,
        deviceID: String,
        actionSHA256: Data? = nil,
        reason: String? = nil
    ) async throws -> Nexus_V1_SignedApproval {
        let actionHash = try Self.verifiedActionHash(for: approval, override: actionSHA256)

        let now = Date()
        var expiresAt: Date = approval.hasExpiresAt
            ? approval.expiresAt.date
            : now.addingTimeInterval(ApprovalSignature.defaultValidity)
        expiresAt = min(expiresAt, now.addingTimeInterval(ApprovalSignature.maxValidity))
        let expiresSeconds = Int64(expiresAt.timeIntervalSince1970.rounded(.down))

        let message = ApprovalSignature.message(
            approvalID: approval.id,
            approve: approve,
            challenge: approval.challenge,
            actionSHA256: actionHash,
            expiresAtUnixSeconds: expiresSeconds
        )

        let signingKey: SigningKey
        switch ApprovalSignature.keyKind(level: approval.level, approve: approve) {
        case .auth:
            signingKey = try keys.authKey()
        case .approval:
            let context = try await authenticate(reason: reason ?? "Approve “\(approval.title)”")
            signingKey = try keys.approvalKey(context: context)
        }
        let signature = try signingKey.signature(for: message)

        var signed = Nexus_V1_SignedApproval()
        signed.approvalID = approval.id
        signed.approve = approve
        signed.challenge = approval.challenge
        signed.actionSha256 = actionHash
        signed.expiresAt = Google_Protobuf_Timestamp(seconds: expiresSeconds, nanos: 0)
        signed.deviceID = deviceID
        signed.signature = WireFormat.encodeSignature(signature)
        return signed
    }

    /// What you see is what you sign: recompute SHA256(JCS(payload)) from the preview the user
    /// is looking at and require it to equal the Hub/Node-provided `payload_hash`.
    public static func verifiedActionHash(for approval: Nexus_V1_Approval, override: Data? = nil) throws -> Data {
        if let override {
            guard override.count == 32 else { throw SignerError.missingActionHash }
            return override
        }
        let provided = approval.payloadHash
        if approval.hasPayload {
            let recomputed = try CanonicalJSON.actionHash(of: approval.payload)
            if !provided.isEmpty && provided != recomputed { throw SignerError.actionHashMismatch }
            return recomputed
        }
        guard provided.count == 32 else { throw SignerError.missingActionHash }
        return provided
    }

    /// Runs Face ID and returns the evaluated context (reused to unlock the approval key
    /// without a second prompt).
    public func authenticate(reason: String) async throws -> LAContext {
        let context = LAContext()
        context.localizedCancelTitle = "Cancel"
        var error: NSError?
        let policy: LAPolicy
        if context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error) {
            policy = .deviceOwnerAuthenticationWithBiometrics
        } else if context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) {
            // No biometry enrolled (approval key then uses .userPresence) or Simulator.
            policy = .deviceOwnerAuthentication
        } else {
            throw SignerError.biometryUnavailable(error?.localizedDescription ?? "no passcode set")
        }
        let ok = try await context.evaluatePolicy(policy, localizedReason: reason)
        guard ok else { throw SignerError.authenticationFailed }
        return context
    }
}
