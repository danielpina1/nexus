import NexusCore
import PhotosUI
import SwiftUI

/// A conversation (web pages/Chat.tsx): messages are sent with source "chat" and the thread's
/// conversation id; the reply streams into its bubble live from the event stream with Markdown
/// and a caret, tool calls as compact chips, questions and approvals inline, the engine and
/// exact model that answered, a thinking timer and the time to first token.
struct ChatThreadView: View {
    let thread: ChatThread

    @Environment(NexusStore.self) private var store
    @Environment(ChatStore.self) private var chat
    @State private var draft = ""
    /// Follow the streaming reply; off once you scroll yourself, back on when you send.
    @State private var follow = true

    private var title: String {
        thread.agentID.flatMap { store.agent($0)?.name } ?? "Chat"
    }

    var body: some View {
        let messages = chat.messages(thread)
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: NXSpace.s5) {
                    if messages.isEmpty {
                        ChatEmptyState(agentName: thread.agentID.flatMap { store.agent($0)?.name }) { suggestion in
                            draft = suggestion
                        }
                        .padding(.top, NXSpace.s8)
                    }
                    ForEach(Array(messages.enumerated()), id: \.element.id) { index, message in
                        ChatMessageRow(
                            message: message,
                            thread: thread,
                            threadKey: chat.key(thread),
                            live: index >= messages.count - 6,
                            retryInput: retryInput(messages, index)
                        )
                        .id(message.id)
                    }
                    Color.clear.frame(height: 1).id(Self.bottomID)
                }
                .padding(.horizontal, NXSpace.s4)
                .padding(.vertical, NXSpace.s4)
            }
            .defaultScrollAnchor(.bottom)
            .scrollDismissesKeyboard(.interactively)
            .simultaneousGesture(DragGesture(minimumDistance: 16).onChanged { _ in follow = false })
            .nxScreenBackground()
            .onChange(of: streamSignature(messages)) { _, _ in
                guard follow else { return }
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
            .onChange(of: messages.count) { _, _ in
                follow = true
                withAnimation(.easeOut(duration: 0.2)) { proxy.scrollTo(Self.bottomID, anchor: .bottom) }
            }
            .overlay(alignment: .bottomTrailing) {
                if !follow && chat.isBusy(thread) {
                    Button {
                        follow = true
                        withAnimation { proxy.scrollTo(Self.bottomID, anchor: .bottom) }
                    } label: {
                        Image(systemName: "arrow.down")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(NXColor.fg)
                            .frame(width: NXSize.touchMin, height: NXSize.touchMin)
                            .nxGlass(cornerRadius: NXSize.touchMin / 2)
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXSize.touchMin / 2))
                    .padding(NXSpace.s4)
                    .accessibilityLabel("Jump to the latest reply")
                }
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
            ChatComposer(thread: thread, text: $draft) {
                follow = true
            }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    chat.newChat(thread)
                    draft = ""
                } label: {
                    Image(systemName: "square.and.pencil")
                }
                .disabled(messages.isEmpty)
                .accessibilityLabel("New chat")
                .accessibilityHint("Starts a new conversation. The assistant forgets this one.")
            }
        }
        .task {
            chat.seedDemoIfNeeded()
            chat.resolveQueued()
        }
        .onChange(of: store.outbox.count) { _, _ in
            chat.resolveQueued()
        }
    }

    private static let bottomID = "chat-bottom"

    /// Changes whenever the live reply grows (new events or more streamed text).
    private func streamSignature(_ messages: [ChatStore.Message]) -> Int {
        guard let last = messages.last(where: { $0.role == .nexus }) else { return 0 }
        guard let taskID = last.taskID else { return last.pending ? 1 : 2 }
        let entries = store.timelines[taskID] ?? []
        var length = 0
        if let entry = entries.last, case .output(let text, _) = entry.kind { length = text.count }
        return entries.count &* 100_003 &+ length &+ Int(store.tasks[taskID]?.state.rawValue ?? 0)
    }

    private func retryInput(_ messages: [ChatStore.Message], _ index: Int) -> String? {
        guard messages[index].error != nil, index > 0, messages[index - 1].role == .user else { return nil }
        return messages[index - 1].text
    }
}

