import Connect
import Foundation
import NexusAPI
import SwiftProtobuf

/// Errors surfaced by `HubClient`.
public enum HubError: Error, LocalizedError {
    case notConfigured
    case notPaired
    case invalidURL
    case server(ConnectError)
    case demoMode

    public var errorDescription: String? {
        switch self {
        case .notConfigured: return "No Hub configured. Pair this iPhone first."
        case .notPaired: return "This device is not paired."
        case .invalidURL: return "The Hub URL is invalid."
        case .server(let error): return error.message ?? "Hub error (\(error.code))"
        case .demoMode: return "Not available in demo mode."
        }
    }
}

/// Thin wrapper over a connect-swift `ProtocolClient` pointed at the paired Hub.
///
/// Architecture rule: this is the ONLY network client in the app, widgets, intents and
/// notification extensions. Everything goes to the Hub API (nodes are reached through Hub
/// APIs, e.g. sealed envelopes); nothing talks to nodes, LLMs or other services directly.
///
/// Auth (CONTRIBUTING_AGENTS.md "Wire conventions"): `Authorization: Bearer <token>` where the
/// token is either the dev token from `nexus.yaml` (`security.dev_token`, entered in Settings)
/// or a short-lived access token from `DeviceService.GetChallenge` + `Authenticate` signed with
/// the Secure Enclave device auth key.
///
/// TLS: the Hub's certificate comes from its own private CA. `PinnedURLSessionHTTPClient` pins
/// that CA (`HubConfiguration.caFingerprint`, from the pairing QR) with `HubTrust`.
/// TODO(mTLS): client certificates (SECURITY.md Layer 2).
public final class HubClient: @unchecked Sendable {
    public let configuration: HubConfiguration
    public let protocolClient: ProtocolClient

    public let agents: Nexus_V1_AgentServiceClient
    public let approvals: Nexus_V1_ApprovalServiceClient
    public let architect: Nexus_V1_ArchitectServiceClient
    public let commands: Nexus_V1_CommandServiceClient
    public let devices: Nexus_V1_DeviceServiceClient
    public let events: Nexus_V1_EventServiceClient
    public let nodes: Nexus_V1_NodeServiceClient
    public let tasks: Nexus_V1_TaskServiceClient
    public let teams: Nexus_V1_TeamServiceClient
    public let vault: Nexus_V1_VaultServiceClient

    private let tokens: TokenStore

    /// The Hub URL this client talks to (one of `configuration.hubURLs`).
    public let baseURL: URL

    /// - Parameter baseURL: which of the paired Hub URLs to use; defaults to the one that
    ///   answered last (`HubConfiguration.preferredURL`). `NexusStore` picks it with a
    ///   `HubProbe` and builds a new client when the route changes (Wi-Fi ↔ VPN).
    public init(configuration: HubConfiguration, baseURL: URL? = nil) throws {
        guard let url = baseURL ?? configuration.preferredURL else { throw HubError.notConfigured }
        var host = url.absoluteString
        while host.hasSuffix("/") { host.removeLast() }
        guard url.scheme == "https" || url.scheme == "http" else { throw HubError.invalidURL }

        self.configuration = configuration
        self.baseURL = url
        let codec: Codec = configuration.useJSONCodec ? JSONCodec() : ProtoCodec()
        self.protocolClient = ProtocolClient(
            httpClient: PinnedURLSessionHTTPClient(caFingerprint: configuration.caFingerprint),
            config: ProtocolClientConfig(
                host: host,
                networkProtocol: .connect,
                codec: codec
            )
        )
        self.agents = Nexus_V1_AgentServiceClient(client: protocolClient)
        self.approvals = Nexus_V1_ApprovalServiceClient(client: protocolClient)
        self.architect = Nexus_V1_ArchitectServiceClient(client: protocolClient)
        self.commands = Nexus_V1_CommandServiceClient(client: protocolClient)
        self.devices = Nexus_V1_DeviceServiceClient(client: protocolClient)
        self.events = Nexus_V1_EventServiceClient(client: protocolClient)
        self.nodes = Nexus_V1_NodeServiceClient(client: protocolClient)
        self.tasks = Nexus_V1_TaskServiceClient(client: protocolClient)
        self.teams = Nexus_V1_TeamServiceClient(client: protocolClient)
        self.vault = Nexus_V1_VaultServiceClient(client: protocolClient)
        self.tokens = TokenStore()
    }

    /// Client for the stored configuration (used by widgets, intents and extensions), pointed
    /// at the Hub URL that answered last.
    public static func shared() throws -> HubClient {
        guard let configuration = HubConfiguration.load() else { throw HubError.notConfigured }
        return try HubClient(configuration: configuration)
    }

    /// Probes the paired Hub URLs (`GET /healthz`, 3 s each; last working URL first) and
    /// returns a client for the first one that answers. Remembers the winner in the App Group.
    public static func connect(
        configuration: HubConfiguration,
        path: NetworkPathInfo?
    ) async -> (client: HubClient?, outcome: HubProbe.Outcome) {
        let outcome = await HubProbe.firstReachable(
            configuration.orderedURLs,
            path: path,
            caFingerprint: configuration.caFingerprint
        )
        guard case .reachable(let url) = outcome else { return (nil, outcome) }
        HubRouteMemory.recordContact(url: url, viaVPN: path.map(\.hasVPNInterface))
        return (try? HubClient(configuration: configuration, baseURL: url), outcome)
    }

