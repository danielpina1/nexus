import NexusCore
import SwiftUI

/// Work tab (web "Work" section): Tasks, Teams, Agents, Architect and Spend as section tabs.
struct WorkView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        @Bindable var router = router
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0, pinnedViews: [.sectionHeaders]) {
                Section {
                    VStack(alignment: .leading, spacing: NXSpace.s6) {
                        HubUnreachableBanner()
                        switch router.workSection {
                        case .tasks: TasksSection()
                        case .teams: TeamsSection()
                        case .agents: AgentsSection()
                        case .architect: ArchitectSection()
                        case .spend: SpendSection()
                        }
                    }
                    .padding(.horizontal, NXSpace.s4)
                    .padding(.top, NXSpace.s4)
                    .padding(.bottom, NXSpace.s8)
                } header: {
                    NXSectionTabs(
                        AppRouter.WorkSection.allCases,
                        selection: $router.workSection,
                        title: { $0.title },
                        badge: { $0 == .tasks ? store.runningTasks.count : nil }
                    )
                    .nxGlass(showsBorder: false)
                }
            }
        }
        .nxScreenBackground()
        .navigationTitle("Work")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { CommonToolbar() }
        .refreshable { await store.refresh() }
    }
}

// MARK: - Agents

/// Work → Agents (web Agents): the agent notes in the vault.
struct AgentsSection: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s4) {
            NXPageHeader("Agents", subtitle: "Agents are Markdown notes in your vault (agents/*.md). Edit them in any editor, or improve one with the Architect.")
            if store.agents.isEmpty {
                NXEmptyState(systemImage: "cpu", title: "No agents yet", message: "Add a note under agents/ in the vault, or design one with the Architect.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(store.agents.enumerated()), id: \.element.id) { index, agent in
                        NavigationLink(value: AgentRoute(id: agent.id)) {
                            NXRow(agent.name, subtitle: agent.description_p, minHeight: 60, leading: {
                                AgentIconView(agent: agent, size: 36)
                            }, trailing: {
                                HStack(spacing: 6) {
                                    if !agent.errors.isEmpty {
                                        NXBadge("\(agent.errors.count)", systemImage: "exclamationmark.triangle", tone: .danger)
                                            .accessibilityLabel("\(agent.errors.count) errors")
                                    } else if !agent.enabled {
                                        NXBadge("disabled")
                                    } else if !agent.runtime.isEmpty {
                                        NXBadge(agent.runtime)
                                    }
                                    NXChevron()
                                }
                            })
                        }
                        .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                        if index < store.agents.count - 1 { NXDivider(inset: 64) }
                    }
                }
                .nxCard(padding: 0)
            }
        }
    }
}

struct AgentDetailView: View {
    let agentID: String

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(ToastCenter.self) private var toasts
    @State private var running = false

