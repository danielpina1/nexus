import CryptoKit
import Foundation
import Security

// The Hub serves TLS with a certificate from its own private CA (backend/internal/security/pki):
// the chain it sends is [leaf, CA]. iOS does not trust that CA, so every connection to the Hub
// has to be validated here, against the CA the user scanned at pairing time: the pairing QR
// carries `ca=<hex SHA-256 of the CA certificate DER>` (pki.Fingerprint), stored as
// `HubConfiguration.caFingerprint`.
//
// Validation (HubTrust.evaluate):
//   1. find the certificate in the server's chain whose SHA-256(DER) equals the pinned
//      fingerprint (or, if the server ever stops sending the CA, the CA remembered from an
//      earlier successful connection, re-checked against the fingerprint);
//   2. make it the ONLY anchor, with the SSL server policy for the host;
//   3. evaluate. Hostname, validity dates and key usage are still checked by the system.
//
// Not paired (no fingerprint): default system handling, unchanged.

/// Pins the Hub's private CA for every connection the app and its extensions make to the Hub.
public enum HubTrust {
    /// Why a server certificate was rejected.
    public enum Failure: Error, Equatable, Sendable {
        /// No certificate in the chain matches the CA fingerprint from the pairing QR: this is
        /// not the Hub that was scanned (or the Hub's CA was regenerated).
        case caMismatch
        /// The chain does lead to the pinned CA, but the system rejected it (wrong host name,
        /// expired / not yet valid, wrong key usage…). `reason` is the system's description.
        case evaluationFailed(reason: String)
        /// A Security framework call failed while preparing the evaluation.
        case setupFailed(status: Int32)
    }

    /// User-facing sentence for a pin mismatch.
    public static let mismatchMessage = "The Hub's certificate doesn't match the one you scanned."

    // MARK: Fingerprints

    /// Canonical form of a fingerprint: lowercase hex without separators (`AB:cd ef` → `abcdef`).
    /// Returns nil when nothing is left (not paired). A malformed fingerprint is kept as is so
    /// that it matches nothing and the connection is rejected rather than silently unpinned.
    public static func normalize(_ fingerprint: String?) -> String? {
        guard let fingerprint else { return nil }
        let cleaned = fingerprint.lowercased().filter { !$0.isWhitespace && $0 != ":" }
        return cleaned.isEmpty ? nil : cleaned
    }

    /// Lowercase hex SHA-256 of DER bytes (same as Go's `pki.Fingerprint`).
    public static func fingerprint(ofDER der: Data) -> String {
        SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
    }

    /// Lowercase hex SHA-256 of a certificate's DER encoding.
    public static func fingerprint(of certificate: SecCertificate) -> String {
        fingerprint(ofDER: SecCertificateCopyData(certificate) as Data)
    }

    /// The certificate in `chain` whose fingerprint equals `caFingerprint` (any notation).
    public static func pinnedCertificate(in chain: [SecCertificate], caFingerprint: String) -> SecCertificate? {
        guard let pin = normalize(caFingerprint) else { return nil }
        return chain.first { fingerprint(of: $0) == pin }
    }

    /// The certificates of a server trust (leaf first).
    public static func certificateChain(of trust: SecTrust) -> [SecCertificate] {
        (SecTrustCopyCertificateChain(trust) as? [SecCertificate]) ?? []
    }

    // MARK: Evaluation

