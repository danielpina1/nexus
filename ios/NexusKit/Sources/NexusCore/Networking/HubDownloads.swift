import Foundation

/// Plain HTTPS downloads from the paired Hub (blob / attachment URLs), outside connect-swift:
/// notification attachments and approval previews.
///
/// Requests go through a URLSession that pins the Hub CA (`HubTrust`) and carry the bearer
/// token. Architecture rule: only URLs on the paired Hub are fetched (`isHubURL`).
public enum HubDownloads {
    public enum DownloadError: Error, LocalizedError {
        case notAHubURL
        case notPaired
        case badStatus(Int)

        public var errorDescription: String? {
            switch self {
            case .notAHubURL: return "Only files from your Hub can be opened."
            case .notPaired: return "This device is not paired."
            case .badStatus(let status): return "The Hub answered with an error (\(status))."
            }
        }
    }

    /// True when `url` points at one of the paired Hub's addresses (same scheme and host).
    public static func isHubURL(_ url: URL, configuration: HubConfiguration? = HubConfiguration.load()) -> Bool {
        guard let configuration else { return false }
        return configuration.hubURLs.contains { hub in
            hub.host?.lowercased() == url.host?.lowercased() && hub.scheme == url.scheme
        }
    }

    /// GETs `url` into memory.
    public static func data(from url: URL, timeout: TimeInterval = 30) async throws -> (Data, HTTPURLResponse) {
        let (request, session) = try await prepare(url, timeout: timeout)
        let (data, response) = try await session.data(for: request)
        return (data, try check(response))
    }

    /// GETs `url` into a temporary file (move it before using it elsewhere).
    public static func download(from url: URL, timeout: TimeInterval = 20) async throws -> (URL, HTTPURLResponse) {
        let (request, session) = try await prepare(url, timeout: timeout)
        let (fileURL, response) = try await session.download(for: request)
        return (fileURL, try check(response))
    }

    private static func prepare(_ url: URL, timeout: TimeInterval) async throws -> (URLRequest, URLSession) {
        guard let configuration = HubConfiguration.load() else { throw DownloadError.notPaired }
        guard isHubURL(url, configuration: configuration) else { throw DownloadError.notAHubURL }
        var request = URLRequest(url: url)
        request.timeoutInterval = timeout
        if let hub = try? HubClient(configuration: configuration),
           let headers = try? await hub.authHeaders() {
            for (name, values) in headers {
                request.setValue(values.joined(separator: ","), forHTTPHeaderField: name)
            }
        }
        return (request, sessions.session(for: configuration.caFingerprint))
    }

    private static func check(_ response: URLResponse) throws -> HTTPURLResponse {
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else { throw DownloadError.badStatus(http.statusCode) }
        return http
    }

    private static let sessions = DownloadSessions()
}

/// One pinned download session, rebuilt when the pinned fingerprint changes (re-pairing).
private final class DownloadSessions: @unchecked Sendable {
    private let lock = NSLock()
    private var current: (key: String, session: URLSession)?

    func session(for caFingerprint: String?) -> URLSession {
        let key = HubTrust.normalize(caFingerprint) ?? ""
        lock.lock()
        defer { lock.unlock() }
        if let current, current.key == key { return current.session }
        current?.session.finishTasksAndInvalidate()
        let configuration = URLSessionConfiguration.ephemeral
        configuration.waitsForConnectivity = false
        let session = HubTrust.makeSession(caFingerprint: caFingerprint, configuration: configuration).session
        current = (key, session)
        return session
    }
}
