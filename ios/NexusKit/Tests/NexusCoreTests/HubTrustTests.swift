import Foundation
import Security
import XCTest
@testable import NexusCore

/// CA pinning (HubTrust) and the probe's user-facing errors. No network.
///
/// Fixture: a P-256 CA ("Nexus Test CA", 20 years from 2026-10-18) and a leaf for `nexus.test`
/// / 192.168.1.10 (EKU serverAuth, 2026-10-18 … 2027-01-16), shaped like the certificates from
/// backend/internal/security/pki. Generated with openssl; evaluation pins the verify date.
final class HubTrustTests: XCTestCase {
    static let caDERBase64 = "MIIBuTCCAV6gAwIBAgIUcaJSuIvZkayfYGpKlKroJwn+gVwwCgYIKoZIzj0EAwIwKDEOMAwGA1UECgwFTmV4dXMxFjAUBgNVBAMMDU5leHVzIFRlc3QgQ0EwHhcNMjYxMDE4MTQwODU2WhcNNDYxMDEzMTQwODU2WjAoMQ4wDAYDVQQKDAVOZXh1czEWMBQGA1UEAwwNTmV4dXMgVGVzdCBDQTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABOpiVWX5dzFsJsJRaUF5gWIonqcRKpBW2ubmrC1INJaujs8xUDBTK/JLKCvLX8CL5Ff/vpEJq5y0OXN/9OKC4SSjZjBkMB8GA1UdIwQYMBaAFLYvwiU2oehgcvC+dRZvRXP9wN7YMBIGA1UdEwEB/wQIMAYBAf8CAQAwDgYDVR0PAQH/BAQDAgEGMB0GA1UdDgQWBBS2L8IlNqHoYHLwvnUWb0Vz/cDe2DAKBggqhkjOPQQDAgNJADBGAiEAoVYUjvC9xY+MHR8nnskomAjEXHQ+CKk0DF2o0SHOYQECIQDT9zalhZWpo9yel33/pkGvqjeQd3MuG7IEoEqmAZnEtQ=="
    static let leafDERBase64 = "MIIB5DCCAYmgAwIBAgIUTxW6hGUC9LCRipy1t1z3mPoxD7gwCgYIKoZIzj0EAwIwKDEOMAwGA1UECgwFTmV4dXMxFjAUBgNVBAMMDU5leHVzIFRlc3QgQ0EwHhcNMjYxMDE4MTQwODU2WhcNMjcwMTE2MTQwODU2WjAlMQ4wDAYDVQQKDAVOZXh1czETMBEGA1UEAwwKbmV4dXMudGVzdDBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABGWnTN5nx/gCEUvO3pCobRnEtXQEi6IovGR8AXrCUMa2EVFFVS7m5sdBnSnMIlysa7CDafE6vktpOuK7Ma9u5sOjgZMwgZAwDAYDVR0TAQH/BAIwADAOBgNVHQ8BAf8EBAMCB4AwEwYDVR0lBAwwCgYIKwYBBQUHAwEwGwYDVR0RBBQwEoIKbmV4dXMudGVzdIcEwKgBCjAfBgNVHSMEGDAWgBS2L8IlNqHoYHLwvnUWb0Vz/cDe2DAdBgNVHQ4EFgQUk5yUQhqSHol7NwOBW3OLNCQz2pkwCgYIKoZIzj0EAwIDSQAwRgIhALueYM5DiRgFuzPZXJp35AfoXpFy7vRJwGu6huaLgAP4AiEAlKGk7eclxxgJNp7BZEm2w99KnBm+RnGQDComGHGVLU4="
    /// `sha256sum ca.der` (what the pairing QR carries as `ca=`).
    static let caFingerprint = "17677fdbba931fc44bc3b250cb06845aedc7fbef109c14f56ac88e31cfa65a4e"
    static let leafFingerprint = "d487dad68be53ca77d77d82fb83f32eaab495ee84c292f88a743d7042404bea1"
    /// Inside the leaf's validity window.
    static let verifyDate = Date(timeIntervalSince1970: 1_793_491_200) // 2026-11-01T00:00:00Z