    /// Validates `trust` for `host` with the CA identified by `caFingerprint` as the only anchor.
    ///
    /// - Parameter verifyDate: evaluation date; nil means now (tests pin it).
    /// - Returns: the DER of the anchor CA on success.
    public static func evaluate(
        _ trust: SecTrust,
        host: String,
        caFingerprint: String,
        verifyDate: Date? = nil
    ) -> Result<Data, Failure> {
        guard let pin = normalize(caFingerprint) else { return .failure(.caMismatch) }

        let anchor: SecCertificate
        if let presented = pinnedCertificate(in: certificateChain(of: trust), caFingerprint: pin) {
            anchor = presented
        } else if let remembered = rememberedCA(fingerprint: pin) {
            anchor = remembered
        } else {
            return .failure(.caMismatch)
        }

        let policy = SecPolicyCreateSSL(true, host as CFString)
        var status = SecTrustSetPolicies(trust, policy)
        guard status == errSecSuccess else { return .failure(.setupFailed(status: status)) }
        status = SecTrustSetAnchorCertificates(trust, [anchor] as CFArray)
        guard status == errSecSuccess else { return .failure(.setupFailed(status: status)) }
        status = SecTrustSetAnchorCertificatesOnly(trust, true)
        guard status == errSecSuccess else { return .failure(.setupFailed(status: status)) }
        if let verifyDate {
            status = SecTrustSetVerifyDate(trust, verifyDate as CFDate)
            guard status == errSecSuccess else { return .failure(.setupFailed(status: status)) }
        }

        var error: CFError?
        guard SecTrustEvaluateWithError(trust, &error) else {
            let reason = error.map { ($0 as Error).localizedDescription } ?? "The certificate is not trusted."
            return .failure(.evaluationFailed(reason: reason))
        }
        return .success(SecCertificateCopyData(anchor) as Data)
    }

    /// What to answer to an authentication challenge.
    ///
    /// Server-trust challenges are validated with `evaluate` when a fingerprint is pinned; every
    /// other challenge (and server trust when not paired) gets `.performDefaultHandling`.
    /// `onResult` reports the outcome of a pinned evaluation (host, nil on success).
    public static func disposition(
        for challenge: URLAuthenticationChallenge,
        caFingerprint: String?,
        onResult: ((String, Failure?) -> Void)? = nil
    ) -> (URLSession.AuthChallengeDisposition, URLCredential?) {
        let space = challenge.protectionSpace
        guard space.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let pin = normalize(caFingerprint),
              let trust = space.serverTrust else {
            return (.performDefaultHandling, nil)
        }
        switch evaluate(trust, host: space.host, caFingerprint: pin) {
        case .success(let caDER):
            remember(caDER: caDER, fingerprint: pin)
            onResult?(space.host, nil)
            return (.useCredential, URLCredential(trust: trust))
        case .failure(let failure):
            onResult?(space.host, failure)
            return (.cancelAuthenticationChallenge, nil)
        }
    }

    // MARK: Sessions

    /// A URLSession whose delegate pins `caFingerprint` (default handling when nil).
    /// The session retains its delegate; invalidate sessions that are not kept for the app's
    /// lifetime (`finishTasksAndInvalidate()`).
    public static func makeSession(
        caFingerprint: String?,
        configuration: URLSessionConfiguration = .ephemeral
    ) -> (session: URLSession, delegate: HubTrustDelegate) {
        let delegate = HubTrustDelegate(caFingerprint: caFingerprint)
        let session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        return (session, delegate)
    }

    /// True for the URLSession errors a rejected server trust surfaces as (cancelling the
    /// challenge fails the task with `.cancelled`; default handling with the certificate codes).
    public static func isTrustRelated(_ error: Error) -> Bool {
        let nsError = error as NSError
        guard nsError.domain == NSURLErrorDomain else { return false }
        switch nsError.code {
        case NSURLErrorCancelled,
             NSURLErrorSecureConnectionFailed,
             NSURLErrorServerCertificateHasBadDate,
             NSURLErrorServerCertificateUntrusted,
             NSURLErrorServerCertificateHasUnknownRoot,
             NSURLErrorServerCertificateNotYetValid:
            return true
        default:
            return false
        }
    }

    // MARK: Remembered CA (App Group, shared with the extensions)

