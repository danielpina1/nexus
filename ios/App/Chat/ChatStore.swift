import Foundation
import NexusCore
import Observation

/// The chat's conversations (web lib/chat.ts): messages per thread, persisted on this iPhone,
/// and one `conversation_id` per thread sent with every message (CommandContext source "chat").
/// "New chat" starts a new conversation id. Replies render live from `NexusStore.timelines`;
/// the final text, engine and first-token time are kept here so old bubbles render without
/// replaying events.
@Observable
@MainActor
final class ChatStore {
    struct Source: Codable, Hashable {
        var id: String
        var path: String
        var title: String
    }

    struct Message: Codable, Identifiable, Hashable {
        enum Role: String, Codable { case user, nexus }

        var id: String
        var role: Role
        var text: String
        var at: Date
        var pending = false
        var error: String?
        var taskID: String?
        /// Outbox item id while the message waits for the Hub (away, VPN off).
        var queuedID: String?
        /// The prompt the Hub stores on the task (to find the task once a queued message is sent).
        var prompt: String?
        /// "search" / "capture" / "workflow" for replies that are not plain answers.
        var routeKind: String?
        var sources: [Source]?
        /// Router question choices (only when the Hub falls back to the router).
        var choices: [String]?
        var originalInput: String?
        /// Final answer of the task, kept so old bubbles render without the run's events.
        var finalText: String?
        /// "Claude Code · claude-sonnet-4-5-20250929" of the run that answered.
        var engine: String?
        /// "Assistant", "Researcher", …
        var agentLabel: String?
        /// Send → first streamed token, measured on the phone.
        var firstTokenMs: Int?
    }

    private struct Stored: Codable {
        var threads: [String: [Message]]
        var conversations: [String: String]
    }

    private(set) var threads: [String: [Message]] = [:]
    private(set) var conversations: [String: String] = [:]

    @ObservationIgnored let store: NexusStore
    @ObservationIgnored private var demoSeeded = false
    private static let storageKey = "nexus.chat.v1"
    private static let maxMessages = 200

    init(store: NexusStore) {
        self.store = store
        if let data = UserDefaults.standard.data(forKey: Self.storageKey),
           let stored = try? JSONDecoder().decode(Stored.self, from: data) {
            threads = stored.threads.mapValues { messages in
                messages.map { message in
                    var m = message
                    m.pending = false
                    return m
                }
            }
            conversations = stored.conversations
        }
    }

    // MARK: Threads

    /// Storage key of a thread; demo conversations live apart from the real ones.
    func key(_ thread: ChatThread) -> String {
        (store.isDemo ? "demo:" : "") + (thread.agentID.map { "agent:" + $0 } ?? "nexus")
    }

    func messages(_ thread: ChatThread) -> [Message] {
        threads[key(thread)] ?? []
    }

    func conversationID(_ thread: ChatThread) -> String {
        let k = key(thread)
        if let id = conversations[k] { return id }
        let id = UUID().uuidString.lowercased()
        conversations[k] = id
        persist()
        return id
    }

    /// "New chat": forgets the messages and starts a new conversation (new CLI session).
    func newChat(_ thread: ChatThread) {
        let k = key(thread)
        threads[k] = []
        conversations[k] = UUID().uuidString.lowercased()
        persist()
    }

    /// The task of the latest reply while it still runs (drives Stop and "busy").
    func activeTask(_ thread: ChatThread) -> Nexus_V1_Task? {
        guard let last = messages(thread).last(where: { $0.role == .nexus }), let id = last.taskID,
              let task = store.tasks[id], task.state.isActive else { return nil }
        return task
    }

    func isBusy(_ thread: ChatThread) -> Bool {
        guard let last = messages(thread).last(where: { $0.role == .nexus }) else { return false }
        return last.pending || activeTask(thread) != nil
    }

    // MARK: Sending

    /// Sends a chat line. `forcedAgent` re-sends a router question's input to the chosen agent.
    func send(_ raw: String, thread: ChatThread, attachments: [Nexus_V1_Attachment] = [], forcedAgent: String? = nil) async {
        var text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || !attachments.isEmpty else { return }
        if let forcedAgent { text = "@\(forcedAgent) \(text)" }
        let command = ChatCommand.parse(text)
        let wire = ChatCommand.wireInput(text, threadAgentID: thread.agentID)
        let k = key(thread)
        let user = Message(id: Self.newID(), role: .user, text: text, at: Date())
        var reply = Message(id: Self.newID(), role: .nexus, text: "", at: Date(), pending: true)
        reply.prompt = command.prompt
        append([user, reply], to: k)

        if store.isDemo {
            ChatDemo.run(input: wire, command: command, replyID: reply.id, threadKey: k, chat: self)
            return
        }
        do {
            let delivery = try await store.sendChat(wire, conversationID: conversationID(thread), attachments: attachments)
            switch delivery {
            case .queued(let item):
                update(reply.id, in: k) {
                    $0.pending = false
                    $0.queuedID = item.id
                }
            case .sent(let response):
                apply(response, to: reply.id, in: k, input: text)
            }
        } catch {
            update(reply.id, in: k) {
                $0.pending = false
                $0.error = error.localizedDescription
            }
        }
    }

