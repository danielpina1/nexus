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
    @State private var path = NavigationPath()

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: NXSpace.s6) {
                    HubUnreachableBanner()

                    NavigationLink(value: ChatThread(agentID: nil)) {
                        NXRow("Nexus", subtitle: "Routed automatically to the right agent", minHeight: 64, leading: {
                            NXIconTile(systemImage: "circle.hexagongrid.fill", tone: .accent, size: 40)
                        }, trailing: {
                            NXChevron()
                        })
                        .nxCard(padding: 0)
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))

                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        NXSectionHeader("Agents", count: store.agents.count)
                        if store.agents.isEmpty {
                            NXEmptyState(systemImage: "cpu", title: "No agents yet", message: "Add a note under agents/ in the vault, or design one with the Architect.", compact: true)
                                .nxCard(padding: 0)
                        } else {
                            VStack(spacing: 0) {
                                ForEach(Array(store.agents.enumerated()), id: \.element.id) { index, agent in
                                    NavigationLink(value: ChatThread(agentID: agent.id)) {
                                        NXRow(agent.name, subtitle: agent.description_p, minHeight: 60, leading: {
                                            AgentIconView(agent: agent, size: 36)
                                        }, trailing: {
                                            NXChevron()
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
                .padding(.horizontal, NXSpace.s4)
                .padding(.top, NXSpace.s2)
                .padding(.bottom, NXSpace.s8)
            }
            .nxScreenBackground()
            .navigationTitle("Chat")
            .toolbar { CommonToolbar() }
            .nexusDestinations()
            .onChange(of: router.focusComposer) { _, focus in
                if focus {
                    path = NavigationPath([ChatThread(agentID: nil)])
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
                LazyVStack(alignment: .leading, spacing: NXSpace.s5) {
                    if tasks.isEmpty && queuedCommands.isEmpty {
                        NXEmptyState(
                            systemImage: "bubble.left.and.text.bubble.right",
                            title: "Start a conversation",
                            message: thread.agentID == nil ? "Ask anything — Nexus routes it to the right agent." : "Send \(title) a task."
                        )
                        .padding(.top, NXSpace.s10)
                    }
                    ForEach(tasks, id: \.id) { task in
                        VStack(alignment: .leading, spacing: NXSpace.s3) {
                            RunTimelineView(task: task, compact: true)
                            RunControls(task: task)
                        }
                        .id(task.id)
                        NXDivider()
                    }
                    ForEach(queuedCommands) { item in
                        QueuedBubble(text: item.summary)
                    }
                }
                .padding(NXSpace.s4)
            }
            .nxScreenBackground()
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
                    .padding(.horizontal, NXSpace.s4)
                    .padding(.top, NXSpace.s1)
            }
        }
        .safeAreaInset(edge: .bottom) {
            ComposerView(agentID: thread.agentID, replyTaskID: nil)
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Reply / Try harder / Stop + cost for a task (MASTER.md: Cancel needs a second tap).
struct RunControls: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store
    @State private var showReply = false
    @State private var replyText = ""
    @State private var busy: String?
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: NXSpace.s2) {
                TaskStateBadge(state: task.state)
                Spacer()
                Text(Format.cost(cents: task.costCents))
                    .nx(NXType.caption)
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
                    .accessibilityLabel("Cost \(Format.cost(cents: task.costCents))")
            }
            if task.state.isActive {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: NXSpace.s2) { activeButtons }
                    VStack(alignment: .leading, spacing: NXSpace.s2) { activeButtons }
                }
            } else if task.state == .failed {
                NXButton("Retry harder", systemImage: "arrow.clockwise", kind: .secondary, size: .small, isLoading: busy == "escalate") {
                    perform("escalate") { try await store.escalate(taskID: task.id) }
                }
            }
            if showReply {
                HStack(spacing: NXSpace.s2) {
                    NXTextField(placeholder: "Message the agent…", text: $replyText, axis: .vertical)
                    NXIconButton(systemImage: "paperplane.fill", label: "Send", tint: NXColor.accent, isLoading: busy == "reply") {
                        let text = replyText
                        replyText = ""
                        showReply = false
                        perform("reply") { _ = try await store.sendMessage(taskID: task.id, text: text) }
                    }
                    .disabled(replyText.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
            if let errorMessage {
                NXInlineError(errorMessage) { self.errorMessage = nil }
            }
        }
    }

    @ViewBuilder
    private var activeButtons: some View {
        NXButton("Reply", systemImage: "arrowshape.turn.up.left", kind: .secondary, size: .small) {
            showReply.toggle()
        }
        NXButton("Try harder", systemImage: "arrow.up.circle", kind: .secondary, size: .small, isLoading: busy == "escalate") {
            perform("escalate") { try await store.escalate(taskID: task.id) }
        }
        NXConfirmButton("Stop", confirmTitle: "Confirm stop", systemImage: "stop.circle", kind: .ghost, size: .small, isLoading: busy == "cancel") {
            perform("cancel") { try await store.cancel(taskID: task.id) }
        }
    }

    private func perform(_ key: String, _ action: @escaping () async throws -> Void) {
        busy = key
        Task {
            defer { busy = nil }
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
        let lastEntryID = entries.last?.id
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            if entries.isEmpty || !entries.contains(where: { if case .userMessage = $0.kind { return true }; return false }) {
                UserBubble(text: task.prompt.isEmpty ? task.displayTitle : task.prompt)
            }
            if entries.isEmpty && task.state.isActive {
                HStack(spacing: NXSpace.s2) {
                    ProgressView().controlSize(.small)
                    Text(task.state == .queued || task.state == .routing ? "Finding the right machine…" : "Starting…")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
                .accessibilityElement(children: .combine)
            }
            ForEach(entries) { entry in
                entryView(entry, isLast: entry.id == lastEntryID)
            }
            if task.state.isTerminal {
                if !task.summary.isEmpty {
                    Label(task.summary, systemImage: task.state == .succeeded ? "checkmark.seal.fill" : "xmark.octagon")
                        .nxText(NXType.body.weight(.medium))
                        .foregroundStyle(task.state == .succeeded ? NXColor.ok : NXColor.danger)
                } else if entries.isEmpty {
                    Text("No output recorded.")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
            }
        }
    }

    @ViewBuilder
    private func entryView(_ entry: TimelineItem, isLast: Bool) -> some View {
        switch entry.kind {
        case .userMessage(let text):
            UserBubble(text: text)
        case .output(let text, let isFinal):
            HStack(alignment: .top, spacing: NXSpace.s2) {
                AgentIconView(agent: store.agent(task.agentID), agentID: task.agentID, size: 24)
                VStack(alignment: .leading, spacing: NXSpace.s1) {
                    MarkdownText(markdown: text)
                        .nxText(NXType.bodyLg)
                        .foregroundStyle(NXColor.fg)
                        .textSelection(.enabled)
                    // Streaming indicator only on the chunk that is still growing.
                    if !isFinal && isLast && task.state.isActive {
                        StreamingCaret()
                    }
                }
            }
        case .toolCall(let call, let result):
            ToolCallView(call: call, result: result)
        case .question(let question):
            Label(question.text, systemImage: "questionmark.bubble")
                .nxText(NXType.body.weight(.medium))
                .foregroundStyle(NXColor.info)
                .padding(NXSpace.s3)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(NXColor.infoSoft, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        case .approval(let approval):
            if approval.state == .pending {
                ApprovalCardView(approval: approval, compact: true)
            } else {
                HStack(spacing: NXSpace.s2) {
                    Image(systemName: approval.isBiometric ? "faceid" : "hand.raised")
                        .foregroundStyle(NXColor.muted)
                        .accessibilityHidden(true)
                    Text(approval.title)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.fg2)
                    NXBadge(approval.state.badgeLabel, tone: approval.state.tone)
                }
                .accessibilityElement(children: .combine)
            }
        case .cost(let cost):
            if !compact {
                Text("\(cost.hasAmount ? cost.amount.formatted : "") · \(cost.model) · \(cost.inputTokens)→\(cost.outputTokens) tokens")
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
            }
        case .artifact(let attachment):
            ArtifactView(attachment: attachment)
        case .status(let state, _):
            if !compact {
                HStack(spacing: NXSpace.s2) {
                    Rectangle().fill(NXColor.border).frame(height: 1)
                    TaskStateBadge(state: state)
                    Rectangle().fill(NXColor.border).frame(height: 1)
                }
            }
        case .notice(let notice):
            Label(notice.title, systemImage: "bell")
                .nxText(NXType.footnote)
                .foregroundStyle(NXColor.muted)
        }
    }
}

/// Blinking caret while text streams (static under Reduce Motion).
struct StreamingCaret: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var visible = true

    var body: some View {
        RoundedRectangle(cornerRadius: 1)
            .fill(NXColor.accent)
            .frame(width: 8, height: 14)
            .opacity(visible ? 1 : 0.15)
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 0.6).repeatForever(autoreverses: true)) {
                    visible = false
                }
            }
            .accessibilityLabel("Streaming")
    }
}