    var caDER: Data { Data(base64Encoded: Self.caDERBase64)! }
    var leafDER: Data { Data(base64Encoded: Self.leafDERBase64)! }

    func certificate(_ der: Data) throws -> SecCertificate {
        try XCTUnwrap(SecCertificateCreateWithData(nil, der as CFData))
    }

    /// A server trust as URLSession would present it: chain [leaf, CA], SSL policy.
    func serverTrust(host: String) throws -> SecTrust {
        let chain = [try certificate(leafDER), try certificate(caDER)]
        var trust: SecTrust?
        let status = SecTrustCreateWithCertificates(chain as CFArray, SecPolicyCreateSSL(true, host as CFString), &trust)
        XCTAssertEqual(status, errSecSuccess)
        return try XCTUnwrap(trust)
    }

    override func setUp() {
        super.setUp()
        HubTrust.forgetRememberedCA()
    }

    override func tearDown() {
        HubTrust.forgetRememberedCA()
        super.tearDown()
    }

    // MARK: Fingerprints

    func testFingerprintOfDERMatchesGoAndOpenSSL() {
        XCTAssertEqual(HubTrust.fingerprint(ofDER: caDER), Self.caFingerprint)
        XCTAssertEqual(HubTrust.fingerprint(ofDER: leafDER), Self.leafFingerprint)
    }

    func testFingerprintOfCertificateUsesDER() throws {
        XCTAssertEqual(HubTrust.fingerprint(of: try certificate(caDER)), Self.caFingerprint)
    }

    func testNormalize() {
        XCTAssertNil(HubTrust.normalize(nil))
        XCTAssertNil(HubTrust.normalize(""))
        XCTAssertNil(HubTrust.normalize("  "))
        XCTAssertEqual(HubTrust.normalize("AB:cd EF"), "abcdef")
        XCTAssertEqual(HubTrust.normalize(Self.caFingerprint.uppercased()), Self.caFingerprint)
        // Malformed input is kept (it matches nothing) rather than dropped (which would unpin).
        XCTAssertEqual(HubTrust.normalize("not-hex"), "not-hex")
    }

    func testPinnedCertificateFindsTheCAInTheChain() throws {
        let leaf = try certificate(leafDER)
        let ca = try certificate(caDER)
        let found = try XCTUnwrap(HubTrust.pinnedCertificate(in: [leaf, ca], caFingerprint: Self.caFingerprint))
        XCTAssertEqual(SecCertificateCopyData(found) as Data, caDER)

        let colonUpper = stride(from: 0, to: 64, by: 2).map { i -> String in
            let start = Self.caFingerprint.index(Self.caFingerprint.startIndex, offsetBy: i)
            return String(Self.caFingerprint[start ..< Self.caFingerprint.index(start, offsetBy: 2)]).uppercased()
        }.joined(separator: ":")
        XCTAssertNotNil(HubTrust.pinnedCertificate(in: [leaf, ca], caFingerprint: colonUpper))
    }

    func testPinnedCertificateRejectsOtherFingerprints() throws {
        let chain = [try certificate(leafDER), try certificate(caDER)]
        XCTAssertNil(HubTrust.pinnedCertificate(in: chain, caFingerprint: String(repeating: "0", count: 64)))
        XCTAssertNil(HubTrust.pinnedCertificate(in: chain, caFingerprint: ""))
        XCTAssertNil(HubTrust.pinnedCertificate(in: [try certificate(leafDER)], caFingerprint: Self.caFingerprint))
    }

    // MARK: Evaluation

    func testEvaluateAcceptsChainToPinnedCA() throws {
        let trust = try serverTrust(host: "nexus.test")
        let result = HubTrust.evaluate(trust, host: "nexus.test", caFingerprint: Self.caFingerprint, verifyDate: Self.verifyDate)
        XCTAssertEqual(try result.get(), caDER)
    }

