import CryptoKit
import XCTest
@testable import NexusCore
import NexusAPI
import SwiftProtobuf

/// Golden values produced by the Go backend
/// (backend/internal/security/vectors_test.go → docs/test-vectors/wire-v1.json).
/// If one of these fails, the iOS and backend byte formats have diverged.
final class CrossLanguageVectorTests: XCTestCase {
    private let payloadJSON = #"{"total":84.3,"currency":"EUR","items":[{"name":"Leite Mimosa","qty":6,"price":0.89},{"name":"Pão","qty":1,"price":1e-7}],"slot":"Sat 9–11","big":1e21,"neg":-0.5,"esc":"a\"b\\c\n\u0001/é"}"#
    private let canonicalJSON = #"{"big":1e+21,"currency":"EUR","esc":"a\"b\\c\n\u0001/é","items":[{"name":"Leite Mimosa","price":0.89,"qty":6},{"name":"Pão","price":1e-7,"qty":1}],"neg":-0.5,"slot":"Sat 9–11","total":84.3}"#
    private let actionHashHex = "605e38f33e2e787ba7c2ad83b0a0abf01e88546f26e60de5571aa0df9d2ddf96"
    private let pairingProofHex = "3a2a556b402497409776592047527f7d826f78de268822f93e66df99ea2eeef3"
    private let safetyCode = "🐯 🐺 🐊 🐤 🦁 🐭"

    private var auth: Data { Data([4]) + Data(repeating: 1, count: 64) }
    private var approval: Data { Data([4]) + Data(repeating: 2, count: 64) }
    private var encryption: Data { Data(repeating: 3, count: 32) }
    private var hub: Data { Data([4]) + Data(repeating: 5, count: 64) }

    func testCanonicalJSONMatchesBackend() throws {
        let payload = try Google_Protobuf_Struct(jsonString: payloadJSON)
        XCTAssertEqual(try CanonicalJSON.canonicalize(payload), canonicalJSON)
    }

    func testActionHashMatchesBackend() throws {
        let digest = SHA256.hash(data: Data(canonicalJSON.utf8))
        XCTAssertEqual(Data(digest).map { String(format: "%02x", $0) }.joined(), actionHashHex)
    }

    func testPairingProofMatchesBackend() {
        let keys = DevicePublicKeys(auth: auth, approval: approval, encryption: encryption)
        let proof = PairingService.pairingProof(secret: Data(repeating: 7, count: 32), keys: keys)
        XCTAssertEqual(proof.map { String(format: "%02x", $0) }.joined(), pairingProofHex)
    }

    func testSafetyCodeMatchesBackend() {
        XCTAssertEqual(WireFormat.safetyCode(publicKeys: [hub, auth, approval, encryption]), safetyCode)
    }
}
