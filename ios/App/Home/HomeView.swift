import NexusCore
import SwiftUI

/// Home (web Overview): greeting, metrics (Running, Needs you, Spent today, Machines), the top
/// of the inbox, running work, recent results, machines and favourites.
struct HomeView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    private var needsYou: [InboxItem] {
        store.inbox.filter { item in
            if case .notice(let n) = item { return n.notice.kind != .brief }
            return true
        }
    }

    private var recent: [Nexus_V1_Task] {
        Array(store.doneTasks.filter { $0.parentID.isEmpty }.prefix(5))
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: NXSpace.s8) {
                VStack(alignment: .leading, spacing: NXSpace.s4) {
                    Text(Date().formatted(.dateTime.weekday(.wide).day().month(.wide)))
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                    HubUnreachableBanner()
                    if let error = store.lastError {
                        ErrorBanner(message: error) { store.lastError = nil }
                    }
                    if let brief = store.notices.first(where: { $0.notice.kind == .brief }) {
                        BriefCard(notice: brief.notice)
                    }
                    MetricsGrid()
                }

                if !needsYou.isEmpty {
                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        NXSectionHeader("Needs you", count: needsYou.count) {
                            Button {
                                router.openInbox()
                            } label: {
                                NXLinkLabel(title: "Open inbox")
                            }
                            .buttonStyle(NXPlainPressStyle())
                        }
                        ForEach(needsYou.prefix(4)) { item in
                            InboxCard(item: item, compact: true)
                        }
                    }
                }

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Running now", count: store.runningTasks.count, live: !store.runningTasks.isEmpty) {
                        Button {
                            router.openWork(.tasks)
                        } label: {
                            NXLinkLabel(title: "All tasks")
                        }
                        .buttonStyle(NXPlainPressStyle())
                    }
                    if store.runningTasks.isEmpty {
                        NXEmptyState(systemImage: "checkmark.circle", title: "Nothing running", message: "Ask Nexus for something in Chat.", compact: true)
                            .nxCard(padding: 0)
                    } else {
                        ForEach(store.runningTasks.filter { $0.parentID.isEmpty }.prefix(6), id: \.id) { task in
                            NavigationLink(value: TaskRoute(id: task.id)) {
                                LiveTaskCard(task: task)
                            }
                            .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                        }
                    }
                }

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Recent")
                    if recent.isEmpty {
                        NXEmptyState(systemImage: "clock", title: "No finished tasks yet", message: "Results show up here as soon as a run ends.", compact: true)
                            .nxCard(padding: 0)
                    } else {
                        TaskList(tasks: recent)
                    }
                }

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Machines") {
                        Button {
                            router.openMachines()
                        } label: {
                            NXLinkLabel(title: "Details")
                        }
                        .buttonStyle(NXPlainPressStyle())
                    }
                    MachinesSummary()
                }

                if !store.agents.isEmpty {
                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        NXSectionHeader("Favourites")
                        FavouritesGrid()
                    }
                }
            }
            .padding(.horizontal, NXSpace.s4)
            .padding(.top, NXSpace.s2)
            .padding(.bottom, NXSpace.s8)
        }
        .nxScreenBackground()
        .navigationTitle(Format.greeting())
        .toolbar { CommonToolbar() }
        .refreshable { await store.refresh() }
    }
}

