import Foundation
import Security

/// Minimal generic-password Keychain wrapper.
///
/// - Items are `ThisDeviceOnly` (never synced to iCloud, never in backups restored elsewhere).
/// - Items live in the shared access group (the App Group identifier, which iOS accepts as a
///   keychain access group) so the widgets / intents / notification extensions can read them.
/// - Accessibility is `AfterFirstUnlockThisDeviceOnly` so background work (notification service
///   extension, Live Activity intents) still works while the device is locked after first unlock.
public enum Keychain {
    public enum Item: String, CaseIterable, Sendable {
        case deviceAuthKey = "key.device-auth"
        case approvalKey = "key.approval"
        case encryptionKey = "key.encryption"
        case devToken = "token.dev"
        case accessToken = "token.access"
        case accessTokenExpiry = "token.access.expiry"
        case clientCertificate = "cert.client"
    }

    static let service = "com.example.nexus.keys"

    public enum KeychainError: Error, CustomStringConvertible {
        case status(OSStatus)
        public var description: String {
            let message = SecCopyErrorMessageString(statusCode(), nil) as String? ?? "unknown"
            return "Keychain error \(statusCode()): \(message)"
        }
        private func statusCode() -> OSStatus {
            switch self { case .status(let s): return s }
        }
    }

    private static func baseQuery(_ item: Item, accessGroup: String?) -> [String: Any] {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: item.rawValue,
        ]
        if let accessGroup {
            query[kSecAttrAccessGroup as String] = accessGroup
        }
        return query
    }

    public static func set(_ data: Data, for item: Item) throws {
        do {
            try set(data, for: item, accessGroup: AppGroup.identifier)
        } catch KeychainError.status(let status) where status == errSecMissingEntitlement {
            // Unsigned Simulator builds have no access group entitlement; keep it app-local.
            try set(data, for: item, accessGroup: nil)
        }
    }

    private static func set(_ data: Data, for item: Item, accessGroup: String?) throws {
        let query = baseQuery(item, accessGroup: accessGroup)
        SecItemDelete(query as CFDictionary)
        var attributes = query
        attributes[kSecValueData as String] = data
        attributes[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(attributes as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.status(status) }
    }

    public static func data(for item: Item) -> Data? {
        if let data = data(for: item, accessGroup: AppGroup.identifier) { return data }
        return data(for: item, accessGroup: nil)
    }

    private static func data(for item: Item, accessGroup: String?) -> Data? {
        var query = baseQuery(item, accessGroup: accessGroup)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess else { return nil }
        return result as? Data
    }

    public static func delete(_ item: Item) {
        SecItemDelete(baseQuery(item, accessGroup: AppGroup.identifier) as CFDictionary)
        SecItemDelete(baseQuery(item, accessGroup: nil) as CFDictionary)
    }

    public static func setString(_ value: String?, for item: Item) {
        if let value, let data = value.data(using: .utf8) {
            try? set(data, for: item)
        } else {
            delete(item)
        }
    }

    public static func string(for item: Item) -> String? {
        guard let data = data(for: item) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public static func deleteAll() {
        for item in Item.allCases { delete(item) }
    }
}
