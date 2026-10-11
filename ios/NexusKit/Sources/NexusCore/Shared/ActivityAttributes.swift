import ActivityKit
import Foundation

// Live Activity attributes shared by the app (starts / updates activities, registers push
// tokens) and the widget extension (renders them).
//
// The ContentState field names are the JSON keys of the Hub's `liveactivity` APNs payload
// (`aps.content-state`, IOS_LIVE.md §2.5) — keep them in sync with the backend.

/// Run activity — IOS_LIVE.md §2.3 (verbatim).
public struct RunActivityAttributes: ActivityAttributes {
    public struct ContentState: Codable, Hashable {
        public enum Phase: String, Codable { case running, waitingApproval, waitingInput, done, failed }
        public var phase: Phase
        public var title: String          // "Running tests"
        public var detail: String?        // "+42 −13 · 3 files" (redacted per privacy setting)
        public var progress: Double?      // 0...1, nil = indeterminate
        public var step: Int?
        public var totalSteps: Int?
        public var costCents: Int
        public var approvalId: String?
        public var approvalLevel: String? // "approve" | "biometric"
        public var choices: [String]?     // for simple questions

        public init(
            phase: Phase,
            title: String,
            detail: String? = nil,
            progress: Double? = nil,
            step: Int? = nil,
            totalSteps: Int? = nil,
            costCents: Int = 0,
            approvalId: String? = nil,
            approvalLevel: String? = nil,
            choices: [String]? = nil
        ) {
            self.phase = phase
            self.title = title
            self.detail = detail
            self.progress = progress
            self.step = step
            self.totalSteps = totalSteps
            self.costCents = costCents
            self.approvalId = approvalId
            self.approvalLevel = approvalLevel
            self.choices = choices
        }
    }

    public var taskId: String
    public var agentId: String
    public var agentName: String
    public var agentSymbol: String        // SF Symbol
    public var nodeName: String

    public init(taskId: String, agentId: String, agentName: String, agentSymbol: String, nodeName: String) {
        self.taskId = taskId
        self.agentId = agentId
        self.agentName = agentName
        self.agentSymbol = agentSymbol
        self.nodeName = nodeName
    }

    /// `activity_type` value for `DeviceService.RegisterLiveActivityToken`.
    public static let activityType = "run"
}

/// Aggregate "Pulse" activity — "3 running · 1 waiting for you · €0.42 today".
public struct PulseActivityAttributes: ActivityAttributes {
    public struct ContentState: Codable, Hashable {
        public var running: Int
        public var waiting: Int
        public var costCentsToday: Int
        public var headline: String?      // most relevant task title
        public var headlineSymbol: String?

        public init(running: Int, waiting: Int, costCentsToday: Int, headline: String? = nil, headlineSymbol: String? = nil) {
            self.running = running
            self.waiting = waiting
            self.costCentsToday = costCentsToday
            self.headline = headline
            self.headlineSymbol = headlineSymbol
        }
    }

    public var hubName: String

    public init(hubName: String) {
        self.hubName = hubName
    }

    public static let activityType = "pulse"
}

public extension RunActivityAttributes.ContentState {
    var isWaitingForUser: Bool { phase == .waitingApproval || phase == .waitingInput }
    var isBiometric: Bool { approvalLevel == "biometric" }

    var costText: String {
        (Decimal(costCents) / 100).formatted(.currency(code: "EUR"))
    }

    var stepText: String? {
        guard let step else { return nil }
        if let totalSteps { return "\(step)/\(totalSteps)" }
        return "\(step)"
    }
}

/// Deep links used by Live Activities, widgets and notifications.
public enum DeepLink {
    public static let scheme = "nexus"

    public static func approval(_ id: String) -> URL { URL(string: "nexus://approval/\(id)")! }
    public static func task(_ id: String) -> URL { URL(string: "nexus://task/\(id)")! }
    public static let inbox = URL(string: "nexus://inbox")!
    public static let capture = URL(string: "nexus://capture")!
    public static let machines = URL(string: "nexus://machines")!
    public static let killSwitch = URL(string: "nexus://killswitch")!
}