// MARK: - Messages

struct ChatMessageRow: View {
    let message: ChatStore.Message
    let thread: ChatThread
    let threadKey: String
    let live: Bool
    let retryInput: String?

    @Environment(ChatStore.self) private var chat

    var body: some View {
        if message.role == .user {
            VStack(alignment: .trailing, spacing: NXSpace.s1) {
                if let label = ChatCommand.parse(message.text).label {
                    NXBadge(label, systemImage: label.hasPrefix("@") ? "at" : (label == "Search" ? "magnifyingglass" : "bookmark"), tone: .accent)
                }
                UserBubble(text: message.text)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
        } else {
            HStack(alignment: .top, spacing: 10) {
                NXIconTile(systemImage: "sparkles", tone: .accent, size: 28)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    if let kind = message.routeKind {
                        Label(kind == "search" ? "Search" : (kind == "capture" ? "Saved" : "Workflow"), systemImage: kind == "search" ? "magnifyingglass" : (kind == "capture" ? "bookmark" : "list.bullet.rectangle"))
                            .nxText(NXType.caption.weight(.regular))
                            .foregroundStyle(NXColor.muted)
                    }
                    content
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if let taskID = message.taskID {
            ChatReplyView(message: message, threadKey: threadKey, taskID: taskID, live: live)
        } else if message.pending {
            ThinkingLabel(since: message.at)
        } else if message.queuedID != nil {
            Label(ComposerView.queuedInfo, systemImage: "clock")
                .nxText(NXType.footnote)
                .foregroundStyle(NXColor.muted)
        } else if let error = message.error {
            HStack(alignment: .top, spacing: NXSpace.s2) {
                Image(systemName: "exclamationmark.circle")
                    .foregroundStyle(NXColor.danger)
                    .accessibilityHidden(true)
                Text("Not sent: \(error)")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.danger)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if let retryInput {
                    Button("Retry") {
                        Task { await chat.send(retryInput, thread: thread) }
                    }
                    .font(NXType.footnote.weight(.semibold).font)
                    .foregroundStyle(NXColor.danger)
                    .frame(minWidth: NXSize.touchMin, minHeight: 32)
                }
            }
            .padding(NXSpace.s3)
            .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
            .accessibilityElement(children: .contain)
        } else if !message.text.isEmpty || !(message.choices ?? []).isEmpty || !(message.sources ?? []).isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                if !message.text.isEmpty {
                    ChatMarkdown(text: message.text, streaming: false)
                }
                if let choices = message.choices, !choices.isEmpty {
                    FlowChips(items: choices) { choice in
                        Task { await chat.send(message.originalInput ?? "", thread: thread, forcedAgent: choice) }
                    }
                }
                if let sources = message.sources, !sources.isEmpty {
                    VStack(alignment: .leading, spacing: NXSpace.s1) {
                        ForEach(sources, id: \.self) { source in
                            NavigationLink(value: NoteRoute(id: source.id, path: source.path, title: source.title)) {
                                Label(source.title.isEmpty ? source.path : source.title, systemImage: "book.closed")
                                    .nxText(NXType.footnote.weight(.medium))
                                    .foregroundStyle(NXColor.accent)
                                    .lineLimit(1)
                                    .frame(minHeight: 32)
                            }
                        }
                    }
                }
            }
            .chatBubble()
        }
    }
}

/// The assistant's reply rendered from the run's events.
struct ChatReplyView: View {
    let message: ChatStore.Message
    let threadKey: String
    let taskID: String
    let live: Bool

    @Environment(NexusStore.self) private var store
    @Environment(ChatStore.self) private var chat

