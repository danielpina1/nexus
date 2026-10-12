import CryptoKit
import Foundation
import NexusAPI
import UIKit

// ╔══════════════════════════════════════════════════════════════════════════════════════════╗
// ║  PAIRING QR — MUST MATCH backend/internal/security/pairing (docs/WIRE_FORMATS.md)        ║
// ║                                                                                          ║
// ║   nexus://pair?v=1&k=device&hub=<url>[&hub=<url2>]&ca=<hex sha256 CA DER>&s=<token>      ║
// ║                &hk=<base64url hub key>&exp=<unix>                                        ║
// ║   (base64url JSON {"v","k","hub":[...],"ca","s","hk","exp"} is accepted too)             ║
// ║                                                                                          ║
// ║   s   pairing token = base64url (no padding) of the 32-byte secret; sent verbatim as     ║
// ║       PairRequest.pairing_token; the decoded 32 bytes are the HKDF input.                ║
// ║   hk  Hub identity public key (65-byte P-256) — input of the safety code.                ║
// ╚══════════════════════════════════════════════════════════════════════════════════════════╝
public struct PairingPayload: Equatable, Sendable {
    public var version: Int
    public var kind: String
    public var hubURLs: [URL]
    public var caFingerprint: String?
    /// The `s` token (base64url of the secret).
    public var token: String
    /// Decoded 32-byte secret.
    public var secret: Data
    /// Hub identity public key bytes, if present.
    public var hubKey: Data?
    public var expiresAt: Date?

    public enum ParseError: Error, LocalizedError {
        case notAPairingCode
        case unsupportedVersion
        case notForDevices
        case missingHub
        case badSecret
        case expired

        public var errorDescription: String? {
            switch self {
            case .notAPairingCode: return "This QR code is not a Nexus pairing code."
            case .unsupportedVersion: return "This pairing code needs a newer version of Nexus."
            case .notForDevices: return "This code pairs a machine (node), not a phone."
            case .missingHub: return "The pairing code has no Hub address."
            case .badSecret: return "The pairing code is damaged (bad secret)."
            case .expired: return "This pairing code has expired. Create a new one on the Hub."
            }
        }
    }

    public init(string: String, now: Date = Date()) throws {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        var version = 0
        var kind = ""
        var hubs: [String] = []
        var ca: String?
        var token = ""
        var hk: String?
        var exp: Int64?

        if trimmed.lowercased().hasPrefix("nexus://") {
            guard let components = URLComponents(string: trimmed),
                  components.host?.lowercased() == "pair" else { throw ParseError.notAPairingCode }
            let items = components.queryItems ?? []
            func first(_ name: String) -> String? {
                items.first { $0.name == name }?.value.flatMap { $0.isEmpty ? nil : $0 }
            }
            version = Int(first("v") ?? "") ?? 0
            kind = first("k") ?? ""
            hubs = items.filter { $0.name == "hub" }.compactMap(\.value)
            ca = first("ca")
            token = first("s") ?? ""
            hk = first("hk")
            exp = first("exp").flatMap { Int64($0) }
        } else {
            struct JSONForm: Decodable {
                var v: Int?
                var k: String?
                var hub: [String]?
                var ca: String?
                var s: String?
                var hk: String?
                var exp: Int64?
            }
            guard let data = WireFormat.base64URLDecode(trimmed),
                  let json = try? JSONDecoder().decode(JSONForm.self, from: data) else {
                throw ParseError.notAPairingCode
            }
            version = json.v ?? 0
            kind = json.k ?? ""
            hubs = json.hub ?? []
            ca = json.ca
            token = json.s ?? ""
            hk = json.hk
            exp = json.exp
        }

        guard version == 1 else { throw ParseError.unsupportedVersion }
        if !kind.isEmpty && kind != "device" { throw ParseError.notForDevices }
        let urls = hubs.compactMap { raw -> URL? in
            guard let url = URL(string: raw.trimmingCharacters(in: .whitespaces)),
                  let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https" else { return nil }
            return url
        }
        guard !urls.isEmpty else { throw ParseError.missingHub }
        guard let secret = WireFormat.pairingSecret(fromToken: token) else { throw ParseError.badSecret }
        if let exp, exp > 0, Date(timeIntervalSince1970: TimeInterval(exp)) < now { throw ParseError.expired }

        self.version = version
        self.kind = kind.isEmpty ? "device" : kind
        self.hubURLs = urls
        self.caFingerprint = ca?.lowercased()
        self.token = token
        self.secret = secret
        self.hubKey = hk.flatMap(WireFormat.base64URLDecode)
        self.expiresAt = exp.map { Date(timeIntervalSince1970: TimeInterval($0)) }
    }
}

