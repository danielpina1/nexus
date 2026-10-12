import Foundation

/// Non-secret description of the paired Hub, shared through the App Group so widgets,
/// intents and the notification extensions can reach the same Hub.
///
/// Architecture rule: every target talks ONLY to the Hub API (and receives APNs pushes that
/// originate from the Hub). Nothing here ever points at a node, LLM or third-party service.
public struct HubConfiguration: Codable, Equatable, Sendable {
    /// Hub base URLs from the pairing QR in preference order, e.g. `https://nexus.home.arpa:7700`
    /// (the same URL works on home Wi-Fi and through the UniFi VPN — docs/NETWORK.md).
    public var hubURLs: [URL]
    /// Lowercase hex SHA-256 of the Hub CA certificate (DER), from the pairing QR.
    public var caFingerprint: String?
    /// Device id assigned by the Hub at pairing (`Device.id`).
    public var deviceID: String?
    public var deviceName: String
    public var pairedAt: Date?
    /// Safety code that was confirmed at pairing time (shown in Settings → Trust).
    public var safetyCode: String?
    /// Use the protobuf binary codec (default) or JSON (handy when debugging with a proxy).
    public var useJSONCodec: Bool

    public init(
        hubURLs: [URL],
        caFingerprint: String? = nil,
        deviceID: String? = nil,
        deviceName: String = "iPhone",
        pairedAt: Date? = nil,
        safetyCode: String? = nil,
        useJSONCodec: Bool = false
    ) {
        self.hubURLs = hubURLs
        self.caFingerprint = caFingerprint
        self.deviceID = deviceID
        self.deviceName = deviceName
        self.pairedAt = pairedAt
        self.safetyCode = safetyCode
        self.useJSONCodec = useJSONCodec
    }

    public var primaryURL: URL? { hubURLs.first }

    /// All Hub URLs from the pairing QR (`hub=` is repeatable), the one that answered last
    /// first. HubClient and the reachability probe try them in this order.
    public var orderedURLs: [URL] {
        guard let last = HubRouteMemory.lastWorkingURL, hubURLs.contains(last) else { return hubURLs }
        return [last] + hubURLs.filter { $0 != last }
    }

    /// The URL to use before anything was probed (widgets, intents, extensions).
    public var preferredURL: URL? { orderedURLs.first }

    public var isPaired: Bool { deviceID != nil || Keychain.string(for: .devToken) != nil }

    // MARK: Persistence (App Group defaults)

    public static func load() -> HubConfiguration? {
        guard let data = AppGroup.defaults.data(forKey: AppGroup.Key.hubConfiguration) else { return nil }
        return try? JSONDecoder().decode(HubConfiguration.self, from: data)
    }

    public func save() {
        if let data = try? JSONEncoder().encode(self) {
            AppGroup.defaults.set(data, forKey: AppGroup.Key.hubConfiguration)
        }
    }

    public static func clear() {
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.hubConfiguration)
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.lastEventSeq)
        HubRouteMemory.clear()
        Outbox.clear()
        ApprovalCache.clear()
    }
}
