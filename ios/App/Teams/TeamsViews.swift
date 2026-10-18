import NexusCore
import SwiftUI

// Teams (docs/SWARMS.md; web pages Teams.tsx and TeamDetail.tsx): swarms of agents under an
// orchestrator, working on one task tree. Teams are notes in teams/*.md.

/// Work → Teams: the team list with live run counts and recent runs.
struct TeamsSection: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    @State private var teams: [Nexus_V1_Team]?
    @State private var runs: [String: [Nexus_V1_Task]] = [:]
    @State private var errorMessage: String?

    private var liveCount: Int {
        store.tasks.values.filter { !$0.teamID.isEmpty && $0.parentID.isEmpty && $0.state.isActive }.count
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s4) {
            NXPageHeader(
                "Teams",
                subtitle: "Swarms of agents under an orchestrator, working on one task tree. Teams are notes in teams/*.md."
                    + (liveCount > 0 ? " \(liveCount) running now." : "")
            )
            if let teams {
                if teams.isEmpty {
                    NXEmptyState(
                        systemImage: "person.3",
                        title: "No teams yet",
                        message: "A team is an orchestrator plus member agents with shared limits. Ask the Architect to design one.",
                        actionTitle: "Design with the Architect"
                    ) {
                        router.openWork(.architect)
                    }
                    .nxCard(padding: 0)
                } else {
                    ForEach(teams, id: \.id) { team in
                        TeamCard(team: team, runs: runs[team.id] ?? [])
                    }
                }
            } else if let errorMessage {
                NXErrorState(message: errorMessage, compact: true) {
                    Task { await load() }
                }
                .nxCard(padding: 0)
            } else {
                NXSkeletonRows(rows: 3)
            }
        }
        .task { await load() }
    }

    private func load() async {
        do {
            let list = try await store.listTeams()
            teams = list
            errorMessage = nil
            for team in list {
                runs[team.id] = (try? await store.teamRuns(teamID: team.id, limit: 5)) ?? runs[team.id] ?? []
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

/// Team card (web TeamCard): icon, name, note path, badges, description, strategy,
/// orchestrator, members and the latest runs.
struct TeamCard: View {
    let team: Nexus_V1_Team
    let runs: [Nexus_V1_Task]

    @Environment(NexusStore.self) private var store

    var body: some View {
        // Prefer the live copies of the runs from the store.
        let liveRuns = runs.map { store.tasks[$0.id] ?? $0 }
        let live = liveRuns.filter { $0.state.isActive }.count
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NavigationLink(value: TeamRoute(id: team.id)) {
                HStack(alignment: .top, spacing: NXSpace.s3) {
                    TeamIconView(symbol: team.symbolName, size: 40)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(team.name.isEmpty ? team.id : team.name)
                            .nx(NXType.title3)
                            .foregroundStyle(NXColor.fg)
                            .multilineTextAlignment(.leading)
                        Text("teams/\(team.id).md")
                            .nx(NXType.mono)
                            .foregroundStyle(NXColor.muted)
                            .lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    if live > 0 {
                        NXBadge("\(live) live", tone: .info, dot: true, pulse: true)
                    }
                    NXChevron()
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(NXPlainPressStyle())
            .accessibilityLabel("\(team.name.isEmpty ? team.id : team.name), \(team.strategy.label) team\(live > 0 ? ", \(live) live" : "")")

            TeamStatusBadges(team: team)

            if !team.description_p.isEmpty {
                Text(team.description_p)
                    .nx(NXType.body)
                    .foregroundStyle(NXColor.fg2)
                    .lineLimit(2)
            }
            if let error = team.errors.first {
                Label(error, systemImage: "exclamationmark.triangle")
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.danger)
                    .lineLimit(2)
            }

            VStack(alignment: .leading, spacing: 6) {
                definition("Strategy") { NXBadge(team.strategy.label, tone: .accent) }
                definition("Orchestrator") {
                    Label(orchestratorName, systemImage: "cpu")
                        .nxText(NXType.footnote)
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                }
                definition("Members") {
                    if team.members.isEmpty {
                        Text("—").nx(NXType.footnote).foregroundStyle(NXColor.muted)
                    } else {
                        Text(team.members.map { ($0.role.isEmpty ? $0.agentID : $0.role) + ($0.count > 1 ? " ×\($0.count)" : "") }.joined(separator: ", "))
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg)
                            .lineLimit(2)
                    }
                }
            }

            NXDivider()
            HStack {
                NXOverline("Recent runs")
                Spacer()
                Label("\(team.members.count) roles · \(team.memberSlots) slots", systemImage: "person.2")
                    .nxText(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
            }
            if liveRuns.isEmpty {
                Text("No runs yet.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            } else {
                VStack(spacing: 0) {
                    ForEach(liveRuns.prefix(3), id: \.id) { run in
                        NavigationLink(value: TeamRunRoute(rootTaskID: run.id)) {
                            TeamRunLine(run: run)
                        }
                        .buttonStyle(NXPlainPressStyle())
                    }
                }
            }
        }
        .nxCard()
    }

    private var orchestratorName: String {
        if !team.orchestratorAgentID.isEmpty {
            return store.agent(team.orchestratorAgentID)?.name ?? team.orchestratorAgentID
        }
        return team.hasInlineOrchestrator ? "inline orchestrator" : "none (hub-driven)"
    }

    private func definition<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: NXSpace.s3) {
            Text(label)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
                .frame(width: 96, alignment: .leading)
            content()
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

/// disabled / N errors / N warnings badges.
struct TeamStatusBadges: View {
    let team: Nexus_V1_Team

    var body: some View {
        if !team.enabled || !team.errors.isEmpty || !team.warnings.isEmpty {
            HStack(spacing: 6) {
                if !team.enabled { NXBadge("disabled") }
                if !team.errors.isEmpty {
                    NXBadge("\(team.errors.count) \(team.errors.count == 1 ? "error" : "errors")", systemImage: "exclamationmark.triangle", tone: .danger)
                }
                if !team.warnings.isEmpty {
                    NXBadge("\(team.warnings.count) \(team.warnings.count == 1 ? "warning" : "warnings")", systemImage: "exclamationmark.triangle", tone: .warn)
                }
            }
        }
    }
}

/// One run line: state icon, title, cost, time (44 pt).
struct TeamRunLine: View {
    let run: Nexus_V1_Task

    var body: some View {
        let cost = max(run.hasTreeCost ? Int(run.treeCost.amount.minorUnits) : 0, run.costCents)
        HStack(spacing: NXSpace.s2) {
            Image(systemName: run.state.badgeSymbol)
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(run.state.tone.color)
                .accessibilityLabel(run.state.label)
            Text(run.displayTitle)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.fg2)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
            Text(Format.cost(cents: cost))
                .nx(NXType.caption.weight(.regular))
                .monospacedDigit()
                .foregroundStyle(NXColor.muted)
            if let created = run.createdAt.optionalDate {
                Text(Format.relative(created))
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
        }
        .frame(minHeight: NXSize.touchMin)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

// MARK: - Team detail

/// A team: run box, runs, orchestrator and strategy, members and limits (web TeamDetail).
struct TeamDetailView: View {
    let teamID: String

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(ToastCenter.self) private var toasts

    @State private var team: Nexus_V1_Team?
    @State private var markdown = ""
    @State private var runs: [Nexus_V1_Task] = []
    @State private var errorMessage: String?
    @State private var prompt = ""
    @State private var isStarting = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s6) {
                if let team {
                    header(team)
                    runBox(team)
                    runsCard
                    orchestratorCard(team)
                    membersCard(team)
                    limitsCard(team)
                    if !markdown.isEmpty {
                        VStack(alignment: .leading, spacing: NXSpace.s2) {
                            NXOverline("Mission & instructions")
                            MarkdownText(markdown: markdown)
                                .nxText(NXType.body)
                                .foregroundStyle(NXColor.fg2)
                        }
                        .nxCard()
                    }
                } else if let errorMessage {
                    NXErrorState(message: errorMessage) { Task { await load() } }
                } else {
                    NXSkeleton(height: 44, width: 240)
                    NXSkeleton(height: 120, cornerRadius: NXRadius.lg)
                    NXSkeletonRows(rows: 3)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(team?.name ?? "Team")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            let result = try await store.team(id: teamID)
            team = result.team
            markdown = result.markdown
            runs = (try? await store.teamRuns(teamID: teamID, limit: 50)) ?? runs
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func header(_ team: Nexus_V1_Team) -> some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(alignment: .top, spacing: NXSpace.s3) {
                TeamIconView(symbol: team.symbolName, size: 48)
                VStack(alignment: .leading, spacing: NXSpace.s1) {
                    Text(team.name.isEmpty ? team.id : team.name)
                        .nx(NXType.title1)
                        .foregroundStyle(NXColor.fg)
                        .accessibilityAddTraits(.isHeader)
                    Text("teams/\(team.id).md · v\(team.version)")
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.muted)
                    HStack(spacing: 6) {
                        NXBadge(team.strategy.label, tone: .accent)
                        if team.blackboard { NXBadge("blackboard", systemImage: "note.text") }
                        if team.bundleApprovals { NXBadge("bundled approvals", systemImage: "hand.raised") }
                    }
                }
            }
            TeamStatusBadges(team: team)
            if !team.description_p.isEmpty {
                Text(team.description_p)
                    .nx(NXType.bodyLg)
                    .foregroundStyle(NXColor.fg2)
            }
            ForEach(team.errors, id: \.self) { NXInlineError($0) }
            ForEach(team.warnings, id: \.self) { warning in
                Label(warning, systemImage: "exclamationmark.triangle")
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.warn)
            }
        }
    }

    private func runBox(_ team: Nexus_V1_Team) -> some View {
        let blocked = !team.errors.isEmpty ? "This team has errors; fix the note first." : (!team.enabled ? "This team is disabled." : "")
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            Label("Run this team", systemImage: "play.fill")
                .nxText(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
            NXTextField(
                placeholder: team.triggers.first.map { "e.g. \"\($0): …\"" } ?? "What should the team do?",
                text: $prompt,
                axis: .vertical
            )
            .disabled(!blocked.isEmpty)
            Text(blocked.isEmpty ? "Creates a task for the team; \(team.orchestratorAgentID.isEmpty ? "the orchestrator" : team.orchestratorAgentID) plans and delegates within the limits." : blocked)
                .nx(NXType.caption.weight(.regular))
                .foregroundStyle(blocked.isEmpty ? NXColor.muted : NXColor.danger)
            NXButton("Run team", systemImage: "play.fill", kind: .primary, isLoading: isStarting, fullWidth: true) {
                start(team)
            }
            .disabled(prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !blocked.isEmpty)
        }
        .nxCard()
    }

    private func start(_ team: Nexus_V1_Team) {
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        isStarting = true
        Task {
            defer { isStarting = false }
            do {
                let task = try await store.runTeam(teamID: team.id, prompt: text)
                prompt = ""
                toasts.success("Team run started")
                runs.insert(task, at: 0)
                router.workPath.append(TeamRunRoute(rootTaskID: task.id))
            } catch {
                toasts.error("Couldn't start the team", error) { start(team) }
            }
        }
    }

    private var runsCard: some View {
        let live = runs.map { store.tasks[$0.id] ?? $0 }
        let total = live.reduce(0) { $0 + max($1.hasTreeCost ? Int($1.treeCost.amount.minorUnits) : 0, $1.costCents) }
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Runs", count: live.count)
            if live.isEmpty {
                NXEmptyState(systemImage: "clock.arrow.circlepath", title: "No runs yet", message: "Run the team above; its runs appear here live.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(live.enumerated()), id: \.element.id) { index, run in
                        NavigationLink(value: TeamRunRoute(rootTaskID: run.id)) {
                            HStack(spacing: NXSpace.s3) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(run.displayTitle)
                                        .nx(NXType.body.weight(.medium))
                                        .foregroundStyle(NXColor.fg)
                                        .lineLimit(1)
                                    Text([Format.relative(run.createdAt.optionalDate), run.phase.replacingOccurrences(of: "_", with: " ")].filter { !$0.isEmpty }.joined(separator: " · "))
                                        .nx(NXType.caption.weight(.regular))
                                        .foregroundStyle(NXColor.muted)
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                                TaskStateBadge(state: run.state)
                                NXChevron()
                            }
                            .padding(.horizontal, NXSpace.s4)
                            .frame(minHeight: 56)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(NXPlainPressStyle())
                        if index < live.count - 1 { NXDivider() }
                    }
                }
                .nxCard(padding: 0)
                Text("\(live.count) \(live.count == 1 ? "run" : "runs") · \(Format.cost(cents: total)) in total")
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
            }
        }
    }

    private func orchestratorCard(_ team: Nexus_V1_Team) -> some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Orchestrator")
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                Text("\(team.strategy.label) — \(team.strategy.hint)")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                if !team.orchestratorAgentID.isEmpty {
                    NavigationLink(value: AgentRoute(id: team.orchestratorAgentID)) {
                        NXRow(store.agent(team.orchestratorAgentID)?.name ?? team.orchestratorAgentID, subtitle: "\(store.agent(team.orchestratorAgentID)?.runtime ?? "agent") · orchestrator", leading: {
                            AgentIconView(agent: store.agent(team.orchestratorAgentID), agentID: team.orchestratorAgentID, size: 32)
                        }, trailing: { NXChevron() })
                        .padding(.horizontal, -NXSpace.s4)
                    }
                    .buttonStyle(NXPlainPressStyle())
                } else if team.hasInlineOrchestrator {
                    Text("Inline orchestrator" + (team.inlineOrchestrator.model.isEmpty ? "" : " · \(team.inlineOrchestrator.model)"))
                        .nx(NXType.body.weight(.medium))
                        .foregroundStyle(NXColor.fg)
                    Text(team.inlineOrchestrator.instructions)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.fg2)
                        .lineLimit(6)
                } else {
                    Text("No orchestrator: the hub runs the \(team.strategy.label) pattern.")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
            }
            .nxCard()
        }
    }

    private func membersCard(_ team: Nexus_V1_Team) -> some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Members", count: team.members.count)
            if team.members.isEmpty {
                NXEmptyState(systemImage: "person.2", title: "No members", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(team.members.enumerated()), id: \.offset) { index, member in
                        NavigationLink(value: AgentRoute(id: member.agentID)) {
                            NXRow(
                                member.role.isEmpty ? member.agentID : member.role,
                                subtitle: [store.agent(member.agentID)?.name ?? member.agentID, member.when].filter { !$0.isEmpty }.joined(separator: " — "),
                                minHeight: 56,
                                leading: { AgentIconView(agent: store.agent(member.agentID), agentID: member.agentID, size: 32) },
                                trailing: {
                                    HStack(spacing: 6) {
                                        if member.count > 1 {
                                            NXBadge("×\(member.count)")
                                                .accessibilityLabel("Worker pool of \(member.count)")
                                        }
                                        NXChevron()
                                    }
                                }
                            )
                        }
                        .buttonStyle(NXPlainPressStyle())
                        if index < team.members.count - 1 { NXDivider(inset: 60) }
                    }
                }
                .nxCard(padding: 0)
            }
        }
    }

    private func limitsCard(_ team: Nexus_V1_Team) -> some View {
        let limits = TeamLimitsSummary.items(team.limits)
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Limits")
            VStack(alignment: .leading, spacing: 0) {
                if limits.isEmpty {
                    Text("Hub defaults.")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                } else {
                    ForEach(Array(limits.enumerated()), id: \.offset) { index, item in
                        NXKeyValueRow(item.label, value: item.value)
                        if index < limits.count - 1 { NXDivider() }
                    }
                }
                Text("Enforced by the hub for the whole run.")
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .padding(.top, NXSpace.s2)
            }
            .nxCard()
        }
    }
}

enum TeamLimitsSummary {
    /// web limitsSummary.
    static func items(_ limits: Nexus_V1_TeamLimits) -> [(label: String, value: String)] {
        var out: [(label: String, value: String)] = []
        if limits.maxParallel > 0 { out.append(("Parallel", "\(limits.maxParallel)")) }
        if limits.maxTasks > 0 { out.append(("Tasks", "\(limits.maxTasks)")) }
        if limits.maxDepth > 0 { out.append(("Depth", "\(limits.maxDepth)")) }
        if limits.maxRuns > 0 { out.append(("Runs", "\(limits.maxRuns)")) }
        if limits.hasMaxCost { out.append(("Budget", limits.maxCost.formatted)) }
        if limits.timeoutSeconds > 0 { out.append(("Timeout", humanSeconds(Int(limits.timeoutSeconds)))) }
        return out
    }

    static func humanSeconds(_ s: Int) -> String {
        if s >= 3600 { return "\(s / 3600)h" + (s % 3600 > 0 ? " \((s % 3600) / 60)m" : "") }
        if s >= 60 { return "\(s / 60)m" }
        return "\(s)s"
    }
}