    var body: some View {
        Group {
            if let agent = store.agent(agentID) {
                ScrollView {
                    VStack(alignment: .leading, spacing: NXSpace.s6) {
                        HStack(alignment: .top, spacing: NXSpace.s3) {
                            AgentIconView(agent: agent, size: 48)
                            VStack(alignment: .leading, spacing: NXSpace.s1) {
                                Text(agent.name)
                                    .nx(NXType.title1)
                                    .foregroundStyle(NXColor.fg)
                                    .accessibilityAddTraits(.isHeader)
                                Text("agents/\(agent.id).md")
                                    .nx(NXType.mono)
                                    .foregroundStyle(NXColor.muted)
                                HStack(spacing: 6) {
                                    NXBadge(agent.enabled ? "enabled" : "disabled", tone: agent.enabled ? .ok : .neutral)
                                    if !agent.runtime.isEmpty { NXBadge(agent.runtime, tone: .accent) }
                                }
                            }
                        }
                        if !agent.description_p.isEmpty {
                            Text(agent.description_p)
                                .nx(NXType.bodyLg)
                                .foregroundStyle(NXColor.fg2)
                        }
                        ForEach(agent.errors, id: \.self) { error in
                            NXInlineError(error)
                        }

                        ViewThatFits(in: .horizontal) {
                            HStack(spacing: NXSpace.s2) { actions(agent) }
                            VStack(alignment: .leading, spacing: NXSpace.s2) { actions(agent) }
                        }

                        VStack(alignment: .leading, spacing: 0) {
                            NXKeyValueRow("Id", value: agent.id, mono: true)
                            NXDivider()
                            NXKeyValueRow("Runtime", value: agent.runtime.isEmpty ? "auto" : agent.runtime)
                            NXDivider()
                            NXKeyValueRow("Model", value: agent.model.isEmpty ? "default" : agent.model)
                            if !agent.tools.isEmpty {
                                NXDivider()
                                NXKeyValueRow("Tools", value: agent.tools.joined(separator: ", "), mono: true)
                            }
                            if !agent.nodeTags.isEmpty {
                                NXDivider()
                                NXKeyValueRow("Machines", value: agent.nodeTags.joined(separator: ", "))
                            }
                            if !agent.skills.isEmpty {
                                NXDivider()
                                NXKeyValueRow("Skills", value: agent.skills.joined(separator: ", "))
                            }
                        }
                        .nxCard(padding: NXSpace.s4)

                        let tasks = store.rootTasks.filter { $0.agentID == agent.id }.prefix(8)
                        VStack(alignment: .leading, spacing: NXSpace.s3) {
                            NXSectionHeader("Recent tasks", count: tasks.count)
                            if tasks.isEmpty {
                                NXEmptyState(systemImage: "clock", title: "No tasks yet", compact: true)
                                    .nxCard(padding: 0)
                            } else {
                                TaskList(tasks: Array(tasks))
                            }
                        }
                    }
                    .padding(NXSpace.s4)
                }
            } else {
                NXEmptyState(systemImage: "cpu", title: "Agent not found", message: "It may have been renamed or removed from the vault.")
            }
        }
        .nxScreenBackground()
        .navigationTitle(store.agent(agentID)?.name ?? "Agent")
        .navigationBarTitleDisplayMode(.inline)
    }

    @ViewBuilder
    private func actions(_ agent: Nexus_V1_Agent) -> some View {
        NXButton("Run", systemImage: "play.fill", kind: .primary, isLoading: running) {
            run(agent)
        }
        .disabled(!agent.enabled)
        NavigationLink(value: ChatThread(agentID: agent.id)) {
            Label("Chat", systemImage: "bubble.left")
        }
        .buttonStyle(NXButtonStyle(kind: .secondary))
        NXButton("Improve", systemImage: "wand.and.stars", kind: .ghost) {
            router.improveAgent(agent.id)
        }
        .accessibilityHint("Opens the Architect to draft an improved version.")
    }

