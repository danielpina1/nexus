import NexusCore
import SwiftUI

/// Live team run (web TeamRun): header, limit meters, inline approvals, result, member tree,
/// timeline, blackboard and team messages. The tree updates from the event stream; the run
/// (blackboard, messages, totals) is polled while it is active.
struct TeamRunView: View {
    let rootTaskID: String

    @Environment(NexusStore.self) private var store
    @Environment(ToastCenter.self) private var toasts

    @State private var run: Nexus_V1_TeamRun?
    @State private var errorMessage: String?
    @State private var keyEventsOnly = false
    @State private var isCancelling = false

    private var root: Nexus_V1_Task? {
        store.tasks[rootTaskID] ?? (run?.hasRoot == true ? run?.root : nil)
    }

    /// All tasks of the tree (root included), live from the store.
    private var tree: [Nexus_V1_Task] {
        var byID: [String: Nexus_V1_Task] = [:]
        for item in run?.tasks ?? [] { byID[item.task.id] = item.task }
        for task in store.tasks.values where task.rootID == rootTaskID || task.id == rootTaskID {
            byID[task.id] = task
        }
        return byID.values.sorted { $0.createdAt.date < $1.createdAt.date }
    }

    private var team: Nexus_V1_Team? {
        run?.hasTeam == true ? run?.team : nil
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s6) {
                if let root {
                    content(root)
                } else if let errorMessage {
                    NXErrorState(message: errorMessage) { Task { await load() } }
                } else {
                    NXSkeleton(height: 28, width: 260)
                    LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: NXSpace.s3) {
                        ForEach(0..<4, id: \.self) { _ in NXSkeleton(height: 92, cornerRadius: NXRadius.lg) }
                    }
                    NXSkeletonRows(rows: 4)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(team?.name ?? "Team run")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await load() }
        .task(id: rootTaskID) {
            // Poll while the run is active (every 4 s), slowly afterwards.
            while !Task.isCancelled {
                await load()
                let active = root?.state.isActive ?? true
                try? await Task.sleep(for: .seconds(active ? 4 : 30))
            }
        }
    }

    private func load() async {
        do {
            run = try await store.teamRun(rootTaskID: rootTaskID)
            errorMessage = nil
        } catch {
            if run == nil { errorMessage = error.localizedDescription }
        }
    }

    // MARK: Content

    @ViewBuilder
    private func content(_ root: Nexus_V1_Task) -> some View {
        let tree = self.tree
        let active = root.state.isActive
        let treeIDs = Set(tree.map(\.id))
        let approvals = store.pendingApprovals.filter { $0.rootTaskID == rootTaskID || treeIDs.contains($0.taskID) }

        header(root, tree: tree)

        if let reason = run?.stoppedReason, !reason.isEmpty {
            HStack(alignment: .top, spacing: NXSpace.s2) {
                Image(systemName: "exclamationmark.octagon").accessibilityHidden(true)
                Text("**Stopped:** \(reason)")
                    .nx(NXType.footnote)
            }
            .foregroundStyle(NXColor.danger)
            .padding(NXSpace.s3)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous).strokeBorder(NXColor.danger.opacity(0.4), lineWidth: 1))
            .accessibilityElement(children: .combine)
        }

        LimitMeters(run: run, root: root, tree: tree, limits: run?.hasLimits == true ? run?.limits : team?.limits)

        if !approvals.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s3) {
                Label("\(approvals.count) \(approvals.count == 1 ? "approval waits" : "approvals wait") for you", systemImage: "hand.raised")
                    .nxText(NXType.body.weight(.semibold))
                    .foregroundStyle(NXColor.warn)
                    .accessibilityAddTraits(.isHeader)
                ForEach(approvals, id: \.id) { approval in
                    ApprovalCardView(approval: approval)
                }
            }
        }

        if !active && !root.summary.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                NXOverline("Result")
                MarkdownText(markdown: root.summary)
                    .nxText(NXType.body)
                    .foregroundStyle(NXColor.fg)
            }
            .nxCard()
        }

        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Task tree", count: tree.count)
            TaskTreeView(root: root, tree: tree)
        }

        timeline(tree: tree)
        blackboard
        TeamMessagesView(
            rootTaskID: rootTaskID,
            messages: run?.messages ?? [],
            roles: roles(tree: tree),
            isActive: active
        ) { message in
            run?.messages.append(message)
        }
    }

    private func header(_ root: Nexus_V1_Task, tree: [Nexus_V1_Task]) -> some View {
        let active = root.state.isActive
        let working = tree.filter { $0.id != rootTaskID && $0.state.isActive && $0.phase != "waiting_slot" && $0.state != .queued }.count
        let queued = tree.filter { $0.id != rootTaskID && ($0.phase == "waiting_slot" || $0.state == .queued) }.count
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(alignment: .top, spacing: NXSpace.s3) {
                TeamIconView(symbol: team?.symbolName ?? "point.3.connected.trianglepath.dotted", size: 44)
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    Text(root.displayTitle)
                        .nx(NXType.title1)
                        .foregroundStyle(NXColor.fg)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                    FlowBadges {
                        TaskStateBadge(state: root.state)
                        if let phase = PhaseBadge(phase: root.phase) { phase }
                        if active { NXBadge("live", tone: .info, dot: true, pulse: true) }
                        if let team { NXBadge(team.strategy.label, tone: .accent) }
                    }
                    Text(Self.headerMeta(root: root, working: working, queued: queued))
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
            }
            HStack(spacing: NXSpace.s2) {
                NavigationLink(value: TaskRoute(id: root.id)) {
                    Label("Root task", systemImage: "arrow.up.forward.square")
                }
                .buttonStyle(NXButtonStyle(kind: .ghost, size: .small))
                if active {
                    NXConfirmButton("Cancel run", confirmTitle: "Confirm cancel", systemImage: "xmark.circle", size: .small, isLoading: isCancelling) {
                        cancel(root)
                    }
                }
            }
        }
    }

    private static func headerMeta(root: Nexus_V1_Task, working: Int, queued: Int) -> String {
        var parts: [String] = []
        if let started = root.createdAt.optionalDate {
            parts.append("started " + Format.relative(started))
        }
        if working + queued > 0 {
            parts.append("\(working) active" + (queued > 0 ? " · \(queued) queued" : ""))
        }
        return parts.joined(separator: " · ")
    }

    private func cancel(_ root: Nexus_V1_Task) {
        isCancelling = true
        Task {
            defer { isCancelling = false }
            do {
                try await store.cancel(taskID: root.id)
                toasts.success("Run cancelled")
            } catch {
                toasts.error("Couldn't cancel the run", error) { cancel(root) }
            }
        }
    }

    private func roles(tree: [Nexus_V1_Task]) -> [String] {
        var seen: [String] = []
        for role in (team?.members.map(\.role) ?? []) + tree.filter({ $0.id != rootTaskID }).map(\.role) where !role.isEmpty && !seen.contains(role) {
            seen.append(role)
        }
        return seen
    }

    // MARK: Timeline

    private func timeline(tree: [Nexus_V1_Task]) -> some View {
        let live = store.teamEvents[rootTaskID] ?? []
        let items: [TeamTimelineItem] = live.isEmpty
            ? TeamTimelineItem.synthesized(tree: tree, rootID: rootTaskID, messages: run?.messages ?? [])
            : live.map(TeamTimelineItem.init(event:))
        let shown = (keyEventsOnly ? items.filter { !$0.isMinor } : items).reversed()
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Live timeline", count: items.count)
            Toggle(isOn: $keyEventsOnly) {
                Text("Key events only")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
            }
            .tint(NXColor.accent)
            .frame(minHeight: NXSize.touchMin)
            if shown.isEmpty {
                NXEmptyState(systemImage: "list.bullet.indent", title: "No team events yet", message: "Delegations, results, messages and limits appear here as they happen.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(shown.prefix(60).enumerated()), id: \.element.id) { index, item in
                        TeamTimelineRow(item: item)
                        if index < min(shown.count, 60) - 1 { NXDivider(inset: 40) }
                    }
                }
                .nxCard(padding: 0)
            }
        }
    }

    // MARK: Blackboard

    private var blackboard: some View {
        let entries = run?.blackboard ?? []
        let enabled = team?.blackboard ?? true
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Blackboard", count: entries.count)
            if !enabled {
                Text("Off for this team.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            } else if entries.isEmpty {
                NXEmptyState(systemImage: "note.text", title: "Nothing written yet", message: "Members share plans, findings and drafts here.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(entries.enumerated()), id: \.element.key) { index, entry in
                        BlackboardEntryView(entry: entry)
                        if index < entries.count - 1 { NXDivider() }
                    }
                }
                .nxCard(padding: 0)
            }
        }
    }
}