/// Running · Needs you · Spent today · Machines (web Metric tiles).
struct MetricsGrid: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    private let columns = [GridItem(.flexible(), spacing: NXSpace.s3), GridItem(.flexible(), spacing: NXSpace.s3)]

    var body: some View {
        let running = store.runningTasks
        let active = running.filter { $0.state == .running }.count
        let inbox = store.inbox
        let approvals = inbox.filter { if case .approval = $0 { return true }; return false }.count
        let questions = inbox.filter { if case .question = $0 { return true }; return false }.count
        let notices = inbox.count - approvals - questions
        let online = store.nodes.filter { store.status(ofNode: $0.id)?.online == true }.count

        LazyVGrid(columns: columns, spacing: NXSpace.s3) {
            metric(
                NXStatTile(
                    "Running",
                    value: "\(running.count)",
                    detail: running.isEmpty ? "All quiet" : "\(active) active · \(running.count - active) waiting",
                    dot: running.isEmpty ? nil : .info
                )
            ) { router.openWork(.tasks) }
            metric(
                NXStatTile(
                    "Needs you",
                    value: "\(inbox.count)",
                    detail: inbox.isEmpty ? "Inbox zero" : [
                        approvals > 0 ? "\(approvals) approval\(approvals == 1 ? "" : "s")" : nil,
                        questions > 0 ? "\(questions) question\(questions == 1 ? "" : "s")" : nil,
                        notices > 0 ? "\(notices) notices" : nil,
                    ].compactMap { $0 }.joined(separator: " · "),
                    dot: inbox.isEmpty ? nil : .danger
                )
            ) { router.openInbox() }
            metric(
                NXStatTile("Spent today", value: Format.cost(cents: store.costCentsToday), detail: "cloud and local runs")
            ) { router.openWork(.spend) }
            metric(
                NXStatTile(
                    "Machines",
                    value: store.nodes.isEmpty ? "–" : "\(online)/\(store.nodes.count)",
                    detail: store.nodes.isEmpty ? "None paired" : (online == store.nodes.count ? "All online" : "\(store.nodes.count - online) offline"),
                    dot: store.nodes.isEmpty ? nil : (online == store.nodes.count ? .ok : .warn)
                )
            ) { router.openMachines() }
        }
    }

    private func metric(_ tile: NXStatTile, action: @escaping () -> Void) -> some View {
        Button(action: action) { tile }
            .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
    }
}

struct BriefCard: View {
    let notice: Nexus_V1_Notice

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: NXSpace.s2) {
                Image(systemName: "sun.max")
                    .foregroundStyle(NXColor.warn)
                    .accessibilityHidden(true)
                Text(notice.title.isEmpty ? "Brief" : notice.title)
                    .nx(NXType.title3)
                    .foregroundStyle(NXColor.fg)
                    .accessibilityAddTraits(.isHeader)
            }
            MarkdownText(markdown: notice.body)
                .nxText(NXType.body)
                .foregroundStyle(NXColor.fg2)
        }
        .nxCard()
    }
}

/// Live card for a running task (web LiveTaskCard): last streamed line, elapsed, tools, cost.
struct LiveTaskCard: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store

    private var lastLine: String {
        let entries = store.timelines[task.id] ?? []
        for entry in entries.reversed() {
            if case .output(let text, _) = entry.kind {
                let line = text.split(separator: "\n").last.map(String.init) ?? text
                return line.replacingOccurrences(of: "[*_`#>\\[\\]]", with: "", options: .regularExpression).trimmingCharacters(in: .whitespaces)
            }
        }
        return ""
    }

    private var toolCalls: Int {
        (store.timelines[task.id] ?? []).filter { if case .toolCall = $0.kind { return true }; return false }.count
    }

    var body: some View {
        let waiting = task.state.needsUser
        let agent = store.agent(task.agentID)
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(alignment: .top, spacing: NXSpace.s3) {
                AgentIconView(agent: agent, agentID: task.agentID, size: 32)
                VStack(alignment: .leading, spacing: 2) {
                    Text(task.displayTitle)
                        .nx(NXType.body.weight(.semibold))
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                    Text([agent?.name ?? (task.agentID.isEmpty ? "Routing…" : task.agentID), store.node(task.nodeID)?.name].compactMap { $0 }.joined(separator: " · "))
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                TaskStateBadge(state: task.state)
            }
            Text(lastLine.isEmpty ? (waiting ? (task.state == .waitingApproval ? "Waiting for your approval" : "Waiting for your answer") : (task.summary.isEmpty ? "Working…" : task.summary)) : lastLine)
                .nx(NXType.mono)
                .foregroundStyle(NXColor.fg2)
                .lineLimit(2)
                .frame(maxWidth: .infinity, minHeight: 34, alignment: .topLeading)
                .padding(.horizontal, NXSpace.s3)
                .padding(.vertical, NXSpace.s2)
                .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            HStack(spacing: NXSpace.s3) {
                if let created = task.createdAt.optionalDate {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        Text(Format.duration(context.date.timeIntervalSince(created)))
                            .monospacedDigit()
                    }
                }
                if toolCalls > 0 {
                    Text("\(toolCalls) tools")
                }
                Spacer()
                if task.costCents > 0 {
                    Text(Format.cost(cents: task.costCents))
                        .monospacedDigit()
                        .foregroundStyle(NXColor.fg2)
                }
            }
            .nxText(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)
        }
        .nxCard(urgency: waiting ? .warn : nil)
        .accessibilityElement(children: .combine)
        .accessibilityHint("Opens the task.")
    }
}