    /// The CA certificate (DER) that last validated a Hub connection, if its fingerprint still
    /// equals `fingerprint`.
    static func rememberedCA(fingerprint: String) -> SecCertificate? {
        guard let der = AppGroup.defaults.data(forKey: AppGroup.Key.hubCACertificate),
              self.fingerprint(ofDER: der) == fingerprint else { return nil }
        return SecCertificateCreateWithData(nil, der as CFData)
    }

    /// Stores the CA after a successful match (only when it changed).
    static func remember(caDER: Data, fingerprint: String) {
        let defaults = AppGroup.defaults
        guard defaults.data(forKey: AppGroup.Key.hubCACertificate) != caDER,
              self.fingerprint(ofDER: caDER) == fingerprint else { return }
        defaults.set(caDER, forKey: AppGroup.Key.hubCACertificate)
    }

    /// Forgets the remembered CA (unpairing).
    public static func forgetRememberedCA() {
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.hubCACertificate)
    }
}

/// Error for a Hub connection refused by `HubTrust` (as reported by `HubProbe`).
public struct HubTrustError: Error, LocalizedError, Equatable, Sendable {
    public let host: String
    public let failure: HubTrust.Failure

    public init(host: String, failure: HubTrust.Failure) {
        self.host = host
        self.failure = failure
    }

    public var errorDescription: String? {
        switch failure {
        case .caMismatch:
            return HubTrust.mismatchMessage
        case .evaluationFailed:
            return "The Hub's certificate is not valid for \(host) (wrong name or expired)."
        case .setupFailed:
            return "The Hub's certificate could not be checked."
        }
    }
}

/// Records the most recent pinned-evaluation failure per host, so that a task that failed with
/// `.cancelled` because its server trust was rejected can be reported as such.
final class HubTrustFailureLog: @unchecked Sendable {
    private let lock = NSLock()
    private var failures: [String: HubTrust.Failure] = [:]

    func record(host: String, failure: HubTrust.Failure?) {
        let key = host.lowercased()
        lock.lock()
        defer { lock.unlock() }
        if let failure {
            failures[key] = failure
        } else {
            failures.removeValue(forKey: key)
        }
    }

    func failure(forHost host: String?) -> HubTrust.Failure? {
        guard let host else { return nil }
        lock.lock()
        defer { lock.unlock() }
        return failures[host.lowercased()]
    }
}

/// URLSession delegate that pins the Hub CA (`HubTrust`) at session and task level.
/// Holds no reference to the session, so a session retaining it creates no cycle.
public final class HubTrustDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    /// Normalized pinned fingerprint (nil: not paired, default handling).
    public let caFingerprint: String?
    let failures = HubTrustFailureLog()

    public init(caFingerprint: String?) {
        self.caFingerprint = HubTrust.normalize(caFingerprint)
        super.init()
    }

    /// The last pin failure for `host` (cleared by the next successful evaluation).
    public func trustFailure(forHost host: String?) -> HubTrust.Failure? {
        failures.failure(forHost: host)
    }

    /// `error` as a `HubTrustError` when it was caused by a rejected server trust for `url`.
    public func trustError(for error: Error, url: URL) -> HubTrustError? {
        guard HubTrust.isTrustRelated(error), let host = url.host,
              let failure = trustFailure(forHost: host) else { return nil }
        return HubTrustError(host: host, failure: failure)
    }

    func handle(
        _ challenge: URLAuthenticationChallenge
    ) -> (URLSession.AuthChallengeDisposition, URLCredential?) {
        let log = failures
        return HubTrust.disposition(for: challenge, caFingerprint: caFingerprint) { host, failure in
            log.record(host: host, failure: failure)
        }
    }

    // Session-level challenges (server trust lands here when implemented).
    public func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        let (disposition, credential) = handle(challenge)
        completionHandler(disposition, credential)
    }

    // Task-level challenges.
    public func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        let (disposition, credential) = handle(challenge)
        completionHandler(disposition, credential)
    }
}