// MARK: - Pieces

/// Wraps badges onto several lines when they don't fit.
struct FlowBadges<Content: View>: View {
    @ViewBuilder let content: () -> Content

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) { content() }
            VStack(alignment: .leading, spacing: 4) { content() }
        }
    }
}

/// Hub-side phase badge ("waiting for a slot", "critic reviewing", …).
struct PhaseBadge: View {
    let label: String
    let tone: NXTone
    let symbol: String

    init?(phase: String) {
        guard !phase.isEmpty else { return nil }
        let style = Self.style(phase)
        label = style.label
        tone = style.tone
        symbol = style.symbol
    }

    private static func style(_ phase: String) -> (label: String, tone: NXTone, symbol: String) {
        switch phase {
        case "waiting_slot": return ("waiting for a slot", .neutral, "hourglass")
        case "strategy": return ("running strategy", .info, "arrow.triangle.2.circlepath")
        case "reviewing": return ("critic reviewing", .accent, "arrow.triangle.2.circlepath")
        case "revising": return ("revising", .accent, "arrow.triangle.2.circlepath")
        case "repairing": return ("repairing output", .warn, "arrow.triangle.2.circlepath")
        default: return (phase.replacingOccurrences(of: "_", with: " "), .neutral, "arrow.triangle.2.circlepath")
        }
    }