    func testEvaluateAcceptsIPAddressHost() throws {
        let trust = try serverTrust(host: "192.168.1.10")
        let result = HubTrust.evaluate(trust, host: "192.168.1.10", caFingerprint: Self.caFingerprint, verifyDate: Self.verifyDate)
        XCTAssertNoThrow(try result.get())
    }

    func testEvaluateRejectsUnknownCA() throws {
        let trust = try serverTrust(host: "nexus.test")
        let result = HubTrust.evaluate(trust, host: "nexus.test", caFingerprint: String(repeating: "a", count: 64), verifyDate: Self.verifyDate)
        guard case .failure(.caMismatch) = result else { return XCTFail("expected caMismatch, got \(result)") }
    }

    func testEvaluateStillChecksTheHostName() throws {
        let trust = try serverTrust(host: "other.test")
        let result = HubTrust.evaluate(trust, host: "other.test", caFingerprint: Self.caFingerprint, verifyDate: Self.verifyDate)
        guard case .failure(.evaluationFailed) = result else { return XCTFail("expected evaluationFailed, got \(result)") }
    }

    func testEvaluateStillChecksDates() throws {
        let trust = try serverTrust(host: "nexus.test")
        let afterExpiry = Date(timeIntervalSince1970: 1_830_297_600) // 2028-01-01
        let result = HubTrust.evaluate(trust, host: "nexus.test", caFingerprint: Self.caFingerprint, verifyDate: afterExpiry)
        guard case .failure(.evaluationFailed) = result else { return XCTFail("expected evaluationFailed, got \(result)") }
    }

    func testEvaluateRejectsNearMissFingerprint() throws {
        // Only the exact scanned fingerprint is accepted.
        let trust = try serverTrust(host: "nexus.test")
        let result = HubTrust.evaluate(trust, host: "nexus.test", caFingerprint: Self.caFingerprint.replacingOccurrences(of: "1", with: "2"), verifyDate: Self.verifyDate)
        guard case .failure(.caMismatch) = result else { return XCTFail("expected caMismatch, got \(result)") }
    }

    func testRememberedCAOnlyReturnedForItsFingerprint() {
        HubTrust.remember(caDER: caDER, fingerprint: Self.caFingerprint)
        XCTAssertNotNil(HubTrust.rememberedCA(fingerprint: Self.caFingerprint))
        XCTAssertNil(HubTrust.rememberedCA(fingerprint: String(repeating: "0", count: 64)))
        // A DER that does not hash to the fingerprint is never stored.
        HubTrust.forgetRememberedCA()
        HubTrust.remember(caDER: leafDER, fingerprint: Self.caFingerprint)
        XCTAssertNil(HubTrust.rememberedCA(fingerprint: Self.caFingerprint))
    }

    func testEvaluateFallsBackToRememberedCAWhenServerOmitsIt() throws {
        HubTrust.remember(caDER: caDER, fingerprint: Self.caFingerprint)
        var trust: SecTrust?
        let leafOnly = [try certificate(leafDER)]
        XCTAssertEqual(SecTrustCreateWithCertificates(leafOnly as CFArray, SecPolicyCreateSSL(true, "nexus.test" as CFString), &trust), errSecSuccess)
        let result = HubTrust.evaluate(try XCTUnwrap(trust), host: "nexus.test", caFingerprint: Self.caFingerprint, verifyDate: Self.verifyDate)
        XCTAssertNoThrow(try result.get())
    }

    // MARK: Error mapping

    func testTrustRelatedErrors() {
        XCTAssertTrue(HubTrust.isTrustRelated(URLError(.cancelled)))
        XCTAssertTrue(HubTrust.isTrustRelated(URLError(.serverCertificateUntrusted)))
        XCTAssertFalse(HubTrust.isTrustRelated(URLError(.timedOut)))
        XCTAssertFalse(HubTrust.isTrustRelated(NSError(domain: "other", code: NSURLErrorCancelled)))
    }

