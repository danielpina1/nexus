import ActivityKit
import NexusCore
import SwiftUI

/// Work → Tasks (web Tasks): running / scheduled / done with a search field.
struct TasksSection: View {
    @Environment(NexusStore.self) private var store

    enum Segment: String, CaseIterable, Identifiable {
        case running = "Running"
        case scheduled = "Scheduled"
        case done = "Done"
        var id: String { rawValue }
    }

    @State private var segment: Segment = .running
    @State private var query = ""

    private var tasks: [Nexus_V1_Task] {
        let list: [Nexus_V1_Task]
        switch segment {
        case .running: list = store.runningTasks.filter { $0.parentID.isEmpty }
        case .scheduled: list = store.scheduledTasks
        case .done: list = store.doneTasks.filter { $0.parentID.isEmpty }
        }
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return list }
        return list.filter { task in
            task.displayTitle.lowercased().contains(q)
                || (store.agent(task.agentID)?.name.lowercased().contains(q) ?? false)
        }
    }

    var body: some View {
        let list = tasks
        VStack(alignment: .leading, spacing: NXSpace.s4) {
            NXPageHeader("Tasks", subtitle: "\(list.count) \(list.count == 1 ? "task" : "tasks") · \(Format.cost(cents: list.reduce(0) { $0 + $1.costCents })) spent in view")
            Picker("Show", selection: $segment) {
                ForEach(Segment.allCases) { segment in
                    Text(segment.rawValue).tag(segment)
                }
            }
            .pickerStyle(.segmented)
            NXTextField(placeholder: "Search tasks…", text: $query)
            if list.isEmpty {
                NXEmptyState(
                    systemImage: query.isEmpty ? "tray" : "magnifyingglass",
                    title: query.isEmpty ? (segment == .scheduled ? "Nothing scheduled" : "No tasks") : "No tasks match",
                    message: query.isEmpty ? (segment == .running ? "Ask Nexus for something in Chat." : nil) : "Try a different search.",
                    compact: true
                )
                .nxCard(padding: 0)
            } else {
                TaskList(tasks: list)
            }
        }
    }
}

struct TaskDetailView: View {
    let taskID: String

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var liveError: String?
    @State private var isFollowing = false
    @State private var loaded = false

    var body: some View {
        Group {
            if let task = store.tasks[taskID] {
                ScrollView {
                    VStack(alignment: .leading, spacing: NXSpace.s6) {
                        header(task)
                        resultSection(task)
                        subtasksSection(task)
                        RunControls(task: task)
                            .nxCard()
                        if task.state.isActive {
                            VStack(alignment: .leading, spacing: NXSpace.s2) {
                                NXButton(isFollowing ? "Following live" : "Follow live", systemImage: "livephoto", kind: .primary) {
                                    follow(task)
                                }
                                .disabled(isFollowing)
                                .accessibilityHint("Shows this run on the Lock Screen and in the Dynamic Island.")
                                if let liveError {
                                    NXInlineError(liveError)
                                }
                            }
                        }
                        if !task.teamID.isEmpty && task.parentID.isEmpty {
                            NavigationLink(value: TeamRunRoute(rootTaskID: task.id)) {
                                Label("Open team run", systemImage: "person.3")
                            }
                            .buttonStyle(NXButtonStyle(kind: .secondary))
                        }


                        VStack(alignment: .leading, spacing: NXSpace.s3) {
                            NXSectionHeader("Timeline")
                            RunTimelineView(task: task)
                        }
                    }
                    .padding(NXSpace.s4)
                }
                .nxScreenBackground()
                .safeAreaInset(edge: .bottom) {
                    if task.state.isActive {
                        ComposerView(agentID: task.agentID, replyTaskID: task.id)
                    }
                }
                .navigationTitle(task.displayTitle)
                .navigationBarTitleDisplayMode(.inline)
            } else if loaded {
                NXEmptyState(systemImage: "questionmark.folder", title: "Task not found", message: "It may have been removed, or it belongs to another Hub.")
                    .nxScreenBackground()
            } else {
                NXLoadingState(label: "Loading task…")
                    .nxScreenBackground()
            }
        }
        .task {
            await store.loadTask(taskID)
            loaded = true
            isFollowing = AppEnvironment.shared.liveActivities.activity(for: taskID) != nil
        }
    }