    var body: some View {
        NXBadge(label, systemImage: symbol, tone: tone)
            .accessibilityLabel("Phase: \(label)")
    }
}

/// Tasks, Runs, Cost and Time against the team's limits.
struct LimitMeters: View {
    let run: Nexus_V1_TeamRun?
    let root: Nexus_V1_Task
    let tree: [Nexus_V1_Task]
    let limits: Nexus_V1_TeamLimits?

    var body: some View {
        let others = tree.filter { $0.id != root.id }
        let queued = others.filter { $0.phase == "waiting_slot" || ($0.state == .queued && $0.phase != "strategy") }.count
        let active = others.filter { $0.state.isActive }.count - queued
        let costFromTasks = tree.reduce(0) { $0 + $1.costCents }
        let rootTree = root.hasTreeCost ? Int(root.treeCost.amount.minorUnits) : 0
        let runTotal = run?.hasTotalCost == true ? Int(run?.totalCost.amount.minorUnits ?? 0) : 0
        let cost = max(costFromTasks, rootTree, runTotal)
        let tasks = max(tree.count, Int(run?.totalTasks ?? 0))
        let runsFromTasks = Int(run?.tasks.reduce(0) { $0 + $1.runs } ?? 0)
        let runs = max(Int(run?.totalRuns ?? 0), runsFromTasks, tree.filter { $0.state != .queued }.count)
        let start = root.createdAt.optionalDate ?? Date()
        let end: Date? = root.state.isActive ? nil : root.updatedAt.optionalDate
        let deadline: Date? = run?.hasDeadline == true ? run?.deadline.date : nil
        let timeLimit: TimeInterval? = deadline.map { $0.timeIntervalSince(start) } ?? ((limits?.timeoutSeconds ?? 0) > 0 ? TimeInterval(limits?.timeoutSeconds ?? 0) : nil)
        let maxCost = limits?.hasMaxCost == true ? Int(limits?.maxCost.minorUnits ?? 0) : 0
        let columns = [GridItem(.flexible(), spacing: NXSpace.s3), GridItem(.flexible(), spacing: NXSpace.s3)]

        LazyVGrid(columns: columns, spacing: NXSpace.s3) {
            NXLimitMeter(
                "Tasks", systemImage: "square.stack",
                value: Double(tasks), limit: limits.map { Double($0.maxTasks) },
                display: "\(tasks)", limitDisplay: (limits?.maxTasks ?? 0) > 0 ? "\(limits?.maxTasks ?? 0)" : "∞",
                hint: "\(max(0, active)) active · \(queued) queued"
            )
            NXLimitMeter(
                "Runs", systemImage: "arrow.clockwise",
                value: Double(runs), limit: limits.map { Double($0.maxRuns) },
                display: "\(runs)", limitDisplay: (limits?.maxRuns ?? 0) > 0 ? "\(limits?.maxRuns ?? 0)" : "∞",
                hint: "retries, reviews and repairs included"
            )
            NXLimitMeter(
                "Cost", systemImage: "eurosign.circle",
                value: Double(cost), limit: maxCost > 0 ? Double(maxCost) : nil,
                display: Format.cost(cents: cost), limitDisplay: maxCost > 0 ? Format.cost(cents: maxCost) : "no budget",
                hint: maxCost > 0 ? "\(Format.cost(cents: max(0, maxCost - cost))) left" : "LLM spend of the whole tree"
            )
            TimelineView(.periodic(from: .now, by: 1)) { context in
                let elapsed = (end ?? context.date).timeIntervalSince(start)
                NXLimitMeter(
                    "Time", systemImage: "clock",
                    value: elapsed, limit: timeLimit,
                    display: Format.duration(elapsed), limitDisplay: timeLimit.map { Format.duration($0) } ?? "no deadline",
                    hint: root.state.isActive ? (deadline.map { "deadline " + $0.formatted(date: .omitted, time: .shortened) } ?? "running") : "ended"
                )
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Totals against limits")
    }
}

/// The member tree, flattened depth-first with indentation (web TaskTree).
struct TaskTreeView: View {
    let root: Nexus_V1_Task
    let tree: [Nexus_V1_Task]

    @Environment(NexusStore.self) private var store

    private var rows: [(task: Nexus_V1_Task, depth: Int)] {
        let ids = Set(tree.map(\.id))
        var children: [String: [Nexus_V1_Task]] = [:]
        for task in tree where task.id != root.id {
            // Orphans (parent not loaded yet) hang off the root.
            let parent = !task.parentID.isEmpty && ids.contains(task.parentID) ? task.parentID : root.id
            children[parent, default: []].append(task)
        }
        var out: [(task: Nexus_V1_Task, depth: Int)] = []
        func visit(_ task: Nexus_V1_Task, _ depth: Int) {
            out.append((task, depth))
            for child in children[task.id] ?? [] where depth < 8 {
                visit(child, depth + 1)
            }
        }
        visit(root, 0)
        return out
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            ForEach(rows, id: \.task.id) { row in
                NavigationLink(value: TaskRoute(id: row.task.id)) {
                    TreeNodeRow(task: row.task, depth: row.depth)
                }
                .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.md))
                .padding(.leading, CGFloat(min(row.depth, 4)) * 20)
                .overlay(alignment: .leading) {
                    if row.depth > 0 {
                        Rectangle()
                            .fill(NXColor.borderStrong)
                            .frame(width: 1)
                            .padding(.leading, CGFloat(min(row.depth, 4)) * 20 - 10)
                            .accessibilityHidden(true)
                    }
                }
            }
        }
    }
}

