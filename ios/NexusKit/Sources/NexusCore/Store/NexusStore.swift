import Foundation
import NexusAPI
import Observation
import SwiftProtobuf

/// The app's observable state: agents, nodes, tasks, inbox, per-task timelines and the
/// connection state — fed by `EventStream` and refreshed with unary Hub calls.
@Observable
@MainActor
public final class NexusStore {
    // MARK: State

    public private(set) var connection: ConnectionState = .disconnected
    public private(set) var configuration: HubConfiguration?
    public private(set) var isDemo: Bool

    public private(set) var agents: [Nexus_V1_Agent] = []
    public private(set) var nodes: [Nexus_V1_Node] = []
    public private(set) var nodeStatus: [String: Nexus_V1_NodeStatus] = [:]
    public private(set) var tasks: [String: Nexus_V1_Task] = [:]
    public private(set) var approvals: [String: Nexus_V1_Approval] = [:]
    public private(set) var questions: [String: QuestionItem] = [:]
    public private(set) var notices: [NoticeItem] = []
    public private(set) var timelines: [String: [TimelineItem]] = [:]
    public private(set) var devices: [Nexus_V1_Device] = []
    public var lastError: String?

    /// Called whenever a task changes (used by the Live Activity manager).
    @ObservationIgnored public var onTaskChanged: ((Nexus_V1_Task) -> Void)?
    /// Called for every approval event (used to refresh notifications / activities).
    @ObservationIgnored public var onApprovalChanged: ((Nexus_V1_Approval) -> Void)?

    @ObservationIgnored private var hub: HubClient?
    @ObservationIgnored private var stream: EventStream?
    @ObservationIgnored private var snapshotTask: Task<Void, Never>?
    private static let maxTimelineEntries = 500

    // MARK: Init

    public init(demo: Bool? = nil) {
        self.isDemo = demo ?? AppGroup.defaults.bool(forKey: AppGroup.Key.demoMode)
        self.configuration = HubConfiguration.load()
    }

    /// Preview store with mock data.
    public static func preview() -> NexusStore {
        let store = NexusStore(demo: true)
        store.loadDemoData()
        return store
    }

    public var isPaired: Bool { configuration?.isPaired ?? false }
    public var actions: NexusActions? { hub.map(NexusActions.init(hub:)) }

    // MARK: Derived

    public var inbox: [InboxItem] {
        var items: [InboxItem] = approvals.values.filter { $0.state == .pending }.map { .approval($0) }
        items += questions.values.map { .question($0) }
        items += notices.map { .notice($0) }
        return items.sorted { lhs, rhs in
            lhs.rank != rhs.rank ? lhs.rank < rhs.rank : lhs.date > rhs.date
        }
    }

    public var runningTasks: [Nexus_V1_Task] {
        tasks.values.filter { $0.state.isActive }.sorted { $0.updatedAt.date > $1.updatedAt.date }
    }

    public var doneTasks: [Nexus_V1_Task] {
        tasks.values.filter { $0.state.isTerminal }.sorted { $0.updatedAt.date > $1.updatedAt.date }
    }

    /// Tasks created by triggers / workflows that are queued for later.
    public var scheduledTasks: [Nexus_V1_Task] {
        tasks.values.filter { $0.state == .queued && ($0.source == "trigger" || $0.source == "workflow") }
            .sorted { $0.createdAt.date < $1.createdAt.date }
    }

    public var rootTasks: [Nexus_V1_Task] {
        tasks.values.filter { $0.parentID.isEmpty }.sorted { $0.createdAt.date > $1.createdAt.date }
    }

    public func children(of taskID: String) -> [Nexus_V1_Task] {
        tasks.values.filter { $0.parentID == taskID }.sorted { $0.createdAt.date < $1.createdAt.date }
    }

    public func chatTasks(forAgent agentID: String?) -> [Nexus_V1_Task] {
        rootTasks.filter { agentID == nil ? ($0.source == "ios-chat" || $0.source.isEmpty) : $0.agentID == agentID }
            .sorted { $0.createdAt.date < $1.createdAt.date }
    }

    public func agent(_ id: String) -> Nexus_V1_Agent? { agents.first { $0.id == id } }
    public func node(_ id: String) -> Nexus_V1_Node? { nodes.first { $0.id == id } }