    var body: some View {
        let task = store.tasks[taskID]
        let entries = live ? (store.timelines[taskID] ?? []) : []
        let items = ChatItem.build(entries)
        let model = RunModel.from(entries, task: task)
        // Without the task (not loaded yet) a recent reply counts as running for a moment.
        let active = task?.state.isActive ?? (live && message.finalText == nil && Date().timeIntervalSince(message.at) < 120)
        let text = ChatItem.replyText(items)
        let hasText = !text.isEmpty
        let state = task?.state
        let waiting = state?.needsUser == true
        let failed = state == .failed
        let cancelled = state == .cancelled
        let activeKids = store.children(of: taskID).filter { $0.state.isActive }.count
        let answer = text.isEmpty ? (message.finalText ?? task?.summary ?? "") : text
        let warn = BackgroundClaim.untracked(answer: answer, taskActive: active, activeChildren: activeKids)
        let fallback = items.isEmpty && !active ? (message.finalText ?? task?.summary ?? "") : ""
        let lastAt = entries.last?.at ?? message.at

        VStack(alignment: .leading, spacing: NXSpace.s2) {
            whoLabel(task: task, model: model)
            if items.isEmpty && active {
                ThinkingLabel(since: message.at)
            }
            ForEach(items) { item in
                itemView(item, isLast: item.id == items.last?.id, active: active)
            }
            if !items.isEmpty && active && !waiting && items.last?.isText != true {
                if task?.phase == "waiting_children" {
                    ThinkingLabel(since: lastAt, label: activeKids > 0 ? "Waiting for \(activeKids) sub-task\(activeKids == 1 ? "" : "s")…" : "Waiting for sub-tasks…")
                } else {
                    ThinkingLabel(since: lastAt, label: "Working…")
                }
            }
            if waiting {
                Text(state == .waitingApproval ? "Waiting for your approval…" : "Waiting for your answer…")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            if !fallback.isEmpty && !failed {
                ChatMarkdown(text: fallback, streaming: false)
                    .chatBubble()
            } else if task == nil && !active && items.isEmpty {
                Text("The reply is in Run details.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            Group {
                if failed {
                    Label(task?.summary.isEmpty == false ? task!.summary : "The run failed.", systemImage: "exclamationmark.circle")
                        .nxText(NXType.footnote)
                        .foregroundStyle(NXColor.danger)
                        .padding(NXSpace.s3)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
                }
                if cancelled {
                    Text("Stopped.")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
                if warn {
                    BackgroundWarningView()
                }
                if activeKids > 0 {
                    Label("\(activeKids) sub-task\(activeKids == 1 ? "" : "s") still running", systemImage: "arrow.triangle.branch")
                        .nxText(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.info)
                }
            }
            NavigationLink(value: TaskRoute(id: taskID)) {
                Label("Run details", systemImage: "list.bullet.rectangle")
                    .nxText(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .frame(minHeight: 32)
            }
            .accessibilityHint("Opens the task with every step, cost and sub-tasks.")
        }
        .onAppear { record(hasText: hasText, task: task, text: text, model: model) }
        .task(id: taskID) {
            // Older replies: fetch the task once if this iPhone has not seen it yet.
            if store.tasks[taskID] == nil && !store.isDemo {
                await store.loadTask(taskID)
            }
        }
        .onChange(of: hasText) { _, _ in record(hasText: hasText, task: task, text: text, model: model) }
        .onChange(of: state) { _, _ in record(hasText: hasText, task: task, text: text, model: model) }
        .onChange(of: model) { _, _ in record(hasText: hasText, task: task, text: text, model: model) }
    }

    /// "Athena · Assistant · Claude Code · claude-sonnet-4-5-20250929 · first token 1.2s".
    @ViewBuilder
    private func whoLabel(task: Nexus_V1_Task?, model: RunModel) -> some View {
        let agent = task.flatMap { store.agent($0.agentID) }
        let who = task.map(Self.who(store: store)) ?? (message.agentLabel ?? "Nexus")
        let engine = model.label.isEmpty ? (message.engine ?? "") : model.label
        let alias = agent?.model ?? ""
        HStack(spacing: 4) {
            Text([who, engine].filter { !$0.isEmpty }.joined(separator: " · "))
                .nx(NXType.caption.weight(.medium))
                .foregroundStyle(NXColor.fg2)
                .lineLimit(2)
                .help(alias.isEmpty ? "" : "Agent model alias: \(alias)")
                .accessibilityHint(alias.isEmpty ? "" : "Agent model alias: \(alias)")
            if let ms = message.firstTokenMs {
                let seconds = String(format: "%.1f", Double(ms) / 1000)
                Text("· first token \(seconds)s")
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
                    .help("Time from send to the first streamed token")
                    .accessibilityLabel("First token after \(seconds) seconds")
            }
        }
    }

    static func who(store: NexusStore) -> (Nexus_V1_Task) -> String {
        { task in
            if !task.agentID.isEmpty { return store.agent(task.agentID)?.name ?? task.agentID }
            if !task.teamID.isEmpty { return "Team · \(task.teamID)" }
            if !task.workflowID.isEmpty { return "Workflow · \(task.workflowID)" }
            return "Nexus"
        }
    }

    @ViewBuilder
    private func itemView(_ item: ChatItem, isLast: Bool, active: Bool) -> some View {
        switch item.kind {
        case .text(let text, _):
            if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                ChatMarkdown(text: text, streaming: isLast && active)
                    .chatBubble()
            }
        case .tool(let call, let result):
            ToolChipView(call: call, result: result, active: active)
        case .question(let question):
            ChatQuestionCard(taskID: taskID, question: question)
        case .approval(let snapshot):
            if let pending = store.approvals[snapshot.id] {
                ApprovalCardView(approval: pending, compact: true, inline: true)
            } else if snapshot.state != .pending {
                ApprovalCardView(approval: snapshot, compact: true, inline: true)
            } else {
                Label("Approval no longer pending: \(snapshot.readableTitle)", systemImage: "hand.raised")
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
        case .artifact(let attachment):
            ArtifactView(attachment: attachment)
        }
    }

    /// Records the first-token time and, once the run ended, its answer and engine.
    private func record(hasText: Bool, task: Nexus_V1_Task?, text: String, model: RunModel) {
        if live, hasText, message.firstTokenMs == nil, task?.state.isActive ?? true {
            let ms = Int(Date().timeIntervalSince(message.at) * 1000)
            // Only for replies started in this session (not a reopened old conversation).
            if ms > 0 && ms < 10 * 60 * 1000 {
                chat.annotate(message.id, in: threadKey, firstTokenMs: ms)
            }
        }
        guard let task, task.state.isTerminal, live else { return }
        let final = text.isEmpty ? task.summary : text
        let engine = model.label
        let who = Self.who(store: store)(task)
        if message.finalText != final || (!engine.isEmpty && message.engine != engine) || message.agentLabel != who {
            chat.annotate(message.id, in: threadKey, finalText: final, engine: engine, agentLabel: who)
        }
    }
}

/// "This answer mentions background work that Nexus isn't tracking."
struct BackgroundWarningView: View {
    var body: some View {
        HStack(alignment: .top, spacing: NXSpace.s2) {
            Image(systemName: "exclamationmark.triangle")
                .foregroundStyle(NXColor.warn)
                .accessibilityHidden(true)
            Text(BackgroundClaim.warning)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.fg)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(NXSpace.s3)
        .background(NXColor.warnSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous).strokeBorder(NXColor.warn.opacity(0.4), lineWidth: 1))
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Warning: \(BackgroundClaim.warning)")
    }
}

/// "Thinking… 4s" (the timer since the message was sent or the last event).
struct ThinkingLabel: View {
    let since: Date
    var label = "Thinking…"

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            let seconds = max(0, Int(context.date.timeIntervalSince(since)))
            HStack(spacing: NXSpace.s2) {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityHidden(true)
                Text("\(label) \(seconds)s")
                    .nx(NXType.footnote)
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
            }
            .frame(minHeight: 28)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(label) \(seconds) seconds")
        }
    }
}

/// Wrapping row of small secondary buttons (router question choices).
struct FlowChips: View {
    let items: [String]
    let action: (String) -> Void

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: NXSpace.s2) {
                ForEach(items, id: \.self) { item in
                    NXButton(item, kind: .secondary, size: .small) { action(item) }
                }
            }
        }
    }
}