struct TreeNodeRow: View {
    let task: Nexus_V1_Task
    let depth: Int

    @Environment(NexusStore.self) private var store

    var body: some View {
        let agent = store.agent(task.agentID)
        let role = task.role.isEmpty ? (depth == 0 ? "orchestrator" : "sub-task") : task.role
        let waiting = task.state.needsUser
        let runtime = task.agentID.hasPrefix("eph:") ? "ephemeral" : (task.cost.runtime.isEmpty ? (agent?.runtime ?? "") : task.cost.runtime)
        HStack(alignment: .top, spacing: NXSpace.s3) {
            AgentIconView(agent: agent, agentID: task.agentID, size: 28)
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    NXBadge(role, tone: depth == 0 ? .accent : .neutral)
                    Text(task.displayTitle)
                        .nx(NXType.footnote.weight(.medium))
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(2)
                }
                Text([agent?.name ?? task.agentID, runtime, store.node(task.nodeID)?.name ?? task.nodeID, Format.cost(cents: task.costCents)].filter { !$0.isEmpty }.joined(separator: " · "))
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
                if !task.summary.isEmpty && !task.state.isActive {
                    Text(task.summary)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.fg2)
                        .lineLimit(2)
                }
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    if let phase = PhaseBadge(phase: task.phase) { phase }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(NXSpace.s3)
        .frame(minHeight: 52)
        .background(depth == 0 ? NXColor.surface2 : NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                .strokeBorder(waiting ? NXColor.warn.opacity(0.4) : NXColor.border, lineWidth: 1)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityHint("Opens the task.")
    }
}

