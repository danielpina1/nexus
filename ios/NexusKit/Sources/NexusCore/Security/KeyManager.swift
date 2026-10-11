import CryptoKit
import Foundation
import LocalAuthentication
import Security

/// A P-256 signing key that is either inside the Secure Enclave or (Simulator only) in software.
public enum SigningKey {
    case secureEnclave(SecureEnclave.P256.Signing.PrivateKey)
    /// Software key — ONLY used when the Secure Enclave is unavailable (Simulator).
    case software(P256.Signing.PrivateKey)

    public var publicKey: P256.Signing.PublicKey {
        switch self {
        case .secureEnclave(let key): return key.publicKey
        case .software(let key): return key.publicKey
        }
    }

    public var isHardwareBacked: Bool {
        if case .secureEnclave = self { return true }
        return false
    }

    /// ECDSA P-256 over SHA-256(data).
    public func signature(for data: Data) throws -> P256.Signing.ECDSASignature {
        switch self {
        case .secureEnclave(let key): return try key.signature(for: data)
        case .software(let key): return try key.signature(for: data)
        }
    }
}

/// Public halves of the device keys, encoded for the wire (see `WireFormat`).
public struct DevicePublicKeys: Sendable, Equatable {
    public var auth: Data
    public var approval: Data
    public var encryption: Data
}

/// Owns the three device keys (SECURITY.md §2):
///
/// | Key        | Where                                          | Use                              |
/// |------------|------------------------------------------------|----------------------------------|
/// | auth       | Secure Enclave, no biometry                    | session auth, request signing,   |
/// |            |                                                | `approve`-level decisions        |
/// | approval   | Secure Enclave, `.biometryCurrentSet`          | biometric approvals, owner acts  |
/// | encryption | Keychain, Curve25519 (X25519), ThisDeviceOnly  | HPKE envelopes / notifications   |
///
/// Secure Enclave private keys never leave the chip; we persist only the opaque, device-bound
/// `dataRepresentation` blob in the Keychain (shared access group so extensions can use it).
///
/// SIMULATOR FALLBACK: when `SecureEnclave.isAvailable == false` the signing keys are plain
/// software keys stored in the Keychain. `isHardwareBacked` reports this and the UI shows a
/// warning; never ship a build that relies on it.
public final class KeyManager: @unchecked Sendable {
    public static let shared = KeyManager()

    private let lock = NSLock()

    /// Storage tags (first byte of the keychain blob).
    private enum Tag: UInt8 {
        case secureEnclave = 0x01
        case software = 0x02
    }

    public enum KeyError: Error, LocalizedError {
        case accessControl
        case corrupt(String)

        public var errorDescription: String? {
            switch self {
            case .accessControl: return "Could not create the Secure Enclave access control."
            case .corrupt(let what): return "Stored \(what) key is unreadable."
            }
        }
    }

    public init() {}

    public var isHardwareBacked: Bool { SecureEnclave.isAvailable }

    public var hasKeys: Bool {
        Keychain.data(for: .deviceAuthKey) != nil
            && Keychain.data(for: .approvalKey) != nil
            && Keychain.data(for: .encryptionKey) != nil
    }

    /// Creates any missing keys and returns the encoded public keys.
    @discardableResult
    public func ensureKeys() throws -> DevicePublicKeys {
        lock.lock()
        defer { lock.unlock() }
        let auth = try loadOrCreateSigningKey(item: .deviceAuthKey, biometric: false)
        let approval = try loadOrCreateSigningKey(item: .approvalKey, biometric: true)
        let encryption = try loadOrCreateEncryptionKey()
        return DevicePublicKeys(
            auth: WireFormat.encodeP256PublicKey(auth.publicKey),
            approval: WireFormat.encodeP256PublicKey(approval.publicKey),
            encryption: WireFormat.encodeX25519PublicKey(encryption.publicKey)
        )
    }

    /// Deletes all keys and regenerates them (used when re-pairing).
    @discardableResult
    public func regenerateKeys() throws -> DevicePublicKeys {
        resetAll()
        return try ensureKeys()
    }

    public func resetAll() {
        Keychain.delete(.deviceAuthKey)
        Keychain.delete(.approvalKey)
        Keychain.delete(.encryptionKey)
    }

    /// Device auth key (no user presence required).
    public func authKey() throws -> SigningKey {
        lock.lock()
        defer { lock.unlock() }
        return try loadOrCreateSigningKey(item: .deviceAuthKey, biometric: false)
    }

    /// Biometry-bound approval key. Pass an `LAContext` that has already evaluated a biometric
    /// policy to avoid a second prompt; otherwise the system prompts during signing.
    public func approvalKey(context: LAContext? = nil) throws -> SigningKey {
        lock.lock()
        defer { lock.unlock() }
        return try loadOrCreateSigningKey(item: .approvalKey, biometric: true, context: context)
    }

    public func encryptionKey() throws -> Curve25519.KeyAgreement.PrivateKey {
        lock.lock()
        defer { lock.unlock() }
        return try loadOrCreateEncryptionKey()
    }

    // MARK: Private

    private func loadOrCreateSigningKey(item: Keychain.Item, biometric: Bool, context: LAContext? = nil) throws -> SigningKey {
        if let stored = Keychain.data(for: item), let first = stored.first {
            let body = Data(stored.dropFirst())
            switch Tag(rawValue: first) {
            case .secureEnclave:
                return .secureEnclave(try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: body, authenticationContext: context))
            case .software:
                return .software(try P256.Signing.PrivateKey(rawRepresentation: body))
            case .none:
                throw KeyError.corrupt(item.rawValue)
            }
        }

        let key: SigningKey
        var blob = Data()
        if SecureEnclave.isAvailable {
            let seKey = try makeSecureEnclaveKey(biometric: biometric)
            key = .secureEnclave(seKey)
            blob.append(Tag.secureEnclave.rawValue)
            blob.append(seKey.dataRepresentation)
        } else {
            let swKey = P256.Signing.PrivateKey()
            key = .software(swKey)
            blob.append(Tag.software.rawValue)
            blob.append(swKey.rawRepresentation)
        }
        try Keychain.set(blob, for: item)
        return key
    }

    private func makeSecureEnclaveKey(biometric: Bool) throws -> SecureEnclave.P256.Signing.PrivateKey {
        if biometric {
            // Invalidated if the Face ID enrolment changes.
            if let access = Self.accessControl(
                protection: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
                flags: [.privateKeyUsage, .biometryCurrentSet]
            ), let key = try? SecureEnclave.P256.Signing.PrivateKey(accessControl: access) {
                return key
            }
            // No biometry enrolled: fall back to user presence (Face ID / Touch ID / passcode).
            guard let access = Self.accessControl(
                protection: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
                flags: [.privateKeyUsage, .userPresence]
            ) else { throw KeyError.accessControl }
            return try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        } else {
            guard let access = Self.accessControl(
                protection: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
                flags: [.privateKeyUsage]
            ) else { throw KeyError.accessControl }
            return try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        }
    }

    private static func accessControl(protection: CFString, flags: SecAccessControlCreateFlags) -> SecAccessControl? {
        var error: Unmanaged<CFError>?
        let access = SecAccessControlCreateWithFlags(kCFAllocatorDefault, protection, flags, &error)
        return access
    }

    private func loadOrCreateEncryptionKey() throws -> Curve25519.KeyAgreement.PrivateKey {
        if let stored = Keychain.data(for: .encryptionKey) {
            return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: stored)
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        try Keychain.set(key.rawRepresentation, for: .encryptionKey)
        return key
    }
}
