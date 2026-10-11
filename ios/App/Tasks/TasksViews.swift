import ActivityKit
import NexusCore
import SwiftUI

struct TasksView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    enum Segment: String, CaseIterable, Identifiable {
        case running = "Running"
        case scheduled = "Scheduled"
        case done = "Done"
        var id: String { rawValue }
    }

    @State private var segment: Segment = .running

    private var tasks: [Nexus_V1_Task] {
        switch segment {
        case .running: return store.runningTasks.filter { $0.parentID.isEmpty }
        case .scheduled: return store.scheduledTasks
        case .done: return store.doneTasks.filter { $0.parentID.isEmpty }
        }
    }

    var body: some View {
        @Bindable var router = router
        NavigationStack(path: $router.tasksPath) {
            List {
                Picker("Show", selection: $segment) {
                    ForEach(Segment.allCases) { segment in
                        Text(segment.rawValue).tag(segment)
                    }
                }
                .pickerStyle(.segmented)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())

                if tasks.isEmpty {
                    Text(segment == .scheduled ? "Nothing scheduled." : "No tasks.")
                        .foregroundStyle(.secondary)
                }
                ForEach(tasks, id: \.id) { task in
                    NavigationLink(value: task.id) {
                        TaskRow(task: task)
                    }
                }
            }
            .navigationTitle("Tasks")
            .toolbar { CommonToolbar() }
            .refreshable { await store.refresh() }
            .navigationDestination(for: String.self) { taskID in
                TaskDetailView(taskID: taskID)
            }
        }
    }
}

struct TaskRow: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store

    var body: some View {
        HStack(spacing: 12) {
            AgentIconView(agent: store.agent(task.agentID), size: 32)
            VStack(alignment: .leading, spacing: 3) {
                Text(task.displayTitle).font(.subheadline).lineLimit(2)
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    if let date = task.updatedAt.optionalDate {
                        Text("· \(Format.relative(date))").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            Spacer()
            Text(Format.cost(cents: task.costCents)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
        }
    }
}

struct TaskDetailView: View {
    let taskID: String

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var liveError: String?
    @State private var isFollowing = false

    var body: some View {
        Group {
            if let task = store.tasks[taskID] {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        header(task)
                        RunControls(task: task)
                        if task.state.isActive {
                            Button {
                                follow(task)
                            } label: {
                                Label(isFollowing ? "Following live" : "Follow live", systemImage: "livephoto")
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(isFollowing)
                            if let liveError {
                                Text(liveError).font(.caption).foregroundStyle(.red)
                            }
                        }

                        let children = store.children(of: task.id)
                        if !children.isEmpty {
                            Text("Subtasks").font(.headline)
                            ForEach(children, id: \.id) { child in
                                NavigationLink(value: child.id) {
                                    TaskRow(task: child)
                                }
                                .buttonStyle(.plain)
                            }
                        }

                        Text("Timeline").font(.headline)
                        RunTimelineView(task: task)
                    }
                    .padding()
                }
                .safeAreaInset(edge: .bottom) {
                    if task.state.isActive {
                        ComposerView(agentID: task.agentID, replyTaskID: task.id)
                    }
                }
                .navigationTitle(task.displayTitle)
                .navigationBarTitleDisplayMode(.inline)
            } else {
                ProgressView()
            }
        }
        .task {
            await store.loadTask(taskID)
            isFollowing = AppEnvironment.shared.liveActivities.activity(for: taskID) != nil
        }
    }

    @ViewBuilder
    private func header(_ task: Nexus_V1_Task) -> some View {
        HStack(spacing: 12) {
            AgentIconView(agent: store.agent(task.agentID), size: 44)
            VStack(alignment: .leading, spacing: 2) {
                Text(store.agent(task.agentID)?.name ?? (task.agentID.isEmpty ? "Routing…" : task.agentID))
                    .font(.headline)
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    if let node = store.node(task.nodeID) {
                        Text("· \(node.name)").font(.caption).foregroundStyle(.secondary)
                    }
                    Text("· \(Format.cost(cents: task.costCents))").font(.caption).foregroundStyle(.secondary)
                }
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
    TasksView()
        .environment(NexusStore.preview())
        .environment(AppRouter())
}
