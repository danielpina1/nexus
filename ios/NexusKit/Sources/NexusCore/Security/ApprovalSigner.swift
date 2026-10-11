import CryptoKit
import Foundation
import LocalAuthentication
import NexusAPI
import SwiftProtobuf

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  APPROVAL SIGNATURE LAYOUT — MUST MATCH backend/internal/security                        ║
// ║                                                                                          ║
// ║  The Node verifies this signature itself (edge verification, SECURITY.md §1 Layer 5/§5). ║
// ║  Signed message (concatenation, no separators, no length prefixes):                      ║
// ║                                                                                          ║
// ║     approval_id   UTF-8 bytes of SignedApproval.approval_id                              ║
// ║     approve       1 byte: 0x01 = approve, 0x00 = deny                                    ║
// ║     challenge     raw bytes of SignedApproval.challenge (as issued in Approval.challenge)║
// ║     action_sha256 raw 32 bytes (Approval.payload_hash)                                   ║
// ║     expires_at    int64 BIG-ENDIAN unix seconds (8 bytes) of SignedApproval.expires_at   ║
// ║                                                                                          ║
// ║  signature = ECDSA-P256( SHA-256(message) ), ASN.1 DER encoded (Go: ecdsa.VerifyASN1     ║
// ║  over sha256.Sum256(message)).                                                           ║
// ║                                                                                          ║
// ║  Key used:                                                                               ║
// ║    • TrustLevel.BIOMETRIC → device APPROVAL key (Device.approval_public_key, Face ID)    ║
// ║    • TrustLevel.APPROVE and all DENY decisions → device AUTH key (Device.public_key),    ║
// ║      because they can be issued from a background Live Activity / notification action   ║
// ║      where Face ID cannot be shown.                                                      ║
// ║  The integrator should adjust ONLY `ApprovalSignature.message(...)` and `keyKind(...)`.  ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public enum ApprovalSignature {
    public static func message(
        approvalID: String,
        approve: Bool,
        challenge: Data,
        actionSHA256: Data,
        expiresAtUnixSeconds: Int64
    ) -> Data {
        var message = Data()
        message.append(Data(approvalID.utf8))
        message.append(approve ? 0x01 : 0x00)
        message.append(challenge)
        message.append(actionSHA256)
        WireFormat.appendBigEndian(expiresAtUnixSeconds, to: &message)
        return message
    }

    public enum KeyKind: Sendable { case auth, approval }

    public static func keyKind(level: Nexus_V1_TrustLevel, approve: Bool) -> KeyKind {
        if approve && level == .biometric { return .approval }
        return .auth
    }

    /// Default validity of a decision when the approval itself carries no expiry.
    public static let defaultValidity: TimeInterval = 5 * 60
}

/// Signs approval decisions. Biometric approvals unlock the Secure Enclave approval key with
/// Face ID through `LAContext`.
public struct ApprovalSigner: Sendable {
    public enum SignerError: Error, LocalizedError {
        case notPaired
        case biometryUnavailable(String)
        case authenticationFailed
        case missingActionHash

        public var errorDescription: String? {
            switch self {
            case .notPaired: return "This device is not paired with a Hub."
            case .biometryUnavailable(let why): return "Face ID unavailable: \(why)"
            case .authenticationFailed: return "Authentication failed."
            case .missingActionHash: return "The approval has no action hash; refusing to sign."
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
        let actionHash = actionSHA256 ?? approval.payloadHash
        if approve && actionHash.isEmpty { throw SignerError.missingActionHash }

        let expiresAt: Date = approval.hasExpiresAt
            ? approval.expiresAt.date
            : Date().addingTimeInterval(ApprovalSignature.defaultValidity)
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

    /// Runs Face ID and returns the evaluated context (reused to unlock the approval key
    /// without a second prompt).
    public func authenticate(reason: String) async throws -> LAContext {
        let context = LAContext()
        context.localizedCancelTitle = "Cancel"
        var error: NSError?
        let policy: LAPolicy
        if context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error) {
            policy = .deviceOwnerAuthenticationWithBiometrics
        } else if !keys.isHardwareBacked {
            // Simulator without enrolled biometry: fall back to passcode so demos still work.
            policy = .deviceOwnerAuthentication
        } else {
            throw SignerError.biometryUnavailable(error?.localizedDescription ?? "not enrolled")
        }
        let ok = try await context.evaluatePolicy(policy, localizedReason: reason)
        guard ok else { throw SignerError.authenticationFailed }
        return context
    }
}