    @ViewBuilder
    private func header(_ task: Nexus_V1_Task) -> some View {
        HStack(alignment: .top, spacing: NXSpace.s3) {
            AgentIconView(agent: store.agent(task.agentID), agentID: task.agentID, size: 44)
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                Text(task.displayTitle)
                    .nx(NXType.title2)
                    .foregroundStyle(NXColor.fg)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                Text([
                    store.agent(task.agentID)?.name ?? (task.agentID.isEmpty ? "Routing…" : task.agentID),
                    store.node(task.nodeID)?.name,
                ].compactMap { $0 }.joined(separator: " · "))
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
                engineLine(task)
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    if !task.role.isEmpty {
                        NXBadge(task.role)
                    }
                }
            }
        }
    }

    /// Engine, exact model and cost ("Claude Code · claude-sonnet-4-5-20250929 · €0.31 ·
    /// 12.4k → 860 tokens"); the agent's model alias in the accessibility hint.
    @ViewBuilder
    private func engineLine(_ task: Nexus_V1_Task) -> some View {
        let model = RunModel.from(store.timelines[task.id] ?? [], task: task)
        let alias = store.agent(task.agentID)?.model ?? ""
        let tokens = task.hasCost && (task.cost.inputTokens > 0 || task.cost.outputTokens > 0)
            ? "\(Self.compact(task.cost.inputTokens)) → \(Self.compact(task.cost.outputTokens)) tokens" : ""
        let tree = task.hasTreeCost && task.treeCost.amount.minorUnits > Int64(task.costCents)
            ? "tree " + Format.cost(cents: Int(task.treeCost.amount.minorUnits)) : ""
        let parts = [model.label, Format.cost(cents: task.costCents), tree, tokens].filter { !$0.isEmpty }
        Text(parts.joined(separator: " · "))
            .nx(NXType.caption.weight(.regular))
            .monospacedDigit()
            .foregroundStyle(NXColor.fg2)
            .help(alias.isEmpty ? "" : "Agent model alias: \(alias)")
            .accessibilityHint(alias.isEmpty ? "" : "Agent model alias: \(alias)")
    }

    static func compact(_ n: Int64) -> String {
        n >= 1000 ? String(format: "%.1fk", Double(n) / 1000) : "\(n)"
    }

    /// Result first (web task page): the answer so far / result / what went wrong.
    @ViewBuilder
    private func resultSection(_ task: Nexus_V1_Task) -> some View {
        let items = ChatItem.build(store.timelines[task.id] ?? [])
        let answer = items.last(where: \.isText).map { item -> String in
            if case .text(let text, _) = item.kind { return text.trimmingCharacters(in: .whitespacesAndNewlines) }
            return ""
        } ?? ""
        let active = task.state.isActive
        let failed = task.state == .failed
        let shown = answer.isEmpty ? task.summary : answer
        let runningKids = store.children(of: task.id).filter { $0.state.isActive }.count
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            NXOverline(active ? "Answer so far" : (failed ? "What went wrong" : "Result"))
            if failed {
                Text(task.summary.isEmpty ? "The run failed." : task.summary)
                    .nx(NXType.body)
                    .foregroundStyle(NXColor.danger)
                    .textSelection(.enabled)
                    .padding(NXSpace.s3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
            } else if !shown.isEmpty {
                ChatMarkdown(text: shown, streaming: active)
                    .nxCard()
            } else if active {
                ThinkingLabel(since: task.createdAt.date, label: "Working…")
            } else {
                Text("No answer recorded.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            if BackgroundClaim.untracked(answer: shown, taskActive: active, activeChildren: runningKids) {
                BackgroundWarningView()
            }
        }
    }

    /// "N sub-tasks still running" / "Nothing is still running for this task", then the list.
    @ViewBuilder
    private func subtasksSection(_ task: Nexus_V1_Task) -> some View {
        let children = store.children(of: task.id)
        let running = children.filter { $0.state.isActive }.count
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            NXOverline(children.isEmpty ? "Sub-tasks" : "Sub-tasks (\(children.count))")
            HStack(spacing: NXSpace.s2) {
                if running > 0 || task.state.isActive {
                    ProgressView().controlSize(.small).accessibilityHidden(true)
                } else {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(NXColor.ok)
                        .accessibilityHidden(true)
                }
                Text(
                    running > 0 ? "\(running) sub-task\(running == 1 ? "" : "s") still running"
                        : task.state.isActive ? (task.phase == "waiting_children" ? "Waiting for sub-tasks to finish" : "This task is still running" + (children.isEmpty ? "" : "; its sub-tasks are done"))
                        : "Nothing is still running for this task"
                )
                .nx(NXType.footnote)
                .foregroundStyle(running > 0 ? NXColor.info : NXColor.fg2)
            }
            .accessibilityElement(children: .combine)
            if !children.isEmpty {
                TaskList(tasks: children)
            }
        }
    }

    private func follow(_ task: Nexus_V1_Task) {
        do {
            try AppEnvironment.shared.liveActivities.follow(
                task: task,
                agent: store.agent(task.agentID),
                nodeName: store.node(task.nodeID)?.name ?? "hub"
            )
            isFollowing = true
            liveError = nil
        } catch {
            liveError = AppEnvironment.shared.liveActivities.areActivitiesEnabled
                ? error.localizedDescription
                : "Live Activities are turned off for Nexus in Settings."
        }
    }
}

#Preview {
    NavigationStack {
        TaskDetailView(taskID: "tsk_fix")
            .nexusDestinations()
    }
    .environment(NexusStore.preview())
    .environment(AppRouter())
    .environment(ToastCenter())
}
