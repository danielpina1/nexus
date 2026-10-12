import NexusCore
import SwiftUI

struct HomeView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        NavigationStack {
            List {
                if store.reachability.isUnavailable && !store.isDemo {
                    Section {
                        HubUnreachableBanner()
                    }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }

                if let error = store.lastError {
                    Section {
                        ErrorBanner(message: error) { store.lastError = nil }
                    }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }

                if let brief = store.notices.first(where: { $0.notice.kind == .brief }) {
                    Section {
                        BriefCard(notice: brief.notice)
                    }
                }

                Section {
                    if store.inbox.isEmpty {
                        Label("Nothing needs you right now.", systemImage: "checkmark.circle")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(store.inbox.filter { item in
                        if case .notice(let n) = item { return n.notice.kind != .brief }
                        return true
                    }) { item in
                        InboxCard(item: item)
                    }
                } header: {
                    HStack {
                        Text("Inbox")
                        Spacer()
                        Text(Format.cost(cents: store.costCentsToday) + " today")
                            .font(.caption)
                    }
                }

                if !store.runningTasks.isEmpty {
                    Section("Running") {
                        ForEach(store.runningTasks, id: \.id) { task in
                            Button {
                                router.openTask(task.id)
                            } label: {
                                RunningTaskRow(task: task)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }

                Section("Machines") {
                    MachinesStrip()
                        .listRowInsets(EdgeInsets(top: 8, leading: 0, bottom: 8, trailing: 0))
                }

                if !store.agents.isEmpty {
                    Section("Favourites") {
                        FavouritesGrid()
                    }
                }
            }
            .navigationTitle("Nexus")
            .toolbar { CommonToolbar() }
            .refreshable { await store.refresh() }
        }
    }
}

struct BriefCard: View {
    let notice: Nexus_V1_Notice

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(notice.title.isEmpty ? "Brief" : notice.title, systemImage: "sun.max.fill")
                .font(.headline)
                .foregroundStyle(.orange)
            MarkdownText(markdown: notice.body)
                .font(.subheadline)
        }
        .padding(.vertical, 4)
    }
}

struct InboxCard: View {
    let item: InboxItem

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var replyText = ""
    @State private var errorMessage: String?

    var body: some View {
        switch item {
        case .approval(let approval):
            Button {
                router.openApproval(approval.id)
            } label: {
                HStack(spacing: 12) {
                    AgentIconView(agent: store.agent(approval.agentID), size: 36)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(approval.title).font(.subheadline.weight(.semibold))
                        Text(approval.summary).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                        if let expires = approval.expiresDate {
                            Text("Expires \(Format.relative(expires))").font(.caption2).foregroundStyle(.orange)
                        }
                    }
                    Spacer()
                    Image(systemName: approval.isBiometric ? "faceid" : "hand.raised.fill")
                        .foregroundStyle(.orange)
                }
            }
            .buttonStyle(.plain)

        case .question(let question):
            VStack(alignment: .leading, spacing: 8) {
                Label(question.question.text, systemImage: "questionmark.bubble.fill")
                    .font(.subheadline.weight(.semibold))
                if !question.question.choices.isEmpty {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack {
                            ForEach(question.question.choices, id: \.self) { choice in
                                Button(choice) { answer(question, choice: choice) }
                                    .buttonStyle(.bordered)
                                    .controlSize(.small)
                            }
                        }
                    }
                }
                if question.question.allowFreeText || question.question.choices.isEmpty {
                    HStack {
                        TextField("Reply…", text: $replyText)
                            .textFieldStyle(.roundedBorder)
                        Button("Send") { answer(question, text: replyText) }
                            .disabled(replyText.isEmpty)
                    }
                }
                if let errorMessage {
                    Text(errorMessage).font(.caption).foregroundStyle(.red)
                }
            }
            .padding(.vertical, 4)

        case .notice(let notice):
            Button {
                if !notice.taskID.isEmpty { router.openTask(notice.taskID) }
            } label: {
                HStack(alignment: .top, spacing: 12) {
                    Image(systemName: Self.symbol(for: notice.notice.kind))
                        .foregroundStyle(notice.notice.kind == .alert ? Color.red : Color.accentColor)
                        .frame(width: 24)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(notice.notice.title).font(.subheadline.weight(.semibold))
                        Text(notice.notice.body).font(.caption).foregroundStyle(.secondary).lineLimit(3)
                    }
                }
            }
            .buttonStyle(.plain)
            .swipeActions {
                Button("Dismiss") { store.dismissNotice(notice.id) }
            }
        }
    }

    private func answer(_ question: QuestionItem, choice: String? = nil, text: String = "") {
        Task {
            do {
                try await store.sendMessage(
                    taskID: question.taskID,
                    text: choice ?? text,
                    questionID: question.id,
                    choice: choice
                )
                replyText = ""
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    static func symbol(for kind: Nexus_V1_NoticeKind) -> String {
        switch kind {
        case .result: return "checkmark.seal.fill"
        case .alert: return "exclamationmark.triangle.fill"
        case .suggestion: return "lightbulb.fill"
        case .factReview: return "brain.head.profile"
        case .brief: return "sun.max.fill"
        default: return "bell.fill"
        }
    }
}

struct RunningTaskRow: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store

    var body: some View {
        HStack(spacing: 12) {
            AgentIconView(agent: store.agent(task.agentID), size: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text(task.displayTitle).font(.subheadline).lineLimit(1)
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    if let node = store.node(task.nodeID) {
                        Text("· \(node.name)").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            Spacer()
            Text(Format.cost(cents: task.costCents)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
        }
        .contentShape(Rectangle())
    }
}

struct MachinesStrip: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(store.nodes, id: \.id) { node in
                    let status = store.status(ofNode: node.id)
                    Button {
                        router.tab = .machines
                        router.machinesPath = [node.id]
                    } label: {
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 6) {
                                Circle()
                                    .fill(status?.online == true ? Color.green : Color.gray)
                                    .frame(width: 8, height: 8)
                                Text(node.name).font(.subheadline.weight(.semibold))
                            }
                            let runs = Int(status?.activeRuns ?? 0)
                            Text(status?.online == true ? "CPU \(Int(status?.cpuPercent ?? 0))% · \(runs) run\(runs == 1 ? "" : "s")" : "offline")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                        .padding(10)
                        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal)
        }
    }
}

struct FavouritesGrid: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var status: String?

    private let columns = [GridItem(.flexible()), GridItem(.flexible())]

    var body: some View {
        VStack(alignment: .leading) {
            LazyVGrid(columns: columns, spacing: 10) {
                ForEach(store.agents.filter(\.enabled).prefix(6), id: \.id) { agent in
                    Button {
                        run(agent)
                    } label: {
                        HStack {
                            AgentIconView(agent: agent, size: 24)
                            Text(agent.name).font(.caption.weight(.semibold)).lineLimit(1)
                            Spacer(minLength: 0)
                        }
                        .padding(8)
                        .frame(maxWidth: .infinity)
                        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
                    }
                    .buttonStyle(.plain)
                }
            }
            if let status {
                Text(status).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private func run(_ agent: Nexus_V1_Agent) {
        Task {
            do {
                switch try await store.submit("Run \(agent.name)", agentID: agent.id, source: "ios-favourite") {
                case .sent(let response):
                    status = nil
                    if response.hasTask { router.openTask(response.task.id) }
                case .queued:
                    status = "Queued — \(agent.name) runs when your Nexus is reachable."
                }
            } catch {
                status = error.localizedDescription
            }
        }
    }
}

#Preview {
    HomeView()
        .environment(NexusStore.preview())
        .environment(AppRouter())
}