    public func status(ofNode id: String) -> Nexus_V1_NodeStatus? {
        nodeStatus[id] ?? node(id).flatMap { $0.hasStatus ? $0.status : nil }
    }

    public var costCentsToday: Int {
        let start = Calendar.current.startOfDay(for: Date())
        return tasks.values.filter { $0.updatedAt.date >= start }.reduce(0) { $0 + $1.costCents }
    }

    public var pendingApprovals: [Nexus_V1_Approval] {
        approvals.values.filter { $0.state == .pending }.sorted { $0.createdAt.date > $1.createdAt.date }
    }

    // MARK: Lifecycle

    /// Connects to the paired Hub (or loads demo data) and starts the event stream.
    public func start() {
        if isDemo {
            if agents.isEmpty { loadDemoData() }
            return
        }
        if hub != nil && stream != nil { return }
        configuration = HubConfiguration.load()
        guard let configuration, configuration.primaryURL != nil else {
            connection = .disconnected
            return
        }
        do {
            let hub = try HubClient(configuration: configuration)
            self.hub = hub
            self.stream?.stop()
            let stream = EventStream(
                hub: hub,
                onEvent: { [weak self] event in self?.apply(event) },
                onState: { [weak self] state in self?.connection = state }
            )
            self.stream = stream
            stream.start()
            Task { await refresh() }
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// App returned to the foreground: reconnect a stalled stream right away.
    public func foreground() {
        start()
        if let stream, connection != .connected {
            stream.restart()
        }
    }

    public func stop() {
        stream?.stop()
        stream = nil
    }

    /// Re-reads configuration (after pairing / settings changes) and reconnects.
    public func reconnect() {
        stop()
        hub = nil
        start()
    }

    public func setDemoMode(_ enabled: Bool) {
        AppGroup.defaults.set(enabled, forKey: AppGroup.Key.demoMode)
        isDemo = enabled
        clearState()
        if enabled {
            stop()
            loadDemoData()
        } else {
            reconnect()
        }
    }

    /// Forgets the Hub and all keys (used by "Unpair").
    public func unpair() {
        stop()
        hub = nil
        HubConfiguration.clear()
        Keychain.deleteAll()
        KeyManager.shared.resetAll()
        configuration = nil
        clearState()
    }

    private func clearState() {
        agents = []
        nodes = []
        nodeStatus = [:]
        tasks = [:]
        approvals = [:]
        questions = [:]
        notices = []
        timelines = [:]
        devices = []
        connection = isDemo ? .demo : .disconnected
    }

    private func loadDemoData() {
        connection = .demo
        agents = MockData.agents
        nodes = MockData.nodes
        tasks = Dictionary(uniqueKeysWithValues: MockData.tasks.map { ($0.id, $0) })
        approvals = Dictionary(uniqueKeysWithValues: MockData.approvals.map { ($0.id, $0) })
        questions = Dictionary(uniqueKeysWithValues: MockData.questions.map { ($0.id, $0) })
        notices = MockData.notices
        devices = MockData.devices
        for task in MockData.tasks where timelines[task.id] == nil {
            timelines[task.id] = MockData.timeline(for: task.id)
        }
        scheduleSnapshot()
    }

    /// Fetches agents, nodes, tasks and pending approvals.
    public func refresh() async {
        guard !isDemo, let hub else { return }
        do {
            async let agentsResponse = hub.call { headers in
                await hub.agents.listAgents(request: Nexus_V1_ListAgentsRequest(), headers: headers)
            }
            async let nodesResponse = hub.call { headers in
                await hub.nodes.listNodes(request: Nexus_V1_ListNodesRequest(), headers: headers)
            }
            async let tasksResponse = hub.call { headers in
                var request = Nexus_V1_ListTasksRequest()
                var page = Nexus_V1_PageRequest()
                page.pageSize = 200
                request.page = page
                return await hub.tasks.listTasks(request: request, headers: headers)
            }
            async let approvalsResponse = NexusActions(hub: hub).pendingApprovals()

            let (a, n, t, p) = try await (agentsResponse, nodesResponse, tasksResponse, approvalsResponse)
            agents = a.agents
            nodes = n.nodes
            for node in n.nodes where node.hasStatus {
                nodeStatus[node.id] = node.status
            }
            for task in t.tasks {
                tasks[task.id] = task
            }
            approvals = Dictionary(p.map { ($0.id, $0) }, uniquingKeysWith: { _, new in new })
            lastError = nil
            scheduleSnapshot()
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// Loads a task with children (task detail).
    public func loadTask(_ id: String) async {
        guard !isDemo, let hub else { return }
        var request = Nexus_V1_GetTaskRequest()
        request.id = id
        request.includeChildren = true
        do {
            let response = try await hub.call { headers in
                await hub.tasks.getTask(request: request, headers: headers)
            }
            tasks[response.task.id] = response.task
            for child in response.children { tasks[child.id] = child }
        } catch {
            lastError = error.localizedDescription
        }
    }

    // MARK: Event application

    public func apply(_ event: Nexus_V1_Event) {
        let at = event.hasAt ? event.at.date : Date()
        let entryID = "evt_\(event.seq)"
        switch event.body {
        case .taskUpdated(let update):
            let task = update.task
            let previous = tasks[task.id]
            tasks[task.id] = task
            if previous?.state != task.state {
                append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .status(task.state, summary: task.summary)), to: task.id)
            }
            if task.state != .waitingInput && task.state != .needsInput {
                questions = questions.filter { $0.value.taskID != task.id }
            }
            onTaskChanged?(task)

        case .runOutput(let output):
            appendOutput(output, taskID: event.taskID, runID: event.runID, at: at, entryID: entryID)

        case .toolCall(let call):
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .toolCall(call, result: nil)), to: event.taskID)

        case .toolResult(let result):
            attach(result, taskID: event.taskID)

        case .question(let question):
            questions[question.questionID] = QuestionItem(taskID: event.taskID, question: question, at: at)
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .question(question)), to: event.taskID)

