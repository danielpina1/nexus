import CryptoKit
import XCTest
@testable import NexusCore
import NexusAPI
import SwiftProtobuf

/// Byte-layout tests for docs/WIRE_FORMATS.md. Cross-check vectors with
/// backend/internal/security/*_test.go when changing anything here.
final class WireFormatTests: XCTestCase {
    func testApprovalMessageLayout() {
        let message = ApprovalSignature.message(
            approvalID: "apr_1",
            approve: true,
            challenge: Data([0xAA, 0xBB]),
            actionSHA256: Data(repeating: 0x11, count: 32),
            expiresAtUnixSeconds: 0x0102030405060708
        )
        var expected = Data("nexus/v1/approval".utf8)
        expected.append(0x00)
        expected.append(contentsOf: [0, 0, 0, 5])
        expected.append(Data("apr_1".utf8))
        expected.append(0x01)
        expected.append(contentsOf: [0, 0, 0, 2, 0xAA, 0xBB])
        expected.append(Data(repeating: 0x11, count: 32))
        expected.append(contentsOf: [0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08])
        XCTAssertEqual(message, expected)
    }

    func testAuthenticateMessageLayout() {
        let message = WireFormat.authenticationMessage(deviceID: "dev", challenge: Data([9]))
        var expected = Data("nexus/v1/authenticate".utf8)
        expected.append(0)
        expected.append(contentsOf: [0, 0, 0, 3])
        expected.append(Data("dev".utf8))
        expected.append(contentsOf: [0, 0, 0, 1, 9])
        XCTAssertEqual(message, expected)
    }

    func testRequestSigningString() {
        let s = WireFormat.requestSigningString(method: "post", target: "/a?b=1", body: Data(), timestamp: 10, nonce: "n", deviceID: "d", counter: nil)
        XCTAssertEqual(s, "POST\n/a?b=1\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n10\nn\nd\n")
    }

    func testCanonicalJSON() throws {
        let s = try Google_Protobuf_Struct(jsonString: #"{"b":1.50,"a":[true,null,"x\n"],"c":{"z":100,"y":1e-7}}"#)
        XCTAssertEqual(try CanonicalJSON.canonicalize(s), #"{"a":[true,null,"x\n"],"b":1.5,"c":{"y":1e-7,"z":100}}"#)
    }

    func testPairingPayloadParsing() throws {
        let secret = Data(repeating: 7, count: 32)
        let token = WireFormat.base64URL(secret)
        let url = "nexus://pair?v=1&k=device&hub=http%3A%2F%2F192.168.1.10%3A7700&hub=https://hub.ts.net&ca=\(String(repeating: "ab", count: 32))&s=\(token)&hk=AQ&exp=0"
        let payload = try PairingPayload(string: url)
        XCTAssertEqual(payload.hubURLs.map(\.absoluteString), ["http://192.168.1.10:7700", "https://hub.ts.net"])
        XCTAssertEqual(payload.secret, secret)
        XCTAssertEqual(payload.token, token)
        XCTAssertEqual(payload.hubKey, Data([1]))
    }

    func testSafetyCodeShape() {
        let code = WireFormat.safetyCode(publicKeys: [Data([2]), Data([1]), Data()])
        XCTAssertEqual(code.split(separator: " ").count, 6)
        XCTAssertEqual(code, WireFormat.safetyCode(publicKeys: [Data([1]), Data([2])]))
        XCTAssertEqual(WireFormat.safetyCodeEmojis.count, 64)
    }

    func testPairingProofIsDeterministic() {
        let keys = DevicePublicKeys(auth: Data(repeating: 1, count: 65), approval: Data(repeating: 2, count: 65), encryption: Data(repeating: 3, count: 32))
        let secret = Data(repeating: 9, count: 32)
        let a = PairingService.pairingProof(secret: secret, keys: keys)
        XCTAssertEqual(a, PairingService.pairingProof(secret: secret, keys: keys))
        XCTAssertEqual(a.count, 32)
    }

    func testEnvelopeRoundTrip() throws {
        let recipient = Curve25519.KeyAgreement.PrivateKey()
        var header = Nexus_V1_EnvelopeHeader()
        header.purpose = .notification
        header.nonce = Data((0..<16).map { _ in UInt8.random(in: 0...255) })
        let headerBytes: Data = try header.serializedBytes()
        var sender = try HPKE.Sender(recipientKey: recipient.publicKey, ciphersuite: EnvelopeFormat.ciphersuite, info: EnvelopeFormat.hpkeInfo)
        let ciphertext = try sender.seal(Data(#"{"title":"T","body":"B"}"#.utf8), authenticating: headerBytes)
        var envelope = Nexus_V1_SealedEnvelope()
        envelope.header = headerBytes
        envelope.encapsulatedKey = sender.encapsulatedKey
        envelope.ciphertext = ciphertext
        let opened = try EnvelopeCrypto.open(envelope, expectedPurpose: .notification, recipientKey: recipient)
        let content = try JSONDecoder().decode(NotificationPlaintext.self, from: opened.plaintext)
        XCTAssertEqual(content.title, "T")
        XCTAssertEqual(content.body, "B")
        XCTAssertThrowsError(try EnvelopeCrypto.open(envelope, expectedPurpose: .notification, recipientKey: recipient))
    }
}
