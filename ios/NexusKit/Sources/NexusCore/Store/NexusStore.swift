import Foundation
import NexusAPI
import Observation
import SwiftProtobuf

/// Result of an action that may be queued while the Hub is unreachable.
public enum Delivery<Value> {
    /// Reached the Hub.
    case sent(Value)
    /// Stored in the `Outbox`; sent automatically once the Hub answers again.
    case queued(OutboxItem)

    public var isQueued: Bool {
        if case .queued = self { return true }
        return false
    }
}

public enum OutboxError: Error, LocalizedError {
    case attachmentsNeedConnection

    public var errorDescription: String? {
        switch self {
        case .attachmentsNeedConnection:
            return "Photos can't be queued. Connect to your Nexus (home Wi-Fi or VPN) and send again."
        }
    }
}

/// The app's observable state: agents, nodes, tasks, inbox, per-task timelines and the
/// connection state — fed by `EventStream` and refreshed with unary Hub calls.
///
/// Remote access (docs/NETWORK.md): `reachability` says whether the Hub answers and through
/// which route; `NWPathMonitor` changes (Wi-Fi ↔ cellular, VPN up/down) trigger a re-probe of
/// all paired Hub URLs, and commands / replies / captures / signed decisions made while it is
/// unreachable go to the `Outbox` and are sent on reconnect.
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
    /// Team / task-tree lifecycle events per root task id, oldest first (team run view).
    public private(set) var teamEvents: [String: [TeamEventItem]] = [:]
    public var lastError: String?

    // Remote access
    public private(set) var reachability: HubReachability = .unknown
    public private(set) var networkPath: NetworkPathInfo?
    /// A `/healthz` probe is running (drives the spinner on "Retry").
    public private(set) var isProbing = false
    /// Last time the Hub answered (probe or event stream).
    public private(set) var lastContact: Date? = HubRouteMemory.lastContact
    /// Items waiting for the Hub, oldest first.
    public private(set) var outbox: [OutboxItem] = Outbox.load()

    /// Called whenever a task changes (used by the Live Activity manager).
    @ObservationIgnored public var onTaskChanged: ((Nexus_V1_Task) -> Void)?
    /// Called for every approval event (used to refresh notifications / activities).
    @ObservationIgnored public var onApprovalChanged: ((Nexus_V1_Approval) -> Void)?

    @ObservationIgnored private var hub: HubClient?
    @ObservationIgnored private var stream: EventStream?
    @ObservationIgnored private var snapshotTask: Task<Void, Never>?
    @ObservationIgnored private let network = NetworkMonitor()
    @ObservationIgnored private var probeTask: Task<Void, Never>?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private var isFlushing = false
    @ObservationIgnored private var streamGeneration = 0
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
    /// The current Hub client (nil in demo mode and before the first connection).
    public var hubClient: HubClient? { hub }

    /// The Hub URL currently in use (nil before the first connection attempt).
    public var currentHubURL: URL? { hub?.baseURL }

    /// "Home Wi-Fi", "VPN", "Unreachable", … for Settings → Remote access.
    public var routeLabel: String {
        switch reachability {
        case .connected(_, let viaVPN):
            if viaVPN == true { return "VPN" }
            if networkPath?.usesWiFi == true { return "Home Wi-Fi" }
            if networkPath?.usesWired == true { return "Home network (wired)" }
            if networkPath?.usesCellular == true { return "Cellular (direct)" }
            return "Connected"
        case .unreachable: return "Unreachable"
        case .offline: return "No network"
        case .checking: return "Checking…"
        case .unknown: return isDemo ? "Demo" : "Not checked yet"
        }
    }

    // MARK: Derived

    public var inbox: [InboxItem] {
        var items: [InboxItem] = approvals.values.filter { $0.state == .pending }.map { .approval($0) }
        items += questions.values.map { .question($0) }
        // A chat reply is shown in the chat: no "result" card for it (web selectors.ts).
        items += notices.filter { !isChatResult($0) }.map { .notice($0) }
        return items.sorted { lhs, rhs in
            lhs.rank != rhs.rank ? lhs.rank < rhs.rank : lhs.date > rhs.date
        }
    }

    /// A result notice of a chat task (the reply is already in the chat).
    public func isChatResult(_ notice: NoticeItem) -> Bool {
        notice.notice.kind == .result && !notice.taskID.isEmpty && tasks[notice.taskID]?.isChat == true
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
        rootTasks.filter { agentID == nil ? ($0.source == "ios-chat" || $0.source == "chat" || $0.source.isEmpty) : $0.agentID == agentID }
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
        if hub != nil && (stream != nil || reachability.isUnavailable) { return }
        configuration = HubConfiguration.load()
        guard let configuration, configuration.primaryURL != nil else {
            connection = .disconnected
            reachability = .unknown
            return
        }
        outbox = Outbox.load()
        if approvals.isEmpty {
            approvals = Dictionary(ApprovalCache.load().map { ($0.id, $0) }, uniquingKeysWith: { _, new in new })
        }
        network.start { [weak self] info in self?.pathChanged(info) }
        do {
            // Optimistic start on the URL that answered last; the probe below corrects the
            // route (other URL, VPN, unreachable) within a few seconds.
            let hub = try HubClient(configuration: configuration)
            self.hub = hub
            startStream(hub)
            Task { await refresh() }
        } catch {
            lastError = error.localizedDescription
        }
        probe(restartStream: false)
    }

    /// App returned to the foreground: re-check the route and reconnect a stalled stream.
    public func foreground() {
        start()
        guard !isDemo, hub != nil else { return }
        if !reachability.isConnected {
            probe(restartStream: true)
        } else if let stream, connection != .connected {
            stream.restart()
        }
    }

    public func stop() {
        stopStream()
        retryTask?.cancel()
        retryTask = nil
        probeTask?.cancel()
        probeTask = nil
    }

    // MARK: Remote access

    private func startStream(_ hub: HubClient) {
        stream?.stop()
        streamGeneration += 1
        let generation = streamGeneration
        let stream = EventStream(
            hub: hub,
            onEvent: { [weak self] event in self?.apply(event) },
            onState: { [weak self] state in self?.streamStateChanged(state, generation: generation) }
        )
        self.stream = stream
        stream.start()
    }

    private func stopStream() {
        stream?.stop()
        stream = nil
        streamGeneration += 1
        if !isDemo { connection = .disconnected }
    }

    private func streamStateChanged(_ state: ConnectionState, generation: Int) {
        // Ignore late callbacks from a stream that was replaced (route change).
        guard generation == streamGeneration else { return }
        connection = state
        switch state {
        case .connected:
            guard let hub else { return }
            let viaVPN = networkPath.map(\.hasVPNInterface)
            HubRouteMemory.recordContact(url: hub.baseURL, viaVPN: viaVPN)
            lastContact = Date()
            reachability = .connected(url: hub.baseURL, viaVPN: viaVPN)
            retryTask?.cancel()
            retryTask = nil
            if !outbox.isEmpty { Task { await flushOutbox() } }
        case .reconnecting(let attempt, _):
            // The stream keeps failing: probe GET /healthz on every paired URL before
            // declaring the Hub unreachable (and switch URL if another one answers).
            if attempt >= 2 && !isProbing { probe(restartStream: false) }
        default:
            break
        }
    }

    private func pathChanged(_ info: NetworkPathInfo) {
        let previous = networkPath
        networkPath = info
        guard !isDemo, configuration != nil, hub != nil else { return }
        if !info.isSatisfied {
            reachability = .offline
            stopStream()
            scheduleRetry()
            return
        }
        // Wi-Fi ↔ cellular, VPN up/down: the way to the Hub may have changed. Re-probe (after a
        // short settle delay — on-demand VPNs come up a moment after the new path).
        if previous != nil || !reachability.isConnected {
            probe(after: .milliseconds(800), restartStream: true)
        }
    }

    /// "Retry" in the unreachable banner and Settings → Remote access → Test connection.
    public func retryConnection() {
        guard !isDemo else { return }
        if hub == nil { start() }
        probe(restartStream: true)
    }

    /// Probes all paired Hub URLs (last working first) with `GET /healthz`.
    private func probe(after delay: Duration = .zero, restartStream: Bool) {
        guard !isDemo, let configuration, !configuration.hubURLs.isEmpty else { return }
        probeTask?.cancel()
        probeTask = Task { [weak self] in
            if delay > .zero { try? await Task.sleep(for: delay) }
            guard !Task.isCancelled, let self else { return }
            await self.runProbe(configuration, restartStream: restartStream)
        }
    }

    private func runProbe(_ configuration: HubConfiguration, restartStream: Bool) async {
        if reachability == .unknown { reachability = .checking }
        isProbing = true
        let path = network.current ?? networkPath
        let result = await HubClient.connect(configuration: configuration, path: path)
        isProbing = false
        guard !Task.isCancelled else { return }

        switch result.outcome {
        case .reachable(let url):
            let viaVPN = path.map(\.hasVPNInterface)
            lastContact = Date()
            reachability = .connected(url: url, viaVPN: viaVPN)
            retryTask?.cancel()
            retryTask = nil
            if hub?.baseURL != url || stream == nil, let client = result.client {
                let switched = hub != nil
                hub = client
                startStream(client)
                if switched { await refresh() }
            } else if restartStream, connection != .connected {
                stream?.restart()
            }
            await flushOutbox()
        case .unreachable(let reason):
            reachability = .unreachable(reason: reason)
            stopStream()
            scheduleRetry()
        case .offline:
            reachability = .offline
            stopStream()
            scheduleRetry()
        }
    }

    /// While unreachable (and the app is running), re-probe every 15–60 s. Path changes
    /// (e.g. the WireGuard on-demand tunnel coming up) trigger an immediate probe as well.
    private func scheduleRetry() {
        guard retryTask == nil else { return }
        retryTask = Task { [weak self] in
            var delay: Double = 15
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(delay))
                guard !Task.isCancelled, let self else { return }
                if self.reachability.isConnected { break }
                if self.networkPath?.isSatisfied != false && !self.isProbing {
                    self.probe(restartStream: true)
                }
                delay = min(delay * 1.5, 60)
            }
        }
    }

    /// Sends `kind` now, or queues it when the Hub is unreachable. A transport failure is
    /// confirmed with a `/healthz` probe before queueing (the Hub may just have answered with
    /// an error, or another paired URL may work).
    private func deliverOrQueue<Value>(
        _ kind: OutboxItem.Kind,
        idempotencyKey: String = .idempotencyKey(),
        send: (NexusActions, String) async throws -> Value
    ) async throws -> Delivery<Value> {
        if isDemo { throw HubError.demoMode }
        guard let configuration, let hub else { throw HubError.notConfigured }
        if reachability.isUnavailable {
            return .queued(enqueue(kind, idempotencyKey: idempotencyKey))
        }
        do {
            return .sent(try await send(NexusActions(hub: hub), idempotencyKey))
        } catch let error where HubClient.isTransportFailure(error) {
            await runProbe(configuration, restartStream: true)
            if reachability.isConnected, let current = self.hub {
                // The Hub answers (possibly on another URL now): one retry, same key.
                return .sent(try await send(NexusActions(hub: current), idempotencyKey))
            }
            return .queued(enqueue(kind, idempotencyKey: idempotencyKey))
        }
    }

    private func enqueue(_ kind: OutboxItem.Kind, idempotencyKey: String) -> OutboxItem {
        let item = OutboxItem(kind: kind, idempotencyKey: idempotencyKey)
        outbox = Outbox.enqueue(item)
        return item
    }

    /// Drops a queued item (swipe to delete in Settings → Remote access).
    public func discardOutboxItem(_ id: String) {
        Outbox.remove(id: id)
        outbox = Outbox.load()
    }

    /// Sends queued items in order. Expired signed decisions are dropped with a message;
    /// items the Hub rejects are dropped with the Hub's reason; a transport failure stops
    /// the flush (still unreachable) and keeps the rest.
    public func flushOutbox() async {
        guard !isFlushing, !isDemo, let hub, reachability.isConnected else { return }
        let pending = Outbox.load()
        guard !pending.isEmpty else {
            outbox = []
            return
        }
        isFlushing = true
        defer { isFlushing = false }
        let actions = NexusActions(hub: hub)
        var sentAny = false
        var problems: [String] = []
        for item in pending {
            if item.isExpired {
                Outbox.remove(id: item.id)
                problems.append("Not sent — “\(item.summary)” expired before your Nexus could be reached. Nothing was approved or denied.")
                continue
            }
            do {
                try await actions.deliver(item)
                Outbox.remove(id: item.id)
                sentAny = true
            } catch let error where HubClient.isTransportFailure(error) {
                var failed = item
                failed.attempts += 1
                failed.lastError = error.localizedDescription
                Outbox.update(failed)
                probe(restartStream: false)
                break
            } catch {
                Outbox.remove(id: item.id)
                problems.append("Could not send “\(item.summary)”: \(error.localizedDescription)")
            }
        }
        outbox = Outbox.load()
        if !problems.isEmpty { lastError = problems.joined(separator: "\n") }
        if sentAny { await refresh() }
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
        reachability = .unknown
        HubConfiguration.clear()
        outbox = []
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
        teamEvents = [:]
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
        for task in MockData.teamRunTasks {
            tasks[task.id] = task
        }
        approvals[MockData.teamApproval.id] = MockData.teamApproval
        teamEvents[MockData.teamRunRootID] = MockData.teamEvents
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
        } catch let error where HubClient.isTransportFailure(error) {
            // Not an error to show: the unreachable banner covers it. Confirm with a probe.
            if !isProbing { probe(restartStream: false) }
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

        case .team(let teamEvent):
            let rootID = teamEvent.rootTaskID.isEmpty ? event.taskID : teamEvent.rootTaskID
            if !rootID.isEmpty {
                var entries = teamEvents[rootID] ?? []
                entries.append(TeamEventItem(id: entryID, at: at, event: teamEvent))
                if entries.count > Self.maxTimelineEntries {
                    entries.removeFirst(entries.count - Self.maxTimelineEntries)
                }
                teamEvents[rootID] = entries
            }

        case .none:
            break
        }
        scheduleSnapshot()
    }

    /// Merges tasks fetched outside the event stream (team runs, task trees).
    public func upsert(tasks newTasks: [Nexus_V1_Task]) {
        for task in newTasks where !task.id.isEmpty {
            tasks[task.id] = task
        }
    }

    /// Adds or replaces an approval fetched outside the event stream (e.g. Architect publish).
    public func upsert(approval: Nexus_V1_Approval) {
        if approval.state == .pending {
            approvals[approval.id] = approval
        } else {
            approvals.removeValue(forKey: approval.id)
        }
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

    /// Like `execute`, but queues the command in the `Outbox` when the Hub is unreachable
    /// (away from home with the VPN off). Used by the chat composer and favourites.
    public func submit(_ input: String, attachments: [Nexus_V1_Attachment] = [], agentID: String? = nil, source: String = "ios-chat") async throws -> Delivery<Nexus_V1_ExecuteResponse> {
        if isDemo {
            return .sent(try await execute(input, attachments: attachments, agentID: agentID, source: source))
        }
        if !attachments.isEmpty {
            // Photos are never queued (the outbox is small, text-only).
            if reachability.isUnavailable { throw OutboxError.attachmentsNeedConnection }
            return .sent(try await execute(input, attachments: attachments, agentID: agentID, source: source))
        }
        let delivery = try await deliverOrQueue(.command(input: input, agentID: agentID, source: source)) { actions, key in
            try await actions.execute(input, attachments: attachments, agentID: agentID, source: source, idempotencyKey: key)
        }
        if case .sent(let response) = delivery, response.hasTask {
            tasks[response.task.id] = response.task
            append(TimelineItem(id: "local_\(UUID().uuidString)", at: Date(), runID: response.task.currentRunID, kind: .userMessage(input)), to: response.task.id)
        }
        return delivery
    }

    /// Sends a chat message (CommandContext source "chat" + conversation id): the Hub routes it
    /// without the router (assistant, "@agent", "/search", "remember"). Queued in the `Outbox`
    /// while the Hub is unreachable (text only).
    public func sendChat(_ input: String, conversationID: String, attachments: [Nexus_V1_Attachment] = []) async throws -> Delivery<Nexus_V1_ExecuteResponse> {
        if isDemo { throw HubError.demoMode }
        // The event stream may already have delivered newer states / output of this task:
        // keep those, and put the prompt first in its timeline.
        let record: (Nexus_V1_ExecuteResponse) -> Void = { [weak self] response in
            guard let self, response.hasTask else { return }
            let id = response.task.id
            if self.tasks[id] == nil { self.tasks[id] = response.task }
            var entries = self.timelines[id] ?? []
            entries.insert(TimelineItem(id: "local_\(UUID().uuidString)", at: Date(), runID: response.task.currentRunID, kind: .userMessage(input)), at: 0)
            self.timelines[id] = entries
        }
        if !attachments.isEmpty {
            if reachability.isUnavailable { throw OutboxError.attachmentsNeedConnection }
            let response = try await requireActions().execute(input, attachments: attachments, source: "chat", conversationID: conversationID)
            record(response)
            return .sent(response)
        }
        let delivery = try await deliverOrQueue(.chat(input: input, conversationID: conversationID)) { actions, key in
            try await actions.execute(input, source: "chat", conversationID: conversationID, idempotencyKey: key)
        }
        if case .sent(let response) = delivery { record(response) }
        return delivery
    }

    /// Demo mode: applies a scripted event as if it came from the Hub (chat demo).
    public func applyDemo(_ event: Nexus_V1_Event) {
        guard isDemo else { return }
        apply(event)
    }

    /// Replies to a task; queued while the Hub is unreachable (text only).
    @discardableResult
    public func sendMessage(taskID: String, text: String, questionID: String? = nil, choice: String? = nil, attachments: [Nexus_V1_Attachment] = []) async throws -> Delivery<Void> {
        if isDemo {
            if let questionID { questions.removeValue(forKey: questionID) }
            append(TimelineItem(id: UUID().uuidString, at: Date(), runID: "", kind: .userMessage(choice ?? text)), to: taskID)
            return .sent(())
        }
        if !attachments.isEmpty {
            if reachability.isUnavailable { throw OutboxError.attachmentsNeedConnection }
            try await requireActions().sendMessage(taskID: taskID, text: text, questionID: questionID, choice: choice, attachments: attachments)
            if let questionID { questions.removeValue(forKey: questionID) }
            return .sent(())
        }
        let delivery = try await deliverOrQueue(.message(taskID: taskID, text: text, questionID: questionID, choice: choice)) { actions, key in
            try await actions.sendMessage(taskID: taskID, text: text, questionID: questionID, choice: choice, attachments: attachments, idempotencyKey: key)
        }
        if let questionID { questions.removeValue(forKey: questionID) }
        return delivery
    }

    /// Signs (Face ID for biometric approvals) and sends a decision. While the Hub is
    /// unreachable the signed decision is queued and retried until the Hub answers or the
    /// signature's expiry passes (then it is dropped with a message).
    @discardableResult
    public func decide(_ approval: Nexus_V1_Approval, approve: Bool, comment: String = "", remember: Bool = false) async throws -> Delivery<Void> {
        if isDemo {
            if approve && approval.level == .biometric {
                _ = try await ApprovalSigner().authenticate(reason: "Approve “\(approval.title)”")
            }
            approvals.removeValue(forKey: approval.id)
            // Like the Hub's approval event: the run's timeline shows the decision.
            var decided = approval
            decided.state = approve ? .approved : .denied
            append(TimelineItem(id: UUID().uuidString, at: Date(), runID: approval.runID, kind: .approval(decided)), to: approval.taskID)
            onApprovalChanged?(decided)
            return .sent(())
        }
        let request = try await requireActions().signDecision(
            approval: approval, approve: approve, comment: comment, remember: remember
        )
        let blob: Data = try request.serializedBytes()
        let kind = OutboxItem.Kind.decision(
            request: blob,
            approvalID: approval.id,
            title: approval.title,
            approve: approve,
            expiresAt: request.decision.expiresAt.date
        )
        let delivery = try await deliverOrQueue(kind, idempotencyKey: approval.id) { actions, _ in
            try await actions.send(decision: request)
        }
        approvals.removeValue(forKey: approval.id)
        switch delivery {
        case .sent(let updated):
            if updated.state == .pending { approvals[updated.id] = updated }
            scheduleSnapshot()
            return .sent(())
        case .queued(let item):
            scheduleSnapshot()
            return .queued(item)
        }
    }

    /// Finds an approval by id (cached or fetched) — for deep links and notifications.
    public func approval(id: String) async -> Nexus_V1_Approval? {
        if let cached = approvals[id] { return cached }
        // While unreachable only the cache counts (no 60 s network timeout behind a spinner).
        guard !isDemo, let hub, !reachability.isUnavailable else { return nil }
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

    @discardableResult
    public func capture(_ text: String, url: String? = nil) async throws -> Delivery<Void> {
        if isDemo { return .sent(()) }
        return try await deliverOrQueue(.capture(text: text, url: url)) { actions, key in
            _ = try await actions.capture(text, url: url, source: "ios-capture", idempotencyKey: key)
        }
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
        if !isDemo { ApprovalCache.save(Array(approvals.values)) }
        if snapshot != WidgetSnapshot.load() {
            snapshot.save()
            onSnapshot?()
        }
    }
}
