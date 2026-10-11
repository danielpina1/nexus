import Foundation
import NexusAPI
import SwiftProtobuf

/// An `ask_user` question waiting for an answer.
public struct QuestionItem: Identifiable, Hashable, Sendable {
    public var id: String { question.questionID }
    public var taskID: String
    public var question: Nexus_V1_Question
    public var at: Date

    public init(taskID: String, question: Nexus_V1_Question, at: Date) {
        self.taskID = taskID
        self.question = question
        self.at = at
    }
}

/// A notice with the time it arrived.
public struct NoticeItem: Identifiable, Hashable, Sendable {
    public var id: String { notice.id }
    public var taskID: String
    public var notice: Nexus_V1_Notice
    public var at: Date

    public init(taskID: String, notice: Nexus_V1_Notice, at: Date) {
        self.taskID = taskID
        self.notice = notice
        self.at = at
    }
}

/// One card in the Inbox (CLIENTS.md §1 ranking).
public enum InboxItem: Identifiable, Hashable, Sendable {
    case approval(Nexus_V1_Approval)
    case question(QuestionItem)
    case notice(NoticeItem)

    public var id: String {
        switch self {
        case .approval(let a): return "apr:" + a.id
        case .question(let q): return "q:" + q.id
        case .notice(let n): return "n:" + n.id
        }
    }

    /// Lower = more important.
    public var rank: Int {
        switch self {
        case .approval(let a): return a.level == .biometric ? 0 : 1
        case .question: return 2
        case .notice(let n):
            switch n.notice.kind {
            case .alert: return 3
            case .result: return 4
            case .factReview: return 5
            case .suggestion: return 6
            default: return 7
            }
        }
    }

    public var date: Date {
        switch self {
        case .approval(let a): return a.createdAt.optionalDate ?? .distantPast
        case .question(let q): return q.at
        case .notice(let n): return n.at
        }
    }

    public var taskID: String {
        switch self {
        case .approval(let a): return a.taskID
        case .question(let q): return q.taskID
        case .notice(let n): return n.taskID
        }
    }
}

/// One entry of a task's timeline / live run view.
public struct TimelineEntry: Identifiable, Sendable {
    public enum Kind: Sendable {
        case userMessage(String)
        case output(String, isFinal: Bool)
        case toolCall(Nexus_V1_ToolCall, result: Nexus_V1_ToolResult?)
        case question(Nexus_V1_Question)
        case approval(Nexus_V1_Approval)
        case cost(Nexus_V1_Cost)
        case artifact(Nexus_V1_Attachment)
        case status(Nexus_V1_TaskState, summary: String)
        case notice(Nexus_V1_Notice)
    }

    public var id: String
    public var at: Date
    public var runID: String
    public var kind: Kind

    public init(id: String, at: Date, runID: String, kind: Kind) {
        self.id = id
        self.at = at
        self.runID = runID
        self.kind = kind
    }
}

// MARK: - Display helpers

public extension Nexus_V1_TaskState {
    var isActive: Bool {
        switch self {
        case .queued, .routing, .needsInput, .assigned, .running, .waitingApproval, .waitingInput, .escalating:
            return true
        default:
            return false
        }
    }

    var isTerminal: Bool {
        self == .succeeded || self == .failed || self == .cancelled
    }

    var needsUser: Bool {
        self == .waitingApproval || self == .waitingInput || self == .needsInput
    }

    var label: String {
        switch self {
        case .unspecified: return "Unknown"
        case .queued: return "Queued"
        case .routing: return "Routing"
        case .needsInput: return "Needs input"
        case .assigned: return "Assigned"
        case .running: return "Running"
        case .waitingApproval: return "Waiting for approval"
        case .waitingInput: return "Waiting for you"
        case .escalating: return "Trying harder"
        case .succeeded: return "Done"
        case .failed: return "Failed"
        case .cancelled: return "Cancelled"
        case .UNRECOGNIZED: return "Unknown"
        }
    }

    var symbol: String {
        switch self {
        case .queued, .routing, .assigned: return "clock"
        case .running, .escalating: return "play.circle.fill"
        case .needsInput, .waitingInput: return "questionmark.bubble.fill"
        case .waitingApproval: return "hand.raised.fill"
        case .succeeded: return "checkmark.circle.fill"
        case .failed: return "xmark.octagon.fill"
        case .cancelled: return "stop.circle"
        default: return "circle.dashed"
        }
    }
}

public extension Nexus_V1_Task {
    var costCents: Int { hasCost ? Int(cost.amount.minorUnits) : 0 }
    var displayTitle: String { title.isEmpty ? (prompt.isEmpty ? id : prompt) : title }
}

public extension Nexus_V1_Agent {
    /// The SF Symbol for the agent (`icon` in the agent note), or a default.
    var symbolName: String {
        AgentIcon.isSFSymbol(icon) ? icon : "sparkles"
    }

    /// Emoji icon if the note uses one.
    var emoji: String? {
        (!icon.isEmpty && !AgentIcon.isSFSymbol(icon)) ? icon : nil
    }
}

public enum AgentIcon {
    public static func isSFSymbol(_ icon: String) -> Bool {
        !icon.isEmpty && icon.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == ".") }
    }
}

public extension Nexus_V1_Approval {
    var isBiometric: Bool { level == .biometric }
    var expiresDate: Date? { hasExpiresAt ? expiresAt.date : nil }

    /// Pretty-printed JSON of the exact action payload (what you see is what you sign).
    var payloadPreview: String {
        guard hasPayload else { return "" }
        if let json = try? payload.jsonString(),
           let data = json.data(using: .utf8),
           let object = try? JSONSerialization.jsonObject(with: data),
           let pretty = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys]),
           let string = String(data: pretty, encoding: .utf8) {
            return string
        }
        return (try? payload.jsonString()) ?? ""
    }
}

public extension Nexus_V1_ActionClass {
    var label: String {
        switch self {
        case .read: return "Read"
        case .writeInternal: return "Write (internal)"
        case .compute: return "Compute"
        case .writeExternal: return "Write (external)"
        case .codePush: return "Code push"
        case .spend: return "Spend"
        case .destructive: return "Destructive"
        case .deviceControl: return "Device control"
        default: return "Action"
        }
    }
}

public extension Google_Protobuf_Struct {
    /// Compact one-line JSON for tool inputs/outputs.
    var compactJSON: String {
        (try? jsonString()) ?? "{}"
    }
}