struct ChatEmptyState: View {
    let agentName: String?
    let pick: (String) -> Void

    private static let suggestions = [
        "What can you do?",
        "What are you working on right now?",
        "/search heat pumps",
        "remember the plumber comes on Tuesday",
    ]

    var body: some View {
        VStack(spacing: NXSpace.s4) {
            NXEmptyState(
                systemImage: "sparkles",
                title: agentName.map { "Chat with \($0)" } ?? "Chat with Nexus",
                message: agentName == nil
                    ? "Your assistant answers here, live. Start with @agent to talk to a specialist. /search searches your vault; remember … saves a note."
                    : "Messages in this chat go to \(agentName!). /search and remember … work here too."
            )
            VStack(spacing: NXSpace.s2) {
                ForEach(Self.suggestions, id: \.self) { suggestion in
                    Button {
                        pick(suggestion)
                    } label: {
                        Text(suggestion)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg2)
                            .frame(maxWidth: .infinity, minHeight: NXSize.touchMin)
                            .padding(.horizontal, NXSpace.s3)
                            .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
                            .overlay(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                    .accessibilityHint("Puts this in the message field.")
                }
            }
        }
        .frame(maxWidth: .infinity)
    }
}

// MARK: - Bubble

private struct ChatBubbleModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(NXColor.surface, in: UnevenRoundedRectangle(topLeadingRadius: 6, bottomLeadingRadius: 16, bottomTrailingRadius: 16, topTrailingRadius: 16, style: .continuous))
            .overlay(UnevenRoundedRectangle(topLeadingRadius: 6, bottomLeadingRadius: 16, bottomTrailingRadius: 16, topTrailingRadius: 16, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
    }
}

extension View {
    /// The assistant's bubble: surface, hairline border, small top-leading corner.
    func chatBubble() -> some View {
        modifier(ChatBubbleModifier())
    }
}

// MARK: - Markdown

/// Streamed Markdown: paragraphs, headings, bullet and numbered lists, quotes and fenced code
/// (inline syntax via `AttributedString(markdown:)`), with a blinking caret while streaming.
struct ChatMarkdown: View {
    let text: String
    let streaming: Bool

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    enum Block {
        case paragraph(String)
        case heading(Int, String)
        case bullet(String)
        case numbered(String, String)
        case quote(String)
        case code(String)
    }