public enum PairingError: Error, LocalizedError {
    case safetyCodeMismatch
    case hubUnreachable(urls: [URL], reason: String)

    public var errorDescription: String? {
        switch self {
        case .safetyCodeMismatch:
            return "The Hub's safety code does not match the pairing code. Pairing was aborted — someone may be intercepting it."
        case .hubUnreachable(let urls, let reason):
            let list = urls.map(\.absoluteString).joined(separator: ", ")
            return "Can't reach your Hub at \(list). \(reason) Pair at home on Wi-Fi (or with your home VPN on) and try again."
        }
    }
}

/// Result of a successful `DeviceService.Pair`, awaiting safety-code confirmation.
public struct PairingResult: Sendable {
    public var device: Nexus_V1_Device
    public var safetyCode: String
    public var configuration: HubConfiguration
    /// The Hub URL from the QR that answered during pairing.
    public var workingURL: URL?
}

/// Phone ↔ Hub pairing (SECURITY.md §3):
/// scan QR → generate keys → pairing proof (HMAC with HKDF(secret)) → `DeviceService.Pair`
/// → show emoji safety code → user confirms → configuration is persisted.
public struct PairingService: Sendable {
    public init() {}

    /// HMAC-SHA256 pairing proof over the three public keys.
    public static func pairingProof(secret: Data, keys: DevicePublicKeys) -> Data {
        let macKey = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: secret),
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
        // Pair at home on Wi-Fi: probe every Hub URL from the QR (`hub=` is repeatable) and
        // pair through the first one that answers. If none answers, still try the first URL
        // (the probe is advisory) and turn a transport failure into a clear message.
        let probe = await HubProbe.firstReachable(payload.hubURLs, path: nil)
        var probeReason = "It did not answer."
        var baseURL = payload.hubURLs.first
        switch probe {
        case .reachable(let url): baseURL = url
        case .unreachable(let reason): probeReason = reason
        case .offline: probeReason = "This iPhone has no network connection."
        }
        let client = try HubClient(configuration: configuration, baseURL: baseURL)

        var request = Nexus_V1_PairRequest()
        request.pairingToken = payload.token
        request.name = name
        request.kind = .ios
        request.publicKey = keys.auth
        request.approvalPublicKey = keys.approval
        request.encryptionPublicKey = keys.encryption
        request.pairingProof = Self.pairingProof(secret: payload.secret, keys: keys)
        // TODO(App Attest): DCAppAttestService attestation over SHA256(pairing_proof) on device.

        let response: Nexus_V1_PairResponse
        do {
            response = try await client.callUnauthenticated { headers in
                await client.devices.pair(request: request, headers: headers)
            }
        } catch let error where HubClient.isTransportFailure(error) {
            KeyManager.shared.resetAll()
            throw PairingError.hubUnreachable(urls: payload.hubURLs, reason: probeReason)
        }

        // Safety code from the QR's hub key (in-person channel) + our keys; must equal the
        // code the Hub computed, otherwise someone substituted keys in transit.
        let local = payload.hubKey.map { Self.safetyCode(hubKey: $0, keys: keys) }
        if let local, !response.safetyCode.isEmpty, local != response.safetyCode {
            KeyManager.shared.resetAll()
            throw PairingError.safetyCodeMismatch
        }
        let code = local ?? response.safetyCode

        configuration.deviceID = response.device.id
        configuration.pairedAt = Date()
        configuration.safetyCode = code
        return PairingResult(device: response.device, safetyCode: code, configuration: configuration, workingURL: client.baseURL)
    }

    /// Persists the configuration once the user confirmed the safety code.
    public func confirm(_ result: PairingResult) {
        var configuration = result.configuration
        configuration.safetyCode = result.safetyCode
        configuration.save()
        if let url = result.workingURL {
            HubRouteMemory.recordContact(url: url, viaVPN: nil)
        }
        Keychain.delete(.accessToken)
        Keychain.delete(.accessTokenExpiry)
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.lastEventSeq)
    }

    /// Safety code over the Hub identity key + the device's three public keys
    /// (`WireFormat.safetyCode`, identical to the Hub's `pairing.SafetyCode`).
    public static func safetyCode(hubKey: Data, keys: DevicePublicKeys) -> String {
        WireFormat.safetyCode(publicKeys: [hubKey, keys.auth, keys.approval, keys.encryption])
    }
}