    private func run(_ agent: Nexus_V1_Agent) {
        running = true
        Task {
            defer { running = false }
            do {
                switch try await store.submit("Run \(agent.name)", agentID: agent.id, source: "ios") {
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

// MARK: - Spend

/// Work → Spend (web Costs): spend from task costs, per day, agent and model.
struct SpendSection: View {
    @Environment(NexusStore.self) private var store

    private struct Row: Identifiable {
        let id: String
        let label: String
        let cents: Int
    }

    var body: some View {
        let tasks = Array(store.tasks.values)
        let calendar = Calendar.current
        let today = calendar.startOfDay(for: Date())
        let days: [(date: Date, cents: Int)] = (0..<14).reversed().map { offset in
            let day = calendar.date(byAdding: .day, value: -offset, to: today) ?? today
            let next = calendar.date(byAdding: .day, value: 1, to: day) ?? day
            let cents = tasks.filter {
                let d = $0.createdAt.date
                return d >= day && d < next
            }.reduce(0) { $0 + $1.costCents }
            return (day, cents)
        }
        let week = days.suffix(7).reduce(0) { $0 + $1.cents }
        let total = tasks.reduce(0) { $0 + $1.costCents }
        let byAgent = Dictionary(grouping: tasks, by: { $0.agentID })
            .map { Row(id: $0.key, label: store.agent($0.key)?.name ?? ($0.key.isEmpty ? "Unrouted" : $0.key), cents: $0.value.reduce(0) { $0 + $1.costCents }) }
            .filter { $0.cents > 0 }
            .sorted { $0.cents > $1.cents }
        let byModel = Dictionary(grouping: tasks.filter { $0.hasCost }, by: { $0.cost.model.isEmpty ? ($0.cost.runtime.isEmpty ? "unknown" : $0.cost.runtime) : $0.cost.model })
            .map { Row(id: $0.key, label: $0.key, cents: $0.value.reduce(0) { $0 + $1.costCents }) }
            .filter { $0.cents > 0 }
            .sorted { $0.cents > $1.cents }

        VStack(alignment: .leading, spacing: NXSpace.s6) {
            NXPageHeader("Spend", subtitle: "Spend from task costs, per day, agent and model.")
            LazyVGrid(columns: [GridItem(.flexible(), spacing: NXSpace.s3), GridItem(.flexible(), spacing: NXSpace.s3)], spacing: NXSpace.s3) {
                NXStatTile("Today", value: Format.cost(cents: store.costCentsToday))
                NXStatTile("Last 7 days", value: Format.cost(cents: week))
                NXStatTile("All loaded tasks", value: Format.cost(cents: total), detail: "\(tasks.count) tasks")
                NXStatTile("Most expensive", value: byAgent.first.map { Format.cost(cents: $0.cents) } ?? "–", detail: byAgent.first?.label)
            }
            if total == 0 {
                NXEmptyState(systemImage: "eurosign.circle", title: "No spend yet", message: "Costs appear as agents run. Each run reports its cost on the event stream.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Daily spend")
                    DailyBars(days: days)
                        .nxCard()
                }
                breakdown("By agent", rows: byAgent)
                breakdown("By model", rows: byModel)
            }
        }
    }

    @ViewBuilder
    private func breakdown(_ title: String, rows: [Row]) -> some View {
        if !rows.isEmpty {
            let maxCents = max(1, rows.first?.cents ?? 1)
            VStack(alignment: .leading, spacing: NXSpace.s3) {
                NXSectionHeader(title)
                VStack(spacing: NXSpace.s3) {
                    ForEach(rows) { row in
                        VStack(alignment: .leading, spacing: 6) {
                            HStack {
                                Text(row.label)
                                    .nx(NXType.body.weight(.medium))
                                    .foregroundStyle(NXColor.fg)
                                    .lineLimit(1)
                                Spacer()
                                Text(Format.cost(cents: row.cents))
                                    .nx(NXType.body)
                                    .monospacedDigit()
                                    .foregroundStyle(NXColor.fg2)
                            }
                            NXMeter(value: Double(row.cents) / Double(maxCents), label: row.label, tone: .accent)
                                .accessibilityHidden(true)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
                .nxCard()
            }
        }
    }
}

/// 14 bars, today highlighted (web MiniBars / daily chart). One VoiceOver element per day.
struct DailyBars: View {
    let days: [(date: Date, cents: Int)]

    var body: some View {
        let maxCents = max(1, days.map(\.cents).max() ?? 1)
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(alignment: .bottom, spacing: 4) {
                ForEach(Array(days.enumerated()), id: \.offset) { index, day in
                    let isToday = index == days.count - 1
                    RoundedRectangle(cornerRadius: 2, style: .continuous)
                        .fill(isToday ? NXColor.accent : NXColor.accent.opacity(0.3))
                        .frame(height: max(4, 96 * CGFloat(day.cents) / CGFloat(maxCents)))
                        .frame(maxWidth: .infinity)
                        .accessibilityElement()
                        .accessibilityLabel(day.date.formatted(.dateTime.weekday(.wide).day().month()))
                        .accessibilityValue(Format.cost(cents: day.cents))
                }
            }
            .frame(height: 96, alignment: .bottom)
            HStack {
                Text(days.first?.date.formatted(.dateTime.day().month()) ?? "")
                Spacer()
                Text("Today")
            }
            .nxText(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)
            .accessibilityHidden(true)
        }
    }
}