    var body: some View {
        let blocks = Self.parse(text)
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                blockView(block, caret: streaming && index == blocks.count - 1)
            }
            if streaming && blocks.isEmpty {
                StreamingCaret()
            }
        }
        .textSelection(.enabled)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func blockView(_ block: Block, caret: Bool) -> some View {
        switch block {
        case .code(let code):
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                CodeBlock(text: code, maxLines: 60)
                if caret { StreamingCaret() }
            }
        case .heading(let level, let line):
            withCaret(Self.inline(line), caret: caret)
                .nxText(level == 1 ? NXType.title2 : NXType.title3)
                .foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
        case .bullet(let line):
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text("•").foregroundStyle(NXColor.muted).accessibilityHidden(true)
                withCaret(Self.inline(line), caret: caret)
                    .nxText(NXType.bodyLg)
                    .foregroundStyle(NXColor.fg)
            }
        case .numbered(let number, let line):
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(number).monospacedDigit().foregroundStyle(NXColor.muted)
                withCaret(Self.inline(line), caret: caret)
                    .nxText(NXType.bodyLg)
                    .foregroundStyle(NXColor.fg)
            }
            .nxText(NXType.bodyLg)
        case .quote(let line):
            withCaret(Self.inline(line), caret: caret)
                .nxText(NXType.bodyLg)
                .foregroundStyle(NXColor.fg2)
                .padding(.leading, NXSpace.s2)
                .overlay(alignment: .leading) { Rectangle().fill(NXColor.borderStrong).frame(width: 3) }
        case .paragraph(let line):
            withCaret(Self.inline(line), caret: caret)
                .nxText(NXType.bodyLg)
                .foregroundStyle(NXColor.fg)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// The text with a caret glyph at its end that blinks (static under Reduce Motion).
    @ViewBuilder
    private func withCaret(_ text: Text, caret: Bool) -> some View {
        if caret && !reduceMotion {
            TimelineView(.periodic(from: .now, by: 0.55)) { context in
                let on = Int(context.date.timeIntervalSinceReferenceDate / 0.55) % 2 == 0
                text + Text(" ▍").foregroundColor(on ? NXColor.accent : NXColor.accent.opacity(0.15))
            }
        } else if caret {
            text + Text(" ▍").foregroundColor(NXColor.accent)
        } else {
            text
        }
    }

    static func inline(_ text: String) -> Text {
        let body = text.replacingOccurrences(of: "[[", with: "**").replacingOccurrences(of: "]]", with: "**")
        let options = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        if let attributed = try? AttributedString(markdown: body, options: options) {
            return Text(attributed)
        }
        return Text(body)
    }

    static func parse(_ text: String) -> [Block] {
        var blocks: [Block] = []
        var paragraph: [String] = []
        var code: [String]?

        func flush() {
            if !paragraph.isEmpty {
                blocks.append(.paragraph(paragraph.joined(separator: "\n")))
                paragraph = []
            }
        }

        for raw in text.components(separatedBy: "\n") {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("```") {
                if let open = code {
                    blocks.append(.code(open.joined(separator: "\n")))
                    code = nil
                } else {
                    flush()
                    code = []
                }
                continue
            }
            if code != nil {
                code?.append(raw)
                continue
            }
            if line.isEmpty {
                flush()
            } else if line.hasPrefix("#") {
                let level = line.prefix(while: { $0 == "#" }).count
                let rest = line.dropFirst(level)
                if level <= 6, rest.hasPrefix(" ") {
                    flush()
                    blocks.append(.heading(level, rest.trimmingCharacters(in: .whitespaces)))
                } else {
                    paragraph.append(line)
                }
            } else if line.hasPrefix("- ") || line.hasPrefix("* ") || line.hasPrefix("• ") {
                flush()
                blocks.append(.bullet(String(line.dropFirst(2))))
            } else if line.hasPrefix("> ") {
                flush()
                blocks.append(.quote(String(line.dropFirst(2))))
            } else if let dot = line.firstIndex(of: "."), dot > line.startIndex, line[..<dot].allSatisfy(\.isNumber),
                      line[line.index(after: dot)...].hasPrefix(" ") {
                flush()
                blocks.append(.numbered(String(line[...dot]), String(line[line.index(dot, offsetBy: 2)...])))
            } else {
                paragraph.append(line)
            }
        }
        if let open = code {
            // Still streaming inside a fence.
            blocks.append(.code(open.joined(separator: "\n")))
        }
        flush()
        return blocks
    }
}