/// Prompt bubble: primary fill, primary-fg text (MASTER.md "Run / stream view").
struct UserBubble: View {
    let text: String

    var body: some View {
        HStack {
            Spacer(minLength: 48)
            Text(text)
                .nx(NXType.bodyLg)
                .foregroundStyle(NXColor.primaryFg)
                .padding(.horizontal, 14)
                .padding(.vertical, NXSpace.s2)
                .background(NXColor.primary, in: UnevenRoundedRectangle(topLeadingRadius: 16, bottomLeadingRadius: 16, bottomTrailingRadius: 6, topTrailingRadius: 16, style: .continuous))
                .textSelection(.enabled)
                .accessibilityLabel("You: \(text)")
        }
    }
}

/// A command waiting in the outbox (typed while the Hub was unreachable).
struct QueuedBubble: View {
    let text: String

    var body: some View {
        VStack(alignment: .trailing, spacing: NXSpace.s1) {
            UserBubble(text: text)
                .opacity(0.6)
            Label("Waiting for your Nexus — sent when you're connected", systemImage: "clock")
                .nxText(NXType.caption.weight(.regular))
                .foregroundStyle(NXColor.muted)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

/// Collapsible tool row (mono, surface-2).
struct ToolCallView: View {
    let call: Nexus_V1_ToolCall
    let result: Nexus_V1_ToolResult?
    @State private var expanded = false

    private var failed: Bool { result?.error.isEmpty == false }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                expanded.toggle()
            } label: {
                HStack(spacing: NXSpace.s2) {
                    Image(systemName: result == nil ? "gearshape.2" : (failed ? "xmark.circle" : "checkmark.circle"))
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(failed ? NXColor.danger : NXColor.muted)
                        .accessibilityHidden(true)
                    Text(call.tool)
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                    if let result, result.durationMs > 0 {
                        Text("\(Double(result.durationMs) / 1000, specifier: "%.1f")s")
                            .nx(NXType.caption.weight(.regular))
                            .monospacedDigit()
                            .foregroundStyle(NXColor.muted)
                    }
                    if result == nil { ProgressView().controlSize(.mini) }
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(NXColor.muted)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                        .accessibilityHidden(true)
                }
                .padding(.horizontal, NXSpace.s3)
                .frame(minHeight: NXSize.touchMin)
                .contentShape(Rectangle())
            }
            .buttonStyle(NXPlainPressStyle())
            .accessibilityLabel("Tool \(call.tool), \(result == nil ? "running" : (failed ? "failed" : "done"))")
            .accessibilityHint(expanded ? "Collapses the details." : "Shows input and output.")

            if expanded {
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    NXOverline("Input")
                    Text(call.input.compactJSON)
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg2)
                        .textSelection(.enabled)
                    if let result {
                        NXOverline(result.error.isEmpty ? "Output" : "Error")
                        Text(result.error.isEmpty ? result.output.compactJSON : result.error)
                            .nx(NXType.mono)
                            .foregroundStyle(result.error.isEmpty ? NXColor.fg2 : NXColor.danger)
                            .textSelection(.enabled)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, NXSpace.s3)
                .padding(.bottom, NXSpace.s3)
            }
        }
        .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
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
                .clipShape(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                .accessibilityLabel("Image: \(attachment.name)")
        } else {
            Label("\(attachment.name) · \(Format.bytes(attachment.sizeBytes))", systemImage: "doc")
                .nxText(NXType.footnote)
                .foregroundStyle(NXColor.fg2)
                .padding(NXSpace.s3)
                .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        }
    }
}

