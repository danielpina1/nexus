import CryptoKit
import Foundation
import NexusAPI
import UIKit

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  PAIRING QR FORMAT — MUST MATCH backend (`nexusd pair --device` / web Security page)     ║
// ║                                                                                          ║
// ║   nexus://pair?hub=<url>[&hub=<url2>...]&ca=<hex sha256>&secret=<secret>[&token=<tok>]  ║
// ║                                                                                          ║
// ║   hub     Hub base URL(s), percent-encoded, repeatable (or one comma-separated value).   ║
// ║   ca      lowercase hex SHA-256 of the Hub CA certificate (DER). Optional in dev.        ║
// ║   secret  one-time pairing secret (HKDF input, used as UTF-8 bytes — see WireFormat).    ║
// ║   token   pairing token for PairRequest.pairing_token; defaults to `secret` if absent.   ║
// ║   hub_key optional Hub public key (stored, not yet used).                                ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public struct PairingPayload: Equatable, Sendable {
    public var hubURLs: [URL]
    public var caFingerprint: String?
    public var secret: String
    public var token: String
    public var hubKey: String?

    public enum ParseError: Error, LocalizedError {
        case notAPairingCode
        case missingHub
        case missingSecret

        public var errorDescription: String? {
            switch self {
            case .notAPairingCode: return "This QR code is not a Nexus pairing code."
            case .missingHub: return "The pairing code has no Hub address."
            case .missingSecret: return "The pairing code has no secret."
            }
        }
    }

    public init(hubURLs: [URL], caFingerprint: String?, secret: String, token: String, hubKey: String? = nil) {
        self.hubURLs = hubURLs
        self.caFingerprint = caFingerprint
        self.secret = secret
        self.token = token
        self.hubKey = hubKey
    }

    public init(string: String) throws {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let components = URLComponents(string: trimmed),
              components.scheme?.lowercased() == "nexus",
              components.host?.lowercased() == "pair" else {
            throw ParseError.notAPairingCode
        }
        let items = components.queryItems ?? []
        func values(_ name: String) -> [String] {
            items.filter { $0.name == name }.compactMap(\.value).filter { !$0.isEmpty }
        }

        var urls: [URL] = []
        for raw in values("hub") + values("hubs") {
            for part in raw.split(separator: ",") {
                let candidate = part.trimmingCharacters(in: .whitespaces)
                if let url = URL(string: candidate), let scheme = url.scheme, ["http", "https"].contains(scheme) {
                    urls.append(url)
                }
            }
        }
        guard !urls.isEmpty else { throw ParseError.missingHub }
        guard let secret = values("secret").first else { throw ParseError.missingSecret }

        self.hubURLs = urls
        self.caFingerprint = values("ca").first?.lowercased()
        self.secret = secret
        self.token = values("token").first ?? secret
        self.hubKey = values("hub_key").first
    }
}

/// Result of a successful `DeviceService.Pair`, awaiting safety-code confirmation.
public struct PairingResult: Sendable {
    public var device: Nexus_V1_Device
    public var safetyCode: String
    public var configuration: HubConfiguration
}

/// Phone ↔ Hub pairing (SECURITY.md §3):
/// scan QR → generate keys → pairing proof (HMAC with HKDF(secret)) → `DeviceService.Pair`
/// → show emoji safety code → user confirms → configuration is persisted.
public struct PairingService: Sendable {
    public init() {}

    /// HMAC-SHA256 pairing proof over the three public keys.
    public static func pairingProof(secret: String, keys: DevicePublicKeys) -> Data {
        let macKey = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: WireFormat.pairingSecretBytes(secret)),
            salt: WireFormat.pairingHKDFSalt,
            info: WireFormat.pairingHKDFInfo,
            outputByteCount: WireFormat.pairingKeyLength
        )
        let message = WireFormat.pairingProofMessage(
            authPublicKey: keys.auth,
            approvalPublicKey: keys.approval,
            encryptionPublicKey: keys.encryption
        )
        let mac = HMAC<SHA256>.authenticationCode(for: message, using: macKey)
        return Data(mac)
    }

    /// Generates fresh keys and calls `DeviceService.Pair`. Nothing is persisted until
    /// `confirm(_:)` is called after the user compared the safety code.
    @MainActor
    public func pair(payload: PairingPayload, deviceName: String? = nil) async throws -> PairingResult {
        let name = deviceName ?? UIDevice.current.name
        let keys = try KeyManager.shared.regenerateKeys()

        var configuration = HubConfiguration(
            hubURLs: payload.hubURLs,
            caFingerprint: payload.caFingerprint,
            deviceName: name
        )
        let client = try HubClient(configuration: configuration)

        var request = Nexus_V1_PairRequest()
        request.pairingToken = payload.token
        request.name = name
        request.kind = .ios
        request.publicKey = keys.auth
        request.approvalPublicKey = keys.approval
        request.encryptionPublicKey = keys.encryption
        request.pairingProof = Self.pairingProof(secret: payload.secret, keys: keys)
        // TODO(App Attest): DCAppAttestService attestation over SHA256(pairing_proof) on device.

        let response = try await client.callUnauthenticated { headers in
            await client.devices.pair(request: request, headers: headers)
        }

        configuration.deviceID = response.device.id
        configuration.pairedAt = Date()
        configuration.safetyCode = response.safetyCode
        return PairingResult(
            device: response.device,
            safetyCode: response.safetyCode.isEmpty ? Self.localSafetyCode(keys: keys) : response.safetyCode,
            configuration: configuration
        )
    }

    /// Persists the configuration once the user confirmed the safety code.
    public func confirm(_ result: PairingResult) {
        var configuration = result.configuration
        configuration.safetyCode = result.safetyCode
        configuration.save()
        Keychain.delete(.accessToken)
        Keychain.delete(.accessTokenExpiry)
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.lastEventSeq)
    }

    /// Fallback display code if the Hub did not send one (device keys only; NOT a substitute
    /// for the Hub-computed code that covers both parties).
    public static func localSafetyCode(keys: DevicePublicKeys) -> String {
        let emoji = ["🍎", "🚀", "🐙", "🌵", "🎈", "🔑", "🦊", "🌙", "⚓️", "🎲", "🍀", "🐝", "🔥", "🌊", "🎸", "🧭"]
        var data = keys.auth
        data.append(keys.approval)
        data.append(keys.encryption)
        let digest = Array(SHA256.hash(data: data))
        return (0..<6).map { emoji[Int(digest[$0] & 0x0F)] }.joined(separator: " ")
    }
}