/// One entry of the team timeline (from the stream, or synthesized from the run).
struct TeamTimelineItem: Identifiable {
    let id: String
    let at: Date
    let label: String
    let symbol: String
    let tone: NXTone
    let text: String
    let taskID: String
    let isMinor: Bool

    init(id: String, at: Date, label: String, symbol: String, tone: NXTone, text: String, taskID: String, isMinor: Bool = false) {
        self.id = id
        self.at = at
        self.label = label
        self.symbol = symbol
        self.tone = tone
        self.text = text
        self.taskID = taskID
        self.isMinor = isMinor
    }

    init(event item: TeamEventItem) {
        let ev = item.event
        let style = Self.describe(ev)
        self.init(
            id: item.id, at: item.at, label: style.label, symbol: style.symbol, tone: style.tone, text: style.text, taskID: ev.taskID,
            isMinor: ev.kind == .blackboardUpdated || ev.kind == .awaiting
        )
    }

    /// web TeamRun describe() + KIND.
    private static func describe(_ ev: Nexus_V1_TeamEvent) -> (label: String, symbol: String, tone: NXTone, text: String) {
        let role = ev.role.isEmpty ? "orchestrator" : ev.role
        let suffix = ev.text.isEmpty ? "" : " — \(ev.text)"
        switch ev.kind {
        case .started: return ("started", "play", .info, "Team run started" + suffix)
        case .delegated: return ("delegated", "arrow.triangle.branch", .accent, "→ \(role): \(ev.text)")
        case .spawned: return ("spawned", "sparkles", .accent, "Spawned specialist \(role): \(ev.text)")
        case .memberFinished: return ("finished", "checkmark.circle", .ok, "\(role) finished" + suffix)
        case .blackboardUpdated: return ("blackboard", "note.text", .neutral, "\(role) wrote “\(ev.key)”")
        case .messageSent: return ("message", "bubble.left", .info, "\(role): \(ev.text)")
        case .reported: return ("reported", "doc.text", .ok, "\(role) reported: \(ev.text)")
        case .awaiting: return ("awaiting", "hourglass", .neutral, "\(role) " + (ev.text.isEmpty ? "is waiting for members" : ev.text))
        case .strategyStep: return ("strategy", "arrow.left.arrow.right", .info, ev.text)
        case .review: return ("review", "text.magnifyingglass", .accent, "Critic: \(ev.text)")
        case .limitReached: return ("limit reached", "exclamationmark.octagon", .danger, ev.text)
        case .approvalPending: return ("approval", "hand.raised", .warn, "\(role) waits for your approval: \(ev.text)")
        case .finished: return ("run ended", "flag", .ok, "Run ended" + suffix)
        default: return ("event", "sparkles", .neutral, ev.text)
        }
    }

