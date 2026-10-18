import Foundation
import Network

// Remote access (docs/NETWORK.md): the Hub is never exposed to the internet. At home the app
// reaches it directly on Wi-Fi; away from home through the UniFi VPN (WireGuard On-Demand or
// WiFiman Teleport). This file decides *whether* and *how* the Hub is reachable right now.

/// Whether the paired Hub can be reached, as shown in the UI.
public enum HubReachability: Equatable, Sendable {
    /// Not probed yet (app just started, or not paired).
    case unknown
    /// A `GET /healthz` probe is in flight.
    case checking
    /// The Hub answered at `url`. `viaVPN` is a best-effort guess from the network path
    /// (VPN tunnels show up as `.other` interfaces); nil when it cannot be told.
    case connected(url: URL, viaVPN: Bool?)
    /// The phone has a network, but no Hub URL answered (typically: away from home, VPN off).
    case unreachable(reason: String)
    /// The phone has no network at all.
    case offline

    public var isConnected: Bool {
        if case .connected = self { return true }
        return false
    }

    /// Coarse phase for `.task(id:)` / `onChange`: "connected", "unavailable" or "pending".
    public var phase: String {
        switch self {
        case .connected: return "connected"
        case .unreachable, .offline: return "unavailable"
        case .unknown, .checking: return "pending"
        }
    }

    /// True for the states in which commands are queued instead of sent.
    public var isUnavailable: Bool {
        switch self {
        case .unreachable, .offline: return true
        default: return false
        }
    }
}

/// Snapshot of the device's network path (from `NWPathMonitor`).
public struct NetworkPathInfo: Equatable, Sendable {
    public var isSatisfied: Bool
    public var usesWiFi: Bool
    public var usesCellular: Bool
    public var usesWired: Bool
    /// A VPN-like interface (`utun…`, reported as `NWInterface.InterfaceType.other`) is up.
    /// Split-tunnel WireGuard does not become the default route, so this also looks at
    /// `availableInterfaces`, not just the interface in use.
    public var hasVPNInterface: Bool
    public var isExpensive: Bool
    public var isConstrained: Bool

    public init(
        isSatisfied: Bool,
        usesWiFi: Bool = false,
        usesCellular: Bool = false,
        usesWired: Bool = false,
        hasVPNInterface: Bool = false,
        isExpensive: Bool = false,
        isConstrained: Bool = false
    ) {
        self.isSatisfied = isSatisfied
        self.usesWiFi = usesWiFi
        self.usesCellular = usesCellular
        self.usesWired = usesWired
        self.hasVPNInterface = hasVPNInterface
        self.isExpensive = isExpensive
        self.isConstrained = isConstrained
    }

    init(_ path: NWPath) {
        let available = path.availableInterfaces.map(\.type)
        self.init(
            isSatisfied: path.status == .satisfied,
            usesWiFi: path.usesInterfaceType(.wifi),
            usesCellular: path.usesInterfaceType(.cellular),
            usesWired: path.usesInterfaceType(.wiredEthernet),
            hasVPNInterface: path.usesInterfaceType(.other) || available.contains(.other),
            isExpensive: path.isExpensive,
            isConstrained: path.isConstrained
        )
    }

    /// Short description, e.g. "Wi-Fi + VPN", "Cellular".
    public var label: String {
        guard isSatisfied else { return "No network" }
        var base = usesWiFi ? "Wi-Fi" : usesCellular ? "Cellular" : usesWired ? "Wired" : "Network"
        if hasVPNInterface { base += " + VPN" }
        return base
    }
}

/// Wraps `NWPathMonitor` and reports *meaningful* path changes (Wi-Fi ↔ cellular, VPN up/down,
/// network lost/back) on the main actor.
public final class NetworkMonitor: @unchecked Sendable {
    public typealias Handler = @MainActor @Sendable (NetworkPathInfo) -> Void

    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "nexus.network-monitor")
    private let lock = NSLock()
    private var last: NetworkPathInfo?
    private var started = false

    public init() {}

    deinit {
        monitor.cancel()
    }

    /// Current path (nil until the monitor delivered its first update).
    public var current: NetworkPathInfo? {
        lock.lock()
        defer { lock.unlock() }
        return last
    }

    /// Starts monitoring; `handler` is called for the initial path and every change.
    public func start(_ handler: @escaping Handler) {
        lock.lock()
        let alreadyStarted = started
        started = true
        lock.unlock()
        guard !alreadyStarted else { return }

        monitor.pathUpdateHandler = { [weak self] path in
            guard let self else { return }
            let info = NetworkPathInfo(path)
            self.lock.lock()
            let changed = info != self.last
            self.last = info
            self.lock.unlock()
            guard changed else { return }
            Task { @MainActor in handler(info) }
        }
        monitor.start(queue: queue)
    }
}