    /// True when `error` looks like "could not reach the Hub" (as opposed to the Hub
    /// answering with an error). Such failures are worth queueing and re-probing.
    public static func isTransportFailure(_ error: Error) -> Bool {
        if error is URLError { return true }
        if case HubError.server(let connectError) = error {
            switch connectError.code {
            case .unavailable, .deadlineExceeded, .unknown, .canceled:
                return true
            default:
                return false
            }
        }
        return false
    }

    // MARK: Auth

    /// Headers for an authenticated call.
    public func authHeaders() async throws -> Headers {
        var headers: Headers = [:]
        let token = try await tokens.token(using: self)
        headers["Authorization"] = ["Bearer \(token)"]
        return headers
    }

    /// Drops the cached access token (e.g. after an `unauthenticated` stream completion).
    public func invalidateToken() async {
        await tokens.invalidate()
    }

    /// Performs an authenticated unary call; retries once with a fresh token on
    /// `unauthenticated`.
    public func call<Output: ProtobufMessage>(
        _ operation: (Headers) async -> ResponseMessage<Output>
    ) async throws -> Output {
        let headers = try await authHeaders()
        var response = await operation(headers)
        if response.code == .unauthenticated, await tokens.canRefresh {
            await tokens.invalidate()
            let retryHeaders = try await authHeaders()
            response = await operation(retryHeaders)
        }
        switch response.result {
        case .success(let message):
            return message
        case .failure(let error):
            throw HubError.server(error)
        }
    }

    /// Performs an unauthenticated unary call (pairing, challenge/auth).
    public func callUnauthenticated<Output: ProtobufMessage>(
        _ operation: (Headers) async -> ResponseMessage<Output>
    ) async throws -> Output {
        let response = await operation([:])
        switch response.result {
        case .success(let message):
            return message
        case .failure(let error):
            throw HubError.server(error)
        }
    }

    /// Obtains a fresh access token by signing a Hub challenge with the device auth key.
    fileprivate func authenticateDevice() async throws -> (token: String, expiresAt: Date) {
        guard let deviceID = configuration.deviceID else { throw HubError.notPaired }
        var challengeRequest = Nexus_V1_GetChallengeRequest()
        challengeRequest.deviceID = deviceID
        let challenge = try await callUnauthenticated { headers in
            await self.devices.getChallenge(request: challengeRequest, headers: headers)
        }.challenge

        let key = try KeyManager.shared.authKey()
        let signature = try key.signature(for: WireFormat.authenticationMessage(deviceID: deviceID, challenge: challenge))

        var authRequest = Nexus_V1_AuthenticateRequest()
        authRequest.deviceID = deviceID
        authRequest.challenge = challenge
        authRequest.signature = WireFormat.encodeSignature(signature)
        let response = try await callUnauthenticated { headers in
            await self.devices.authenticate(request: authRequest, headers: headers)
        }
        let expiry = response.hasExpiresAt ? response.expiresAt.date : Date().addingTimeInterval(10 * 60)
        return (response.accessToken, expiry)
    }
}

/// Caches the bearer token (shared through the Keychain access group so extensions reuse it).
actor TokenStore {
    private var inFlight: Task<String, Error>?

    var canRefresh: Bool {
        // A dev token cannot be refreshed; device tokens can.
        Keychain.string(for: .devToken) == nil
    }

    func invalidate() {
        Keychain.delete(.accessToken)
        Keychain.delete(.accessTokenExpiry)
    }

    func token(using client: HubClient) async throws -> String {
        if let dev = Keychain.string(for: .devToken), !dev.isEmpty {
            return dev
        }
        if let token = Keychain.string(for: .accessToken),
           let expiryString = Keychain.string(for: .accessTokenExpiry),
           let expiry = TimeInterval(expiryString),
           Date(timeIntervalSince1970: expiry).timeIntervalSinceNow > 30 {
            return token
        }
        if let inFlight {
            return try await inFlight.value
        }
        let task = Task<String, Error> {
            let result = try await client.authenticateDevice()
            Keychain.setString(result.token, for: .accessToken)
            Keychain.setString(String(result.expiresAt.timeIntervalSince1970), for: .accessTokenExpiry)
            return result.token
        }
        inFlight = task
        defer { inFlight = nil }
        return try await task.value
    }
}

// MARK: - Small helpers

public extension Nexus_V1_Money {
    /// "€84.30" style formatting.
    var formatted: String {
        let amount = Decimal(minorUnits) / 100
        return amount.formatted(.currency(code: currency.isEmpty ? "EUR" : currency))
    }
}

public extension Google_Protobuf_Timestamp {
    var optionalDate: Date? {
        seconds == 0 && nanos == 0 ? nil : date
    }
}

public extension String {
    /// Random idempotency key for commands / messages.
    static func idempotencyKey() -> String { UUID().uuidString.lowercased() }
}