    /// Without stream events (opened after the fact): tasks created / ended and messages.
    static func synthesized(tree: [Nexus_V1_Task], rootID: String, messages: [Nexus_V1_TeamMessage]) -> [TeamTimelineItem] {
        var out: [TeamTimelineItem] = []
        for task in tree {
            let created = task.createdAt.optionalDate ?? .distantPast
            if task.id == rootID {
                out.append(TeamTimelineItem(id: "s_\(task.id)", at: created, label: "started", symbol: "play", tone: .info, text: "Team run started — \(task.displayTitle)", taskID: task.id))
            } else {
                out.append(TeamTimelineItem(id: "d_\(task.id)", at: created, label: "delegated", symbol: "arrow.triangle.branch", tone: .accent, text: "→ \(task.role.isEmpty ? "member" : task.role): \(task.displayTitle)", taskID: task.id))
            }
            if task.state.isTerminal, let ended = task.updatedAt.optionalDate {
                out.append(TeamTimelineItem(
                    id: "f_\(task.id)", at: ended,
                    label: task.id == rootID ? "run ended" : "finished",
                    symbol: task.state == .succeeded ? "checkmark.circle" : "xmark.circle",
                    tone: task.state == .succeeded ? .ok : .danger,
                    text: "\(task.id == rootID ? "Run" : (task.role.isEmpty ? "member" : task.role)) \(task.state.label.lowercased())" + (task.summary.isEmpty ? "" : " — \(task.summary)"),
                    taskID: task.id
                ))
            }
        }
        for message in messages {
            out.append(TeamTimelineItem(id: "m_\(message.id)", at: message.at.optionalDate ?? .distantPast, label: "message", symbol: "bubble.left", tone: .info, text: "\(message.fromRole.isEmpty ? "user" : message.fromRole) → \(message.to.isEmpty ? "orchestrator" : message.to): \(message.text)", taskID: message.fromTaskID))
        }
        return out.sorted { $0.at < $1.at }
    }
}

struct TeamTimelineRow: View {
    let item: TeamTimelineItem

    var body: some View {
        let row = HStack(alignment: .top, spacing: NXSpace.s2) {
            Image(systemName: item.symbol)
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(item.tone.color)
                .frame(width: 16)
                .padding(.top, 2)
                .accessibilityHidden(true)
            Text(item.at.formatted(date: .omitted, time: .shortened))
                .nx(NXType.caption.weight(.regular))
                .monospacedDigit()
                .foregroundStyle(NXColor.muted)
            (Text(item.label.uppercased() + "  ").nx(NXType.caption.weight(.semibold)).foregroundStyle(item.tone.color)
                + Text(item.text).nx(NXType.footnote).foregroundStyle(NXColor.fg2))
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s2)
        .frame(minHeight: NXSize.touchMin)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)

        if item.taskID.isEmpty {
            row
        } else {
            NavigationLink(value: TaskRoute(id: item.taskID)) { row }
                .buttonStyle(NXPlainPressStyle())
        }
    }
}

struct BlackboardEntryView: View {
    let entry: Nexus_V1_BlackboardEntry
    @State private var expanded = false

    private var text: String {
        guard entry.hasValue else { return "" }
        if case .stringValue(let s) = entry.value.kind { return s }
        return (try? entry.value.jsonString()) ?? ""
    }