/// `GET <hub>/healthz` probes with short timeouts. Used before declaring the Hub unreachable,
/// to pick which of the paired Hub URLs works, and after every network path change.
public enum HubProbe {
    /// Connect + response timeout per URL.
    public static let timeout: TimeInterval = 3

    public enum Outcome: Sendable {
        case reachable(URL)
        case unreachable(reason: String)
        case offline
    }

    /// Probe sessions, one per pinned CA fingerprint (usually exactly one).
    private static let sessions = ProbeSessions()

    /// Probes one base URL. Any HTTP answer from `/healthz` with a 2xx status counts.
    ///
    /// - Parameter caFingerprint: the Hub CA pin (pairing QR `ca=`); the server certificate
    ///   must chain to that CA (`HubTrust`). nil: system trust (not paired).
    /// - Returns: on a pin failure, `.failure(HubTrustError)`.
    public static func probe(_ baseURL: URL, caFingerprint: String? = nil) async -> Result<Void, Error> {
        let url = baseURL.appendingPathComponent("healthz")
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: timeout)
        request.httpMethod = "GET"
        let (session, trust) = sessions.session(for: caFingerprint)
        do {
            let (_, response) = try await session.data(for: request)
            if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
                return .failure(URLError(.badServerResponse))
            }
            return .success(())
        } catch {
            if let trustError = trust.trustError(for: error, url: url) {
                return .failure(trustError)
            }
            return .failure(error)
        }
    }

    /// Tries `urls` in order (callers put the last working URL first) and returns the first
    /// that answers. When none answers, the reason is the most informative failure across all
    /// URLs (a certificate problem on one URL is not hidden by a timeout on a later one).
    public static func firstReachable(
        _ urls: [URL],
        path: NetworkPathInfo?,
        caFingerprint: String? = nil
    ) async -> Outcome {
        if let path, !path.isSatisfied { return .offline }
        guard !urls.isEmpty else { return .unreachable(reason: "No Hub address configured.") }
        var errors: [Error] = []
        for url in urls {
            if Task.isCancelled { break }
            switch await probe(url, caFingerprint: caFingerprint) {
            case .success:
                return .reachable(url)
            case .failure(let error):
                errors.append(error)
                if let urlError = error as? URLError, urlError.code == .notConnectedToInternet {
                    return .offline
                }
            }
        }
        return .unreachable(reason: describe(mostInformative(errors)))
    }

    /// The failure that says the most about why the Hub could not be used: the first error of
    /// the highest `informativeness`.
    static func mostInformative(_ errors: [Error]) -> Error? {
        var best: Error?
        var bestRank = Int.min
        for error in errors {
            let rank = informativeness(error)
            if rank > bestRank {
                best = error
                bestRank = rank
            }
        }
        return best
    }

    /// Higher is more informative. A pin or certificate failure means a Hub-like server answered
    /// and was refused; an HTTP error means it answered; a refused connection or a DNS failure
    /// still says something about the address; a timeout says the least.
    static func informativeness(_ error: Error) -> Int {
        if error is HubTrustError { return 6 }
        guard let urlError = error as? URLError else { return 0 }
        switch urlError.code {
        case .serverCertificateUntrusted, .serverCertificateHasBadDate,
             .serverCertificateNotYetValid, .serverCertificateHasUnknownRoot,
             .secureConnectionFailed, .clientCertificateRejected, .clientCertificateRequired:
            return 5
        case .badServerResponse:
            return 4
        case .cannotConnectToHost, .networkConnectionLost:
            return 3
        case .cannotFindHost, .dnsLookupFailed:
            return 2
        case .timedOut:
            return 1
        default:
            return 0
        }
    }

    /// Calm, user-facing reason for a probe failure.
    static func describe(_ error: Error?) -> String {
        if let trustError = error as? HubTrustError {
            return trustError.errorDescription ?? HubTrust.mismatchMessage
        }
        guard let urlError = error as? URLError else {
            return "Your Nexus did not answer."
        }
        switch urlError.code {
        case .cannotFindHost, .dnsLookupFailed:
            return "The Hub's name doesn't resolve on this network."
        case .timedOut:
            return "Your Nexus did not answer in time."
        case .cannotConnectToHost, .networkConnectionLost:
            return "Your Nexus refused or dropped the connection."
        case .secureConnectionFailed, .serverCertificateUntrusted, .serverCertificateHasBadDate,
             .serverCertificateNotYetValid, .serverCertificateHasUnknownRoot:
            return "The Hub's certificate was not accepted."
        case .clientCertificateRejected, .clientCertificateRequired:
            return "The Hub asked for a device certificate this iPhone doesn't have."
        case .badServerResponse:
            return "The Hub answered with an error."
        case .notConnectedToInternet:
            return "No internet connection."
        default:
            return "Your Nexus did not answer."
        }
    }
}