/// Command composer with photo attachments (PhotosPicker). Glass bar (chrome).
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

    private var canSend: Bool {
        !isSending && !(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty)
    }

    var body: some View {
        VStack(spacing: NXSpace.s2) {
            if let info {
                Text(info)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityAddTraits(.updatesFrequently)
            }
            if !attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: NXSpace.s2) {
                        ForEach(Array(attachments.enumerated()), id: \.offset) { index, attachment in
                            HStack(spacing: 4) {
                                Image(systemName: "photo").accessibilityHidden(true)
                                Text(attachment.name).lineLimit(1)
                                NXIconButton(systemImage: "xmark.circle.fill", label: "Remove \(attachment.name)", tint: NXColor.muted) {
                                    attachments.remove(at: index)
                                }
                                .padding(.vertical, -10)
                            }
                            .nxText(NXType.caption)
                            .foregroundStyle(NXColor.fg2)
                            .padding(.leading, NXSpace.s2)
                            .background(NXColor.surface2, in: Capsule())
                        }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: NXSpace.s2) {
                PhotosPicker(selection: $photoItems, maxSelectionCount: 4, matching: .images) {
                    Image(systemName: "photo.on.rectangle.angled")
                        .font(.system(size: 18))
                        .foregroundStyle(NXColor.fg2)
                        .frame(width: NXSize.touchMin, height: NXSize.touchMin)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Attach photos")
                TextField(agentID == nil ? "Ask Nexus…" : "Message…", text: $text, axis: .vertical)
                    .lineLimit(1...5)
                    .nxText(NXType.bodyLg)
                    .foregroundStyle(NXColor.fg)
                    .focused($focused)
                    .padding(.horizontal, NXSpace.s3)
                    .padding(.vertical, 10)
                    .frame(minHeight: NXSize.touchMin)
                    .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.xl, style: .continuous))
                    .overlay {
                        RoundedRectangle(cornerRadius: NXRadius.xl, style: .continuous)
                            .strokeBorder(focused ? NXColor.ring : NXColor.borderInput, lineWidth: focused ? 2 : 1)
                    }
                    .accessibilityLabel(agentID == nil ? "Ask Nexus" : "Message")
                Button {
                    Task { await send() }
                } label: {
                    ZStack {
                        Circle().fill(canSend ? NXColor.primary : NXColor.surface3)
                        if isSending {
                            ProgressView().tint(NXColor.primaryFg)
                        } else {
                            Image(systemName: "arrow.up")
                                .font(.system(size: 16, weight: .bold))
                                .foregroundStyle(canSend ? NXColor.primaryFg : NXColor.muted)
                        }
                    }
                    .frame(width: NXSize.touchMin, height: NXSize.touchMin)
                }
                .buttonStyle(NXPlainPressStyle(cornerRadius: NXSize.touchMin / 2))
                .disabled(!canSend)
                .accessibilityLabel("Send")
            }
        }
        .padding(.horizontal, NXSpace.s3)
        .padding(.vertical, NXSpace.s2)
        .nxGlass()
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
            info = "Not sent: " + error.localizedDescription
        }
    }
}

#Preview {
    NavigationStack {
        ChatThreadView(thread: ChatThread(agentID: "code-fixer"))
    }
    .environment(NexusStore.preview())
    .environment(AppRouter())
    .environment(ToastCenter())
}