// MARK: - Composer

/// The chat composer: text (with @agent and /command suggestions), photos, Send and Stop.
struct ChatComposer: View {
    let thread: ChatThread
    @Binding var text: String
    var onSend: () -> Void

    @Environment(NexusStore.self) private var store
    @Environment(ChatStore.self) private var chat
    @Environment(ToastCenter.self) private var toasts
    @State private var photoItems: [PhotosPickerItem] = []
    @State private var attachments: [Nexus_V1_Attachment] = []
    @State private var stopping = false
    @FocusState private var focused: Bool
    @Environment(AppRouter.self) private var router

    private var canSend: Bool {
        !(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty)
    }

    /// "@" and "/" completions for the word being typed at the start of the message.
    private var suggestions: [String] {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        guard !trimmed.contains(" "), !trimmed.contains("\n") else { return [] }
        if trimmed.hasPrefix("@") {
            let q = trimmed.dropFirst().lowercased()
            return store.agents
                .filter { q.isEmpty || $0.id.lowercased().hasPrefix(q) || $0.name.lowercased().contains(q) }
                .prefix(8)
                .map { "@" + $0.id }
        }
        if trimmed.hasPrefix("/") {
            return ["/search", "/remember"].filter { $0.hasPrefix(trimmed.lowercased()) }
        }
        return []
    }