    /// Records the Hub's answer to a chat message on its reply bubble.
    func apply(_ response: Nexus_V1_ExecuteResponse, to replyID: String, in threadKey: String, input: String) {
        update(replyID, in: threadKey) { m in
            m.pending = false
            m.queuedID = nil
            if response.hasTask { m.taskID = response.task.id }
            if response.hasRoute {
                switch response.route.kind {
                case .search: m.routeKind = "search"
                case .capture: m.routeKind = "capture"
                case .workflow: m.routeKind = "workflow"
                default: break
                }
            }
            if !response.sources.isEmpty {
                m.sources = response.sources.map { Source(id: $0.id, path: $0.path, title: $0.title) }
            }
            if !response.question.isEmpty {
                m.text = response.question
                m.choices = response.choices
                m.originalInput = input
            } else if !response.answer.isEmpty {
                m.text = response.answer
            } else if !response.hasTask {
                m.text = m.routeKind == "capture" ? "Saved to your vault." : "Done."
            }
        }
    }

    /// Links queued messages to their task once the outbox sent them.
    func resolveQueued() {
        let queuedIDs = Set(store.outbox.map(\.id))
        for (k, messages) in threads {
            for message in messages where message.queuedID != nil && !queuedIDs.contains(message.queuedID!) {
                let match = store.tasks.values
                    .filter { $0.isChat && $0.prompt == (message.prompt ?? "") && $0.createdAt.date >= message.at.addingTimeInterval(-5) }
                    .min { $0.createdAt.date < $1.createdAt.date }
                update(message.id, in: k) { m in
                    m.queuedID = nil
                    if let match {
                        m.taskID = match.id
                    } else {
                        m.text = "Sent while you were away. The reply is in Work → Tasks."
                    }
                }
            }
        }
    }

    /// Keeps facts about a reply once known (final text, engine, first-token time).
    func annotate(_ id: String, in threadKey: String, finalText: String? = nil, engine: String? = nil, agentLabel: String? = nil, firstTokenMs: Int? = nil) {
        update(id, in: threadKey) { m in
            if let finalText { m.finalText = finalText }
            if let engine, !engine.isEmpty { m.engine = engine }
            if let agentLabel, !agentLabel.isEmpty { m.agentLabel = agentLabel }
            if let firstTokenMs, m.firstTokenMs == nil { m.firstTokenMs = firstTokenMs }
        }
    }

    // MARK: Demo

    /// Demo mode: a conversation that already shows a finished answer, a question and an
    /// approval, so the screen works without a Hub.
    func seedDemoIfNeeded() {
        guard store.isDemo, !demoSeeded else { return }
        demoSeeded = true
        let k = key(ChatThread(agentID: nil))
        guard (threads[k] ?? []).isEmpty else { return }
        threads[k] = ChatDemo.seed(chat: self)
    }

    // MARK: Internals

    func update(_ id: String, in threadKey: String, _ change: (inout Message) -> Void) {
        guard var list = threads[threadKey], let index = list.firstIndex(where: { $0.id == id }) else { return }
        change(&list[index])
        threads[threadKey] = list
        persist()
    }

    func append(_ messages: [Message], to threadKey: String) {
        var list = threads[threadKey] ?? []
        list.append(contentsOf: messages)
        if list.count > Self.maxMessages { list.removeFirst(list.count - Self.maxMessages) }
        threads[threadKey] = list
        persist()
    }

    private func persist() {
        // Demo conversations are never stored.
        let real = threads.filter { !$0.key.hasPrefix("demo:") }
        let conv = conversations.filter { !$0.key.hasPrefix("demo:") }
        if let data = try? JSONEncoder().encode(Stored(threads: real, conversations: conv)) {
            UserDefaults.standard.set(data, forKey: Self.storageKey)
        }
    }

    static func newID() -> String {
        String(UUID().uuidString.prefix(8)).lowercased()
    }
}
