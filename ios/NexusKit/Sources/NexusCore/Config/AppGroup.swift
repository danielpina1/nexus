import Foundation

/// Shared App Group storage used by the app, widgets, intents and notification extensions.
///
/// The group identifier is read from each target's Info.plist key `NexusAppGroup`
/// (set to `$(NEXUS_APP_GROUP)` from `Config/Nexus.xcconfig`), falling back to the placeholder.
public enum AppGroup {
    public static let fallbackIdentifier = "group.com.example.nexus"

    public static var identifier: String {
        if let value = Bundle.main.object(forInfoDictionaryKey: "NexusAppGroup") as? String,
           !value.isEmpty, !value.hasPrefix("$(") {
            return value
        }
        return fallbackIdentifier
    }

    /// Shared defaults. Falls back to `.standard` if the App Group is not provisioned
    /// (e.g. running unsigned in the Simulator without the entitlement).
    public static var defaults: UserDefaults {
        UserDefaults(suiteName: identifier) ?? .standard
    }

    /// Shared container directory (nil when the App Group entitlement is missing).
    public static var containerURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: identifier)
    }

    /// Keys used in the shared defaults. Kept in one place so all targets agree.
    public enum Key {
        public static let hubConfiguration = "nexus.hub.configuration"
        public static let lastEventSeq = "nexus.events.lastSeq"
        public static let demoMode = "nexus.demoMode"
        public static let appLockEnabled = "nexus.appLock.enabled"
        public static let widgetSnapshot = "nexus.widget.snapshot"
        public static let pendingAction = "nexus.pendingAction"
        public static let pushEnvironment = "nexus.push.environment"
        public static let lockScreenDetails = "nexus.privacy.lockScreenDetails"
        // Remote access (NETWORK.md): which Hub URL answered last, and when.
        public static let lastWorkingHubURL = "nexus.hub.lastWorkingURL"
        public static let lastHubContact = "nexus.hub.lastContact"
        public static let lastHubContactViaVPN = "nexus.hub.lastContactViaVPN"
        /// Commands, replies, captures and signed decisions waiting for the Hub.
        public static let outbox = "nexus.outbox"
        /// Pending approvals (serialized protobuf) so the approval sheet works while away.
        public static let approvalCache = "nexus.approvals.cache"
    }
}

/// An action requested from a widget / control / intent that must be completed in the app UI
/// (e.g. the kill switch confirmation or quick capture sheet).
public enum PendingAction: String, Codable, Sendable {
    case killSwitch
    case capture
    case askNexus

    public static func set(_ action: PendingAction) {
        AppGroup.defaults.set(action.rawValue, forKey: AppGroup.Key.pendingAction)
    }

    /// Returns and clears the pending action.
    public static func take() -> PendingAction? {
        let defaults = AppGroup.defaults
        guard let raw = defaults.string(forKey: AppGroup.Key.pendingAction) else { return nil }
        defaults.removeObject(forKey: AppGroup.Key.pendingAction)
        return PendingAction(rawValue: raw)
    }
}