    var body: some View {
        let activeTask = chat.activeTask(thread)
        VStack(spacing: NXSpace.s2) {
            if !suggestions.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: NXSpace.s2) {
                        ForEach(suggestions, id: \.self) { suggestion in
                            Button {
                                text = suggestion + " "
                            } label: {
                                Text(suggestion)
                                    .nx(NXType.footnote.weight(.medium))
                                    .foregroundStyle(NXColor.accent)
                                    .padding(.horizontal, NXSpace.s3)
                                    .frame(minHeight: 32)
                                    .background(NXColor.accentSoft, in: Capsule())
                                    .frame(minHeight: NXSize.touchMin)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(NXPlainPressStyle(cornerRadius: 16))
                            .accessibilityHint("Inserts \(suggestion).")
                        }
                    }
                }
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
                TextField(activeTask != nil ? "Nexus is answering…" : "Message Nexus…", text: $text, axis: .vertical)
                    .lineLimit(1...6)
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
                    .accessibilityLabel("Message")
                    .accessibilityHint("Start with @agent for a specialist, /search to search your vault, or remember to save a note.")
                if let activeTask, !canSend {
                    Button {
                        stop(activeTask)
                    } label: {
                        ZStack {
                            Circle().fill(NXColor.fg)
                            if stopping {
                                ProgressView().tint(NXColor.bg)
                            } else {
                                RoundedRectangle(cornerRadius: 3)
                                    .fill(NXColor.bg)
                                    .frame(width: 14, height: 14)
                            }
                        }
                        .frame(width: NXSize.touchMin, height: NXSize.touchMin)
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXSize.touchMin / 2))
                    .disabled(stopping)
                    .accessibilityLabel("Stop")
                    .accessibilityHint("Stops the reply that is running.")
                } else {
                    Button {
                        send()
                    } label: {
                        ZStack {
                            Circle().fill(canSend ? NXColor.primary : NXColor.surface3)
                            Image(systemName: "arrow.up")
                                .font(.system(size: 16, weight: .bold))
                                .foregroundStyle(canSend ? NXColor.primaryFg : NXColor.muted)
                        }
                        .frame(width: NXSize.touchMin, height: NXSize.touchMin)
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXSize.touchMin / 2))
                    .disabled(!canSend)
                    .accessibilityLabel("Send")
                }
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

    private func send() {
        let input = text
        let photos = attachments
        guard canSend else { return }
        text = ""
        attachments = []
        photoItems = []
        onSend()
        Task { await chat.send(input, thread: thread, attachments: photos) }
    }

    private func stop(_ task: Nexus_V1_Task) {
        stopping = true
        Task {
            defer { stopping = false }
            do {
                try await store.cancel(taskID: task.id)
            } catch {
                toasts.error("Couldn't stop the reply", error) { stop(task) }
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
}
