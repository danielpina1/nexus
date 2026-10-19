import Foundation
import NexusAPI
import SwiftProtobuf

/// Something the user did while the Hub was unreachable (away from home with the VPN off,
/// or no network): sent in order once the Hub answers again (CLIENTS.md "Offline behaviour").
public struct OutboxItem: Codable, Identifiable, Hashable, Sendable {
    public enum Kind: Codable, Hashable, Sendable {
        /// A command typed in Chat / a favourite (`CommandService.Execute`).
        case command(input: String, agentID: String?, source: String)
        /// A chat message (`Execute` with source "chat" and its conversation id).
        case chat(input: String, conversationID: String)
        /// A reply / answer to a running task (`TaskService.SendMessage`).
        case message(taskID: String, text: String, questionID: String?, choice: String?)
        /// Quick capture into the vault (`VaultService.Capture`).
        case capture(text: String, url: String?)
        /// An approval decision ALREADY signed with Face ID (serialized `DecideRequest`).
        /// The signature is bound to `expiresAt`; after that the Hub/Node would reject it,
        /// so it is dropped with a message instead of being sent.
        case decision(request: Data, approvalID: String, title: String, approve: Bool, expiresAt: Date)
    }

    public var id: String
    /// Sent as the request idempotency key, so a retry after a lost response is harmless.
    public var idempotencyKey: String
    public var createdAt: Date
    public var kind: Kind
    public var attempts: Int
    public var lastError: String?

    public init(kind: Kind, idempotencyKey: String = .idempotencyKey(), createdAt: Date = Date()) {
        self.id = UUID().uuidString
        self.idempotencyKey = idempotencyKey
        self.createdAt = createdAt
        self.kind = kind
        self.attempts = 0
        self.lastError = nil
    }

    /// One-line description for the UI ("Command: buy the usual").
    public var summary: String {
        switch kind {
        case .command(let input, _, _): return input
        case .chat(let input, _): return input
        case .message(_, let text, _, let choice): return choice ?? text
        case .capture(let text, _): return "Capture: " + text
        case .decision(_, _, let title, let approve, _): return (approve ? "Approve: " : "Deny: ") + title
        }
    }

    public var isExpired: Bool {
        if case .decision(_, _, _, _, let expiresAt) = kind { return expiresAt <= Date() }
        return false
    }

    public var taskID: String? {
        if case .message(let taskID, _, _, _) = kind { return taskID }
        return nil
    }
}

/// Small persisted queue in the App Group (shared defaults), so it survives app restarts and
/// is visible to every target. Tiny by design: text only, no attachments.
public enum Outbox {
    /// Hard cap so a forgotten phone cannot pile up an unbounded backlog.
    public static let maxItems = 50

    public static func load() -> [OutboxItem] {
        guard let data = AppGroup.defaults.data(forKey: AppGroup.Key.outbox),
              let items = try? JSONDecoder().decode([OutboxItem].self, from: data) else { return [] }
        return items
    }

    public static func save(_ items: [OutboxItem]) {
        if items.isEmpty {
            AppGroup.defaults.removeObject(forKey: AppGroup.Key.outbox)
        } else if let data = try? JSONEncoder().encode(Array(items.suffix(maxItems))) {
            AppGroup.defaults.set(data, forKey: AppGroup.Key.outbox)
        }
    }

    @discardableResult
    public static func enqueue(_ item: OutboxItem) -> [OutboxItem] {
        var items = load()
        // One decision per approval: a newer signed decision replaces an older queued one.
        if case .decision(_, let approvalID, _, _, _) = item.kind {
            items.removeAll {
                if case .decision(_, let other, _, _, _) = $0.kind { return other == approvalID }
                return false
            }
        }
        items.append(item)
        save(items)
        return items
    }

    public static func remove(id: String) {
        save(load().filter { $0.id != id })
    }

    public static func update(_ item: OutboxItem) {
        var items = load()
        if let index = items.firstIndex(where: { $0.id == item.id }) {
            items[index] = item
            save(items)
        }
    }

    public static func clear() {
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.outbox)
    }
}

/// Pending approvals persisted in the App Group so that tapping an approval notification
/// while the Hub is unreachable still shows the exact action, lets you sign it with Face ID,
/// and queues the signed decision (see `OutboxItem.Kind.decision`).
public enum ApprovalCache {
    public static func load() -> [Nexus_V1_Approval] {
        guard let blobs = AppGroup.defaults.array(forKey: AppGroup.Key.approvalCache) as? [Data] else { return [] }
        return blobs.compactMap { try? Nexus_V1_Approval(serializedBytes: $0) }
    }

    public static func save(_ approvals: [Nexus_V1_Approval]) {
        let now = Date()
        let blobs: [Data] = approvals
            .filter { $0.state == .pending && ($0.expiresDate.map { $0 > now } ?? true) }
            .prefix(50)
            .compactMap { (approval: Nexus_V1_Approval) -> Data? in try? approval.serializedBytes() }
        if blobs.isEmpty {
            AppGroup.defaults.removeObject(forKey: AppGroup.Key.approvalCache)
        } else {
            AppGroup.defaults.set(blobs, forKey: AppGroup.Key.approvalCache)
        }
    }

    public static func clear() {
        AppGroup.defaults.removeObject(forKey: AppGroup.Key.approvalCache)
    }
}

// MARK: - Delivery

public extension NexusActions {
    /// Sends one queued item. Throws on failure (the caller decides whether to keep it).
    func deliver(_ item: OutboxItem) async throws {
        switch item.kind {
        case .command(let input, let agentID, let source):
            _ = try await execute(input, agentID: agentID, source: source, idempotencyKey: item.idempotencyKey)
        case .chat(let input, let conversationID):
            _ = try await execute(input, source: "chat", conversationID: conversationID, idempotencyKey: item.idempotencyKey)
        case .message(let taskID, let text, let questionID, let choice):
            try await sendMessage(taskID: taskID, text: text, questionID: questionID, choice: choice, idempotencyKey: item.idempotencyKey)
        case .capture(let text, let url):
            _ = try await capture(text, url: url, source: "ios-capture", idempotencyKey: item.idempotencyKey)
        case .decision(let data, _, _, _, _):
            let request = try Nexus_V1_DecideRequest(serializedBytes: data)
            _ = try await send(decision: request)
        }
    }
}