/// Probe URLSessions keyed by CA fingerprint, each with its pinning delegate. Kept small: the
/// app pins one Hub at a time, so older entries are dropped when a new fingerprint shows up.
private final class ProbeSessions: @unchecked Sendable {
    private static let maxEntries = 2

    private let lock = NSLock()
    private var entries: [String: (session: URLSession, trust: HubTrustDelegate)] = [:]
    private var order: [String] = []

    func session(for caFingerprint: String?) -> (session: URLSession, trust: HubTrustDelegate) {
        let key = HubTrust.normalize(caFingerprint) ?? ""
        lock.lock()
        defer { lock.unlock() }
        if let entry = entries[key] { return entry }

        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = HubProbe.timeout
        configuration.timeoutIntervalForResource = HubProbe.timeout + 1
        configuration.waitsForConnectivity = false
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.urlCache = nil
        configuration.httpMaximumConnectionsPerHost = 2
        let made = HubTrust.makeSession(caFingerprint: caFingerprint, configuration: configuration)
        let entry = (session: made.session, trust: made.delegate)

        entries[key] = entry
        order.append(key)
        while order.count > Self.maxEntries {
            let evicted = order.removeFirst()
            entries.removeValue(forKey: evicted)?.session.finishTasksAndInvalidate()
        }
        return entry
    }
}

// MARK: - Remembered route (App Group, shared with widgets / intents / extensions)

public enum HubRouteMemory {
    /// The Hub URL that answered most recently.
    public static var lastWorkingURL: URL? {
        get { AppGroup.defaults.string(forKey: AppGroup.Key.lastWorkingHubURL).flatMap(URL.init(string:)) }
        set { AppGroup.defaults.set(newValue?.absoluteString, forKey: AppGroup.Key.lastWorkingHubURL) }
    }

    /// Last time the Hub answered (a probe, a call or the event stream).
    public static var lastContact: Date? {
        get {
            let value = AppGroup.defaults.double(forKey: AppGroup.Key.lastHubContact)
            return value > 0 ? Date(timeIntervalSince1970: value) : nil
        }
        set { AppGroup.defaults.set(newValue?.timeIntervalSince1970 ?? 0, forKey: AppGroup.Key.lastHubContact) }
    }

    /// Whether the last successful contact went through the VPN (best effort).
    public static var lastContactViaVPN: Bool? {
        get { AppGroup.defaults.object(forKey: AppGroup.Key.lastHubContactViaVPN) as? Bool }
        set { AppGroup.defaults.set(newValue, forKey: AppGroup.Key.lastHubContactViaVPN) }
    }

    public static func recordContact(url: URL, viaVPN: Bool?) {
        lastWorkingURL = url
        lastContact = Date()
        lastContactViaVPN = viaVPN
    }

    public static func clear() {
        let defaults = AppGroup.defaults
        defaults.removeObject(forKey: AppGroup.Key.lastWorkingHubURL)
        defaults.removeObject(forKey: AppGroup.Key.lastHubContact)
        defaults.removeObject(forKey: AppGroup.Key.lastHubContactViaVPN)
    }
}
