import Foundation

/// Small Codable snapshot written by the app into the App Group for widgets.
/// Contains no secrets and only short strings (it is visible on the Home / Lock Screen).
public struct WidgetSnapshot: Codable, Equatable, Sendable {
    public struct Machine: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var name: String
        public var online: Bool
        public var cpuPercent: Double
        public var activeRuns: Int
    }

    public struct RunningTask: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
        public var agentSymbol: String
        public var waitingForYou: Bool
    }

    public struct Workflow: Codable, Equatable, Sendable, Identifiable {
        public var id: String          // agent or workflow id
        public var name: String
        public var symbol: String
        public var prompt: String      // what to run
    }

    public var updatedAt: Date
    public var pendingApprovals: Int
    public var openQuestions: Int
    public var inboxCount: Int
    public var runningCount: Int
    public var costCentsToday: Int
    public var machines: [Machine]
    public var running: [RunningTask]
    public var favourites: [Workflow]
    public var lastResult: String?

    public init(
        updatedAt: Date = Date(),
        pendingApprovals: Int = 0,
        openQuestions: Int = 0,
        inboxCount: Int = 0,
        runningCount: Int = 0,
        costCentsToday: Int = 0,
        machines: [Machine] = [],
        running: [RunningTask] = [],
        favourites: [Workflow] = [],
        lastResult: String? = nil
    ) {
        self.updatedAt = updatedAt
        self.pendingApprovals = pendingApprovals
        self.openQuestions = openQuestions
        self.inboxCount = inboxCount
        self.runningCount = runningCount
        self.costCentsToday = costCentsToday
        self.machines = machines
        self.running = running
        self.favourites = favourites
        self.lastResult = lastResult
    }

    public static func load() -> WidgetSnapshot? {
        guard let data = AppGroup.defaults.data(forKey: AppGroup.Key.widgetSnapshot) else { return nil }
        return try? JSONDecoder().decode(WidgetSnapshot.self, from: data)
    }

    public func save() {
        if let data = try? JSONEncoder().encode(self) {
            AppGroup.defaults.set(data, forKey: AppGroup.Key.widgetSnapshot)
        }
    }

    public static let placeholder = WidgetSnapshot(
        pendingApprovals: 1,
        openQuestions: 1,
        inboxCount: 3,
        runningCount: 2,
        costCentsToday: 42,
        machines: [
            Machine(id: "node_mac", name: "mac-mini", online: true, cpuPercent: 23, activeRuns: 2),
            Machine(id: "node_srv", name: "server", online: true, cpuPercent: 8, activeRuns: 0),
            Machine(id: "node_mbp", name: "macbook", online: false, cpuPercent: 0, activeRuns: 0),
        ],
        running: [
            RunningTask(id: "tsk_1", title: "Fix flaky tests", agentSymbol: "hammer.fill", waitingForYou: false),
            RunningTask(id: "tsk_2", title: "Weekly groceries", agentSymbol: "cart.fill", waitingForYou: true),
        ],
        favourites: [
            Workflow(id: "wf_brief", name: "Brief", symbol: "sun.max.fill", prompt: "Give me my morning brief"),
            Workflow(id: "wf_groceries", name: "Groceries", symbol: "cart.fill", prompt: "Order the weekly groceries"),
            Workflow(id: "wf_inbox", name: "Inbox zero", symbol: "tray.fill", prompt: "Triage my email"),
            Workflow(id: "wf_backup", name: "Backup", symbol: "externaldrive.fill", prompt: "Run the backup workflow"),
        ],
        lastResult: "Tests fixed — PR #42 ready"
    )
}