    func testDelegateMapsCancelledToTrustErrorOnlyAfterAPinFailure() throws {
        let delegate = HubTrustDelegate(caFingerprint: Self.caFingerprint)
        let url = try XCTUnwrap(URL(string: "https://nexus.test:7700/healthz"))
        XCTAssertNil(delegate.trustError(for: URLError(.cancelled), url: url))

        delegate.failures.record(host: "nexus.test", failure: .caMismatch)
        XCTAssertEqual(delegate.trustError(for: URLError(.cancelled), url: url), HubTrustError(host: "nexus.test", failure: .caMismatch))
        XCTAssertNil(delegate.trustError(for: URLError(.timedOut), url: url))

        delegate.failures.record(host: "NEXUS.test", failure: nil)
        XCTAssertNil(delegate.trustError(for: URLError(.cancelled), url: url))
    }

    func testDelegateNormalizesFingerprint() {
        XCTAssertNil(HubTrustDelegate(caFingerprint: nil).caFingerprint)
        XCTAssertNil(HubTrustDelegate(caFingerprint: "").caFingerprint)
        XCTAssertEqual(HubTrustDelegate(caFingerprint: Self.caFingerprint.uppercased()).caFingerprint, Self.caFingerprint)
    }

    // MARK: Probe descriptions

    func testDescribePinMismatch() {
        let error = HubTrustError(host: "nexus.test", failure: .caMismatch)
        XCTAssertEqual(HubProbe.describe(error), "The Hub's certificate doesn't match the one you scanned.")
    }

    func testDescribeKeepsTimeoutDistinct() {
        XCTAssertEqual(HubProbe.describe(URLError(.timedOut)), "Your Nexus did not answer in time.")
        XCTAssertNotEqual(HubProbe.describe(URLError(.timedOut)), HubProbe.describe(URLError(.serverCertificateUntrusted)))
    }

    func testDescribeInvalidCertificateForHost() {
        let error = HubTrustError(host: "nexus.test", failure: .evaluationFailed(reason: "x"))
        XCTAssertTrue(HubProbe.describe(error).contains("nexus.test"))
    }

    func testMostInformativePrefersCertificateFailureOverLaterTimeout() {
        let errors: [Error] = [
            HubTrustError(host: "nexus.home.arpa", failure: .caMismatch),
            URLError(.timedOut),
        ]
        XCTAssertEqual(HubProbe.describe(HubProbe.mostInformative(errors)), HubTrust.mismatchMessage)
    }

    func testMostInformativeOrdering() {
        func best(_ errors: [Error]) -> String { HubProbe.describe(HubProbe.mostInformative(errors)) }
        XCTAssertEqual(best([URLError(.timedOut), URLError(.cannotFindHost)]), HubProbe.describe(URLError(.cannotFindHost)))
        XCTAssertEqual(best([URLError(.cannotFindHost), URLError(.cannotConnectToHost)]), HubProbe.describe(URLError(.cannotConnectToHost)))
        XCTAssertEqual(best([URLError(.timedOut), URLError(.serverCertificateUntrusted)]), HubProbe.describe(URLError(.serverCertificateUntrusted)))
        XCTAssertEqual(best([URLError(.timedOut), URLError(.timedOut)]), "Your Nexus did not answer in time.")
        XCTAssertEqual(best([]), "Your Nexus did not answer.")
    }

    func testFirstReachableWithoutURLs() async {
        guard case .unreachable(let reason) = await HubProbe.firstReachable([], path: nil, caFingerprint: Self.caFingerprint) else {
            return XCTFail("expected unreachable")
        }
        XCTAssertEqual(reason, "No Hub address configured.")
    }

    func testFirstReachableOffline() async {
        let outcome = await HubProbe.firstReachable(
            [URL(string: "https://nexus.test")!],
            path: NetworkPathInfo(isSatisfied: false),
            caFingerprint: Self.caFingerprint
        )
        guard case .offline = outcome else { return XCTFail("expected offline") }
    }
}