    var body: some View {
        let long = text.count > 220 || text.split(separator: "\n").count > 6
        VStack(alignment: .leading, spacing: NXSpace.s1) {
            HStack(spacing: 6) {
                Text(entry.key)
                    .nx(NXType.mono.weight(.semibold))
                    .foregroundStyle(NXColor.fg)
                if !entry.authorRole.isEmpty { NXBadge(entry.authorRole) }
                Text("v\(entry.version)" + (entry.hasUpdatedAt ? " · " + Format.relative(entry.updatedAt.date) : ""))
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
            }
            Text(text)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.fg2)
                .lineLimit(expanded || !long ? nil : 4)
                .textSelection(.enabled)
            if long {
                Button(expanded ? "Show less" : "Show all") { expanded.toggle() }
                    .font(NXType.footnote.weight(.semibold).font)
                    .foregroundStyle(NXColor.accent)
                    .frame(minHeight: NXSize.touchMin)
                    .nxFocusRing()
            }
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s3)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Team messages and the composer ("to orchestrator / everyone / a role").
struct TeamMessagesView: View {
    let rootTaskID: String
    let messages: [Nexus_V1_TeamMessage]
    let roles: [String]
    let isActive: Bool
    var onSent: (Nexus_V1_TeamMessage) -> Void

    @Environment(NexusStore.self) private var store
    @Environment(ToastCenter.self) private var toasts
    @State private var to = "orchestrator"
    @State private var text = ""
    @State private var sending = false

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Team messages", count: messages.count)
            VStack(alignment: .leading, spacing: NXSpace.s3) {
                if messages.isEmpty {
                    NXEmptyState(systemImage: "bubble.left.and.bubble.right", title: "No messages yet", message: "Members talk to each other here; you can join in.", compact: true)
                } else {
                    ForEach(messages, id: \.id) { message in
                        bubble(message)
                    }
                }
                NXDivider()
                HStack(spacing: NXSpace.s2) {
                    Menu {
                        Picker("Send to", selection: $to) {
                            Text("to orchestrator").tag("orchestrator")
                            Text("to everyone").tag("all")
                            ForEach(roles, id: \.self) { role in
                                Text("to \(role)").tag(role)
                            }
                        }
                    } label: {
                        HStack(spacing: 4) {
                            Text(to == "all" ? "everyone" : to)
                                .nx(NXType.footnote.weight(.medium))
                                .lineLimit(1)
                            Image(systemName: "chevron.up.chevron.down")
                                .font(.system(size: 10, weight: .semibold))
                                .accessibilityHidden(true)
                        }
                        .foregroundStyle(NXColor.fg)
                        .padding(.horizontal, NXSpace.s3)
                        .frame(minHeight: NXSize.touchMin)
                        .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.borderInput, lineWidth: 1))
                    }
                    .disabled(!isActive)
                    .accessibilityLabel("Send to")
                    .accessibilityValue(to)
                    NXTextField(placeholder: isActive ? "Message the team…" : "The run has ended", text: $text)
                        .disabled(!isActive)
                        .onSubmit { send() }
                    NXIconButton(systemImage: "paperplane.fill", label: "Send message", tint: NXColor.accent, isLoading: sending) {
                        send()
                    }
                    .disabled(!isActive || text.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
            .nxCard()
        }
    }

    private func bubble(_ message: Nexus_V1_TeamMessage) -> some View {
        let mine = message.fromRole.isEmpty || message.fromRole == "user"
        return HStack {
            if mine { Spacer(minLength: 32) }
            VStack(alignment: .leading, spacing: 2) {
                Text("\(mine ? "You" : message.fromRole) → \(message.to.isEmpty ? "orchestrator" : message.to)" + (message.hasAt ? " · " + Format.relative(message.at.date) : ""))
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                Text(message.text)
                    .nx(NXType.body)
                    .foregroundStyle(NXColor.fg)
                    .textSelection(.enabled)
            }
            .padding(.horizontal, NXSpace.s3)
            .padding(.vertical, NXSpace.s2)
            .background(mine ? NXColor.accentSoft : NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
            .accessibilityElement(children: .combine)
            if !mine { Spacer(minLength: 32) }
        }
    }

    private func send() {
        let message = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !message.isEmpty, isActive, !sending else { return }
        sending = true
        Task {
            defer { sending = false }
            do {
                let sent = try await store.sendTeamMessage(rootTaskID: rootTaskID, to: to, text: message)
                text = ""
                onSent(sent)
            } catch {
                toasts.error("Couldn't send the message", error) { send() }
            }
        }
    }
}