/// Machines card on Home: one row per node with live CPU / memory / disk.
struct MachinesSummary: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        if store.nodes.isEmpty {
            NXEmptyState(systemImage: "cpu", title: "No machines yet", message: "Pair a node with this Hub to run agents on it.", compact: true)
                .nxCard(padding: 0)
        } else {
            VStack(spacing: 0) {
                ForEach(Array(store.nodes.enumerated()), id: \.element.id) { index, node in
                    NavigationLink(value: MachineRoute(id: node.id)) {
                        MachineSummaryRow(node: node, status: store.status(ofNode: node.id))
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                    if index < store.nodes.count - 1 { NXDivider() }
                }
            }
            .nxCard(padding: 0)
        }
    }
}

struct MachineSummaryRow: View {
    let node: Nexus_V1_Node
    let status: Nexus_V1_NodeStatus?

    var body: some View {
        let online = status?.online == true
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: NXSpace.s2) {
                NXDot(color: online ? NXColor.ok : NXColor.muted, size: 8)
                Text(node.name)
                    .nx(NXType.body.weight(.medium))
                    .foregroundStyle(NXColor.fg)
                Text(node.os)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                Spacer()
                if let runs = status?.activeRuns, runs > 0 {
                    Text("\(runs) running")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.info)
                }
                if !online {
                    Text("offline")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
            }
            if let status, online {
                NXMetricRow(label: "CPU", percent: status.cpuPercent)
                NXMetricRow(label: "RAM", percent: status.memoryPercent)
                NXMetricRow(label: "Disk", percent: status.diskPercent)
            }
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s3)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(node.name), \(online ? "online" : "offline")")
    }
}

struct FavouritesGrid: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(ToastCenter.self) private var toasts
    @State private var running: String?

    private let columns = [GridItem(.flexible(), spacing: NXSpace.s3), GridItem(.flexible(), spacing: NXSpace.s3)]

    var body: some View {
        LazyVGrid(columns: columns, spacing: NXSpace.s3) {
            ForEach(store.agents.filter(\.enabled).prefix(6), id: \.id) { agent in
                Button {
                    run(agent)
                } label: {
                    HStack(spacing: NXSpace.s2) {
                        AgentIconView(agent: agent, size: 28)
                        Text(agent.name)
                            .nx(NXType.footnote.weight(.semibold))
                            .foregroundStyle(NXColor.fg)
                            .lineLimit(2)
                        Spacer(minLength: 0)
                        if running == agent.id {
                            ProgressView().controlSize(.small)
                        }
                    }
                    .padding(.horizontal, NXSpace.s3)
                    .frame(maxWidth: .infinity, minHeight: 52)
                    .nxCard(padding: 0)
                }
                .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                .disabled(running != nil)
                .accessibilityLabel("Run \(agent.name)")
            }
        }
    }

    private func run(_ agent: Nexus_V1_Agent) {
        running = agent.id
        Task {
            defer { running = nil }
            do {
                switch try await store.submit("Run \(agent.name)", agentID: agent.id, source: "ios-favourite") {
                case .sent(let response):
                    if response.hasTask { router.openTask(response.task.id) }
                case .queued:
                    toasts.info("Queued", "\(agent.name) runs when your Nexus is reachable.")
                }
            } catch {
                toasts.error("Couldn't run \(agent.name)", error) { run(agent) }
            }
        }
    }
}

#Preview {
    NavigationStack {
        HomeView()
            .nexusDestinations()
    }
    .environment(NexusStore.preview())
    .environment(AppRouter())
    .environment(ToastCenter())
}
