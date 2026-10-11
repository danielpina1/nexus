import CryptoKit
import XCTest
@testable import NexusCore
import NexusAPI
import SwiftProtobuf

final class WireFormatTests: XCTestCase {
    func testApprovalMessageLayout() {
        let message = ApprovalSignature.message(
            approvalID: "apr_1",
            approve: true,
            challenge: Data([0xAA, 0xBB]),
            actionSHA256: Data(repeating: 0x11, count: 32),
            expiresAtUnixSeconds: 0x0102030405060708
        )
        var expected = Data("apr_1".utf8)
        expected.append(0x01)
        expected.append(contentsOf: [0xAA, 0xBB])
        expected.append(Data(repeating: 0x11, count: 32))
        expected.append(contentsOf: [0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08])
        XCTAssertEqual(message, expected)
    }

    func testDenyByte() {
        let message = ApprovalSignature.message(approvalID: "", approve: false, challenge: Data(), actionSHA256: Data(), expiresAtUnixSeconds: 1)
        XCTAssertEqual(message, Data([0x00, 0, 0, 0, 0, 0, 0, 0, 1]))
    }

    func testPairingPayloadParsing() throws {
        let payload = try PairingPayload(string: "nexus://pair?hub=http%3A%2F%2F192.168.1.10%3A7700&hub=https://hub.ts.net&ca=ABCD&secret=s3cr3t")
        XCTAssertEqual(payload.hubURLs.map(\.absoluteString), ["http://192.168.1.10:7700", "https://hub.ts.net"])
        XCTAssertEqual(payload.caFingerprint, "abcd")
        XCTAssertEqual(payload.secret, "s3cr3t")
        XCTAssertEqual(payload.token, "s3cr3t")
    }

    func testPairingProofIsDeterministic() {
        let keys = DevicePublicKeys(auth: Data(repeating: 1, count: 65), approval: Data(repeating: 2, count: 65), encryption: Data(repeating: 3, count: 32))
        let a = PairingService.pairingProof(secret: "secret", keys: keys)
        let b = PairingService.pairingProof(secret: "secret", keys: keys)
        XCTAssertEqual(a, b)
        XCTAssertEqual(a.count, 32)
        XCTAssertNotEqual(a, PairingService.pairingProof(secret: "other", keys: keys))
    }

    func testEnvelopeRoundTrip() throws {
        let recipient = Curve25519.KeyAgreement.PrivateKey()
        var header = Nexus_V1_EnvelopeHeader()
        header.purpose = .notification
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
    }
}