        case .approval(let approval):
            if approval.state == .pending {
                approvals[approval.id] = approval
            } else {
                approvals.removeValue(forKey: approval.id)
            }
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .approval(approval)), to: event.taskID)
            onApprovalChanged?(approval)

        case .cost(let cost):
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .cost(cost.cost)), to: event.taskID)

        case .artifact(let artifact):
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .artifact(artifact.attachment)), to: event.taskID)

        case .nodeStatus(let status):
            nodeStatus[status.nodeID] = status

        case .notice(let notice):
            notices.removeAll { $0.id == notice.id }
            notices.insert(NoticeItem(taskID: event.taskID, notice: notice, at: at), at: 0)
            if !event.taskID.isEmpty {
                append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .notice(notice)), to: event.taskID)
            }

        case .userMessage(let message):
            append(TimelineItem(id: entryID, at: at, runID: event.runID, kind: .userMessage(message.text)), to: event.taskID)

        case .none:
            break
        }
        scheduleSnapshot()
    }

    private func append(_ entry: TimelineItem, to taskID: String) {
        guard !taskID.isEmpty else { return }
        var entries = timelines[taskID] ?? []
        entries.append(entry)
        if entries.count > Self.maxTimelineEntries {
            entries.removeFirst(entries.count - Self.maxTimelineEntries)
        }
        timelines[taskID] = entries
    }

    /// Streaming text: merge consecutive chunks of the same run into one entry.
    private func appendOutput(_ output: Nexus_V1_RunOutput, taskID: String, runID: String, at: Date, entryID: String) {
        guard !taskID.isEmpty else { return }
        var entries = timelines[taskID] ?? []
        if let last = entries.last, last.runID == runID, case .output(let text, let isFinal) = last.kind, !isFinal {
            entries[entries.count - 1].kind = .output(text + output.text, isFinal: output.isFinal)
            timelines[taskID] = entries
        } else {
            append(TimelineItem(id: entryID, at: at, runID: runID, kind: .output(output.text, isFinal: output.isFinal)), to: taskID)
        }
    }

    private func attach(_ result: Nexus_V1_ToolResult, taskID: String) {
        guard var entries = timelines[taskID] else { return }
        for index in entries.indices.reversed() {
            if case .toolCall(let call, _) = entries[index].kind, call.callID == result.callID {
                entries[index].kind = .toolCall(call, result: result)
                timelines[taskID] = entries
                return
            }
        }
    }

    // MARK: Actions

    private func requireActions() throws -> NexusActions {
        if isDemo { throw HubError.demoMode }
        guard let hub else { throw HubError.notConfigured }
        return NexusActions(hub: hub)
    }

    /// Sends a command (Nexus thread → routed; agent thread → forced route).
    @discardableResult
    public func execute(_ input: String, attachments: [Nexus_V1_Attachment] = [], agentID: String? = nil, source: String = "ios-chat") async throws -> Nexus_V1_ExecuteResponse {
        if isDemo {
            var task = Nexus_V1_Task()
            task.id = "tsk_demo_\(UUID().uuidString.prefix(6))"
            task.title = input
            task.prompt = input
            task.agentID = agentID ?? ""
            task.state = .running
            task.source = source
            task.createdAt = Google_Protobuf_Timestamp(date: Date())
            task.updatedAt = task.createdAt
            tasks[task.id] = task
            timelines[task.id] = [TimelineItem(id: UUID().uuidString, at: Date(), runID: "", kind: .userMessage(input))]
            var response = Nexus_V1_ExecuteResponse()
            response.task = task
            return response
        }
        let response = try await requireActions().execute(input, attachments: attachments, agentID: agentID, source: source)
        if response.hasTask {
            tasks[response.task.id] = response.task
            append(TimelineItem(id: "local_\(UUID().uuidString)", at: Date(), runID: response.task.currentRunID, kind: .userMessage(input)), to: response.task.id)
        }
        return response
    }

    public func sendMessage(taskID: String, text: String, questionID: String? = nil, choice: String? = nil, attachments: [Nexus_V1_Attachment] = []) async throws {
        if let questionID { questions.removeValue(forKey: questionID) }
        if isDemo {
            append(TimelineItem(id: UUID().uuidString, at: Date(), runID: "", kind: .userMessage(choice ?? text)), to: taskID)
            return
        }
        try await requireActions().sendMessage(taskID: taskID, text: text, questionID: questionID, choice: choice, attachments: attachments)
    }

    /// Signs (Face ID for biometric approvals) and sends a decision.
    public func decide(_ approval: Nexus_V1_Approval, approve: Bool, comment: String = "", remember: Bool = false) async throws {
        if isDemo {
            if approve && approval.level == .biometric {
                _ = try await ApprovalSigner().authenticate(reason: "Approve “\(approval.title)”")
            }
            approvals.removeValue(forKey: approval.id)
            return
        }
        let updated = try await requireActions().decide(
            approval: approval, approve: approve, comment: comment, remember: remember, allowBiometric: true
        )
        approvals.removeValue(forKey: approval.id)
        if updated.state == .pending { approvals[updated.id] = updated }
        scheduleSnapshot()
    }

    /// Finds an approval by id (cached or fetched) — for deep links and notifications.
    public func approval(id: String) async -> Nexus_V1_Approval? {
        if let cached = approvals[id] { return cached }
        guard !isDemo, let hub else { return nil }
        if let fetched = try? await NexusActions(hub: hub).approval(id: id) {
            approvals[fetched.id] = fetched
            return fetched
        }
        return nil
    }

    public func cancel(taskID: String) async throws {
        if isDemo {
            tasks[taskID]?.state = .cancelled
            return
        }
        try await requireActions().cancel(taskID: taskID)
    }

    public func escalate(taskID: String, hint: String = "") async throws {
        if isDemo {
            tasks[taskID]?.state = .escalating
            return
        }
        let task = try await requireActions().escalate(taskID: taskID, hint: hint)
        tasks[task.id] = task
    }

    /// Kill switch.
    @discardableResult
    public func stopAll() async throws -> Int {
        if isDemo {
            var count = 0
            for (id, task) in tasks where task.state.isActive {
                tasks[id]?.state = .cancelled
                count += 1
            }
            return count
        }
        return try await requireActions().stopAll()
    }

    public func dismissNotice(_ id: String) {
        notices.removeAll { $0.id == id }
        scheduleSnapshot()
    }

    // MARK: Vault

    public func search(_ query: String) async throws -> [Nexus_V1_SearchHit] {
        if isDemo {
            return query.isEmpty ? MockData.searchHits
                : MockData.searchHits.filter { $0.note.title.localizedCaseInsensitiveContains(query) || $0.snippet.localizedCaseInsensitiveContains(query) }
        }
        guard let hub else { throw HubError.notConfigured }
        var request = Nexus_V1_SearchRequest()
        request.query = query
        request.limit = 30
        return try await hub.call { headers in
            await hub.vault.search(request: request, headers: headers)
        }.hits
    }

    public func note(id: String, path: String) async throws -> Nexus_V1_Note {
        if isDemo { return MockData.note(id: id) }
        guard let hub else { throw HubError.notConfigured }
        var request = Nexus_V1_GetNoteRequest()
        request.id = id
        request.path = path
        return try await hub.call { headers in
            await hub.vault.getNote(request: request, headers: headers)
        }.note
    }

    public func capture(_ text: String, url: String? = nil) async throws {
        if isDemo { return }
        _ = try await requireActions().capture(text, url: url, source: "ios-capture")
    }

    // MARK: Machines

    public func files(nodeID: String, path: String) async throws -> [Nexus_V1_FileEntry] {
        if isDemo { return MockData.files }
        guard let hub else { throw HubError.notConfigured }
        var request = Nexus_V1_ListFilesRequest()
        request.nodeID = nodeID
        request.path = path
        return try await hub.call { headers in
            await hub.nodes.listFiles(request: request, headers: headers)
        }.entries
    }

    public func wake(nodeID: String) async throws {
        if isDemo { return }
        guard let hub else { throw HubError.notConfigured }
        var request = Nexus_V1_WakeNodeRequest()
        request.id = nodeID
        _ = try await hub.call { headers in
            await hub.nodes.wakeNode(request: request, headers: headers)
        }
    }

    // MARK: Devices

    public func loadDevices() async {
        guard !isDemo, let hub else { return }
        do {
            devices = try await hub.call { headers in
                await hub.devices.listDevices(request: Nexus_V1_ListDevicesRequest(), headers: headers)
            }.devices
        } catch {
            lastError = error.localizedDescription
        }
    }

    public func revoke(deviceID: String) async throws {
        if isDemo {
            devices.removeAll { $0.id == deviceID }
            return
        }
        guard let hub else { throw HubError.notConfigured }
        var request = Nexus_V1_RevokeDeviceRequest()
        request.id = deviceID
        _ = try await hub.call { headers in
            await hub.devices.revokeDevice(request: request, headers: headers)
        }
        devices.removeAll { $0.id == deviceID }
    }

    // MARK: Widget snapshot

    /// Debounced write of the widget snapshot; `onSnapshot` lets the app reload timelines.
    @ObservationIgnored public var onSnapshot: (() -> Void)?

    private func scheduleSnapshot() {
        snapshotTask?.cancel()
        snapshotTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled, let self else { return }
            self.writeSnapshot()
        }
    }

    public func writeSnapshot() {
        let machines = nodes.map { node -> WidgetSnapshot.Machine in
            let status = self.status(ofNode: node.id)
            return WidgetSnapshot.Machine(
                id: node.id,
                name: node.name,
                online: status?.online ?? false,
                cpuPercent: status?.cpuPercent ?? 0,
                activeRuns: Int(status?.activeRuns ?? 0)
            )
        }
        let running = runningTasks.prefix(5).map { task in
            WidgetSnapshot.RunningTask(
                id: task.id,
                title: task.displayTitle,
                agentSymbol: agent(task.agentID)?.symbolName ?? "sparkles",
                waitingForYou: task.state.needsUser
            )
        }
        let favourites = agents.filter(\.enabled).prefix(4).map { agent in
            WidgetSnapshot.Workflow(id: agent.id, name: agent.name, symbol: agent.symbolName, prompt: "")
        }
        let snapshot = WidgetSnapshot(
            pendingApprovals: pendingApprovals.count,
            openQuestions: questions.count,
            inboxCount: inbox.count,
            runningCount: runningTasks.count,
            costCentsToday: costCentsToday,
            machines: machines,
            running: Array(running),
            favourites: favourites.isEmpty ? (WidgetSnapshot.load()?.favourites ?? []) : Array(favourites),
            lastResult: doneTasks.first.map { $0.summary.isEmpty ? $0.displayTitle : $0.summary }
        )
        if snapshot != WidgetSnapshot.load() {
            snapshot.save()
            onSnapshot?()
        }
    }
}
