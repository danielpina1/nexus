import NexusCore
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

/// A chat thread: the auto-routed "Nexus" thread (agentID == nil) or one agent.
struct ChatThread: Hashable {
    let agentID: String?
}

struct ChatListView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var path: [ChatThread] = []

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if store.reachability.isUnavailable && !store.isDemo {
                    Section {
                        HubUnreachableBanner()
                    }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }
                Section {
                    NavigationLink(value: ChatThread(agentID: nil)) {
                        HStack(spacing: 12) {
                            Image(systemName: "circle.hexagongrid.fill")
                                .font(.title2)
                                .foregroundStyle(.tint)
                                .frame(width: 36)
                            VStack(alignment: .leading) {
                                Text("Nexus").font(.headline)
                                Text("Routed automatically to the right agent").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                Section("Agents") {
                    ForEach(store.agents, id: \.id) { agent in
                        NavigationLink(value: ChatThread(agentID: agent.id)) {
                            HStack(spacing: 12) {
                                AgentIconView(agent: agent, size: 36)
                                VStack(alignment: .leading) {
                                    Text(agent.name).font(.headline)
                                    Text(agent.description_p).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Chat")
            .toolbar { CommonToolbar() }
            .navigationDestination(for: ChatThread.self) { thread in
                ChatThreadView(thread: thread)
            }
            .onChange(of: router.focusComposer) { _, focus in
                if focus {
                    path = [ChatThread(agentID: nil)]
                }
            }
        }
    }
}

struct ChatThreadView: View {
    let thread: ChatThread

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    private var title: String {
        thread.agentID.flatMap { store.agent($0)?.name } ?? "Nexus"
    }

    private var tasks: [Nexus_V1_Task] {
        store.chatTasks(forAgent: thread.agentID)
    }

    /// Commands typed in this thread while the Hub was unreachable.
    private var queuedCommands: [OutboxItem] {
        store.outbox.filter { item in
            if case .command(_, let agentID, _) = item.kind { return agentID == thread.agentID }
            return false
        }
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    if tasks.isEmpty && queuedCommands.isEmpty {
                        ContentUnavailableView(
                            "Start a conversation",
                            systemImage: "bubble.left.and.text.bubble.right",
                            description: Text(thread.agentID == nil ? "Ask anything — Nexus routes it to the right agent." : "Send \(title) a task.")
                        )
                        .padding(.top, 40)
                    }
                    ForEach(tasks, id: \.id) { task in
                        VStack(alignment: .leading, spacing: 8) {
                            RunTimelineView(task: task, compact: true)
                            RunControls(task: task)
                        }
                        .id(task.id)
                        Divider()
                    }
                    ForEach(queuedCommands) { item in
                        QueuedBubble(text: item.summary)
                    }
                }
                .padding()
            }
            .onAppear {
                if let last = tasks.last { proxy.scrollTo(last.id, anchor: .bottom) }
            }
            .onChange(of: tasks.last?.id) { _, id in
                if let id { withAnimation { proxy.scrollTo(id, anchor: .bottom) } }
            }
        }
        .safeAreaInset(edge: .top) {
            if store.reachability.isUnavailable && !store.isDemo {
                HubUnreachableBanner()
                    .padding(.horizontal)
                    .padding(.top, 4)
            }
        }
        .safeAreaInset(edge: .bottom) {
            ComposerView(agentID: thread.agentID, replyTaskID: nil)
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Reply / Try harder / Stop + cost for a task.
struct RunControls: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store
    @State private var showReply = false
    @State private var replyText = ""
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                TaskStateBadge(state: task.state)
                Spacer()
                Text(Format.cost(cents: task.costCents)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
            if task.state.isActive {
                HStack {
                    Button {
                        showReply.toggle()
                    } label: {
                        Label("Reply", systemImage: "arrowshape.turn.up.left")
                    }
                    Button {
                        perform { try await store.escalate(taskID: task.id) }
                    } label: {
                        Label("Try harder", systemImage: "bolt.fill")
                    }
                    Button(role: .destructive) {
                        perform { try await store.cancel(taskID: task.id) }
                    } label: {
                        Label("Stop", systemImage: "stop.fill")
                    }
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
            } else if task.state == .failed {
                Button {
                    perform { try await store.escalate(taskID: task.id) }
                } label: {
                    Label("Retry harder", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
            }
            if showReply {
                HStack {
                    TextField("Message the agent…", text: $replyText, axis: .vertical)
                        .textFieldStyle(.roundedBorder)
                    Button("Send") {
                        let text = replyText
                        replyText = ""
                        showReply = false
                        perform { _ = try await store.sendMessage(taskID: task.id, text: text) }
                    }
                    .disabled(replyText.isEmpty)
                }
            }
            if let errorMessage {
                Text(errorMessage).font(.caption).foregroundStyle(.red)
            }
        }
    }

    private func perform(_ action: @escaping () async throws -> Void) {
        Task {
            do {
                try await action()
                errorMessage = nil
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}

/// Live run view: streaming text, collapsible tool calls, approvals, questions, artifacts, cost.
struct RunTimelineView: View {
    let task: Nexus_V1_Task
    var compact: Bool = false

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        let entries = store.timelines[task.id] ?? []
        VStack(alignment: .leading, spacing: 10) {
            if entries.isEmpty || !entries.contains(where: { if case .userMessage = $0.kind { return true }; return false }) {
                UserBubble(text: task.prompt.isEmpty ? task.displayTitle : task.prompt)
            }
            ForEach(entries) { entry in
                entryView(entry)
            }
            if task.state.isTerminal && !task.summary.isEmpty {
                Label(task.summary, systemImage: task.state == .succeeded ? "checkmark.seal.fill" : "xmark.octagon")
                    .font(.subheadline)
                    .foregroundStyle(task.state == .succeeded ? Color.green : Color.red)
            }
        }
    }

    @ViewBuilder
    private func entryView(_ entry: TimelineItem) -> some View {
        switch entry.kind {
        case .userMessage(let text):
            UserBubble(text: text)
        case .output(let text, let isFinal):
            HStack(alignment: .top, spacing: 8) {
                AgentIconView(agent: store.agent(task.agentID), size: 24)
                VStack(alignment: .leading, spacing: 4) {
                    MarkdownText(markdown: text)
                        .font(.body)
                        .textSelection(.enabled)
                    if !isFinal && task.state.isActive {
                        ProgressView().controlSize(.mini)
                    }
                }
            }
        case .toolCall(let call, let result):
            ToolCallView(call: call, result: result)
        case .question(let question):
            Label(question.text, systemImage: "questionmark.bubble.fill")
                .font(.subheadline)
                .foregroundStyle(.orange)
        case .approval(let approval):
            Button {
                router.openApproval(approval.id)
            } label: {
                Label(
                    approval.state == .pending ? "Approval needed: \(approval.title)" : "\(approval.title) — \(Self.label(approval.state))",
                    systemImage: approval.isBiometric ? "faceid" : "hand.raised.fill"
                )
                .font(.subheadline.weight(.semibold))
            }
            .disabled(approval.state != .pending)
        case .cost(let cost):
            if !compact {
                Text("\(cost.hasAmount ? cost.amount.formatted : "") · \(cost.model) · \(cost.inputTokens)→\(cost.outputTokens) tokens")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        case .artifact(let attachment):
            ArtifactView(attachment: attachment)
        case .status(let state, _):
            if !compact {
                Label(state.label, systemImage: state.symbol)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        case .notice(let notice):
            Label(notice.title, systemImage: "bell")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    static func label(_ state: Nexus_V1_ApprovalState) -> String {
        switch state {
        case .approved: return "approved"
        case .denied: return "denied"
        case .expired: return "expired"
        default: return "pending"
        }
    }
}

struct UserBubble: View {
    let text: String

    var body: some View {
        HStack {
            Spacer(minLength: 40)
            Text(text)
                .padding(10)
                .background(Color.accentColor.opacity(0.15), in: RoundedRectangle(cornerRadius: 14))
        }
    }
}

/// A command waiting in the outbox (typed while the Hub was unreachable).
struct QueuedBubble: View {
    let text: String

    var body: some View {
        VStack(alignment: .trailing, spacing: 4) {
            UserBubble(text: text)
                .opacity(0.6)
            Label("Waiting for your Nexus — sent when you're connected", systemImage: "clock")
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

struct ToolCallView: View {
    let call: Nexus_V1_ToolCall
    let result: Nexus_V1_ToolResult?
    @State private var expanded = false

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 6) {
                Text("Input").font(.caption.weight(.semibold))
                Text(call.input.compactJSON).font(.caption.monospaced()).textSelection(.enabled)
                if let result {
                    Text(result.error.isEmpty ? "Output" : "Error").font(.caption.weight(.semibold))
                    Text(result.error.isEmpty ? result.output.compactJSON : result.error)
                        .font(.caption.monospaced())
                        .foregroundStyle(result.error.isEmpty ? Color.primary : Color.red)
                        .textSelection(.enabled)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            HStack(spacing: 6) {
                Image(systemName: result == nil ? "gearshape.2" : (result?.error.isEmpty == false ? "xmark.circle" : "checkmark.circle"))
                    .foregroundStyle(result?.error.isEmpty == false ? Color.red : Color.secondary)
                Text(call.tool).font(.caption.monospaced())
                if let result, result.durationMs > 0 {
                    Text("\(Double(result.durationMs) / 1000, specifier: "%.1f")s")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                if result == nil { ProgressView().controlSize(.mini) }
            }
        }
        .padding(8)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
    }
}

struct ArtifactView: View {
    let attachment: Nexus_V1_Attachment

    var body: some View {
        if attachment.mimeType.hasPrefix("image/"), !attachment.data.isEmpty, let image = UIImage(data: attachment.data) {
            Image(uiImage: image)
                .resizable()
                .scaledToFit()
                .frame(maxHeight: 240)
                .clipShape(RoundedRectangle(cornerRadius: 10))
        } else {
            Label("\(attachment.name) · \(Format.bytes(attachment.sizeBytes))", systemImage: "doc")
                .font(.caption)
        }
    }
}

/// Command composer with photo attachments (PhotosPicker).
struct ComposerView: View {
    let agentID: String?
    let replyTaskID: String?

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @State private var text = ""
    @State private var photoItems: [PhotosPickerItem] = []
    @State private var attachments: [Nexus_V1_Attachment] = []
    @State private var isSending = false
    @State private var info: String?
    @FocusState private var focused: Bool

    var body: some View {
        VStack(spacing: 6) {
            if let info {
                Text(info).font(.caption).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
            }
            if !attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack {
                        ForEach(Array(attachments.enumerated()), id: \.offset) { index, attachment in
                            HStack(spacing: 4) {
                                Image(systemName: "photo")
                                Text(attachment.name).lineLimit(1)
                                Button {
                                    attachments.remove(at: index)
                                } label: {
                                    Image(systemName: "xmark.circle.fill")
                                }
                                .buttonStyle(.plain)
                            }
                            .font(.caption)
                            .padding(6)
                            .background(.thinMaterial, in: Capsule())
                        }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: 8) {
                PhotosPicker(selection: $photoItems, maxSelectionCount: 4, matching: .images) {
                    Image(systemName: "photo.on.rectangle.angled").font(.title3)
                }
                TextField(agentID == nil ? "Ask Nexus…" : "Message…", text: $text, axis: .vertical)
                    .lineLimit(1...5)
                    .textFieldStyle(.roundedBorder)
                    .focused($focused)
                Button {
                    Task { await send() }
                } label: {
                    Image(systemName: "arrow.up.circle.fill").font(.title2)
                }
                .disabled(isSending || (text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty))
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
        .background(.bar)
        .onChange(of: photoItems) { _, items in
            Task { await load(items) }
        }
        .onAppear {
            if router.focusComposer {
                focused = true
                router.focusComposer = false
            }
        }
    }

    private func load(_ items: [PhotosPickerItem]) async {
        var loaded: [Nexus_V1_Attachment] = []
        for (index, item) in items.enumerated() {
            guard let data = try? await item.loadTransferable(type: Data.self) else { continue }
            var attachment = Nexus_V1_Attachment()
            attachment.id = UUID().uuidString
            attachment.name = "photo-\(index + 1).jpg"
            attachment.mimeType = item.supportedContentTypes.first?.preferredMIMEType ?? "image/jpeg"
            attachment.sizeBytes = Int64(data.count)
            attachment.data = data
            loaded.append(attachment)
        }
        attachments = loaded
    }

    static let queuedInfo = "Queued — sent automatically when your Nexus is reachable."

    private func send() async {
        let input = text.trimmingCharacters(in: .whitespacesAndNewlines)
        isSending = true
        defer { isSending = false }
        do {
            if let replyTaskID {
                let delivery = try await store.sendMessage(taskID: replyTaskID, text: input, attachments: attachments)
                info = delivery.isQueued ? Self.queuedInfo : nil
            } else {
                let delivery = try await store.submit(input, attachments: attachments, agentID: agentID)
                guard case .sent(let response) = delivery else {
                    info = Self.queuedInfo
                    text = ""
                    return
                }
                if !response.answer.isEmpty {
                    info = response.answer
                } else if !response.question.isEmpty {
                    info = response.question + (response.choices.isEmpty ? "" : " (" + response.choices.joined(separator: " / ") + ")")
                } else if response.hasRoute, !response.route.agentID.isEmpty, agentID == nil {
                    info = "→ \(store.agent(response.route.agentID)?.name ?? response.route.agentID)"
                } else {
                    info = nil
                }
            }
            text = ""
            attachments = []
            photoItems = []
        } catch {
            info = error.localizedDescription
        }
    }
}

#Preview {
    NavigationStack {
        ChatThreadView(thread: ChatThread(agentID: "code-fixer"))
    }
    .environment(NexusStore.preview())
    .environment(AppRouter())
}
