import Foundation
import NexusAPI
import SwiftProtobuf

// Chat logic shared by the app's chat, task page and tests (ports of web/src/lib/chat.ts,
// runItems.ts, runMeta.ts and backgroundClaim.ts). Chat messages are sent with
// CommandContext{source: "chat", conversation_id}; the Hub routes them without the LLM router
// (backend/internal/command/chat.go, docs/AGENTS.md §2.4).

/// What a chat line asks for (mirrors the Hub's chat routing).
public enum ChatCommand: Equatable, Sendable {
    /// Everything else: the assistant (or the thread's agent) answers.
    case message(String)
    /// "@agent text" / "@team text" (also "@team:<id> text").
    case mention(target: String, text: String)
    /// "/search query".
    case search(String)
    /// "/remember x", "remember x", "remember that x".
    case remember(String)

    public static func parse(_ raw: String) -> ChatCommand {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if let g = groups(#"^/search\s+([\s\S]+)$"#, text, caseInsensitive: true) {
            return .search(g[0].trimmingCharacters(in: .whitespacesAndNewlines))
        }
        if let g = groups(#"^(?:/remember|remember)(?:\s+that)?\s+([\s\S]+)$"#, text, caseInsensitive: true) {
            return .remember(g[0].trimmingCharacters(in: .whitespacesAndNewlines))
        }
        if let g = groups(#"^@((?:team:)?[\w.-]+)\s+([\s\S]+)$"#, text, caseInsensitive: false), g.count == 2 {
            var target = g[0]
            if target.hasPrefix("team:") { target = String(target.dropFirst(5)) }
            return .mention(target: target, text: g[1].trimmingCharacters(in: .whitespacesAndNewlines))
        }
        return .message(text)
    }

    /// The input sent to the Hub. Agent threads address their agent with "@agent" unless the
    /// line already is a command; "@team:<id>" becomes "@<id>" (the Hub checks teams first).
    public static func wireInput(_ raw: String, threadAgentID: String?) -> String {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        switch parse(text) {
        case .mention(let target, let body):
            return "@\(target) \(body)"
        case .message(let body):
            if let agent = threadAgentID, !agent.isEmpty { return "@\(agent) \(body)" }
            return body
        case .search, .remember:
            return text
        }
    }

    /// Short label for the user's bubble ("Search", "Remember", "@researcher"), nil for plain text.
    public var label: String? {
        switch self {
        case .message: return nil
        case .mention(let target, _): return "@" + target
        case .search: return "Search"
        case .remember: return "Remember"
        }
    }

    /// The prompt the Hub stores on the task (the @mention is stripped).
    public var prompt: String {
        switch self {
        case .message(let t), .search(let t), .remember(let t): return t
        case .mention(_, let t): return t
        }
    }

    static func groups(_ pattern: String, _ text: String, caseInsensitive: Bool) -> [String]? {
        guard let regex = try? NSRegularExpression(pattern: pattern, options: caseInsensitive ? [.caseInsensitive] : []) else { return nil }
        let range = NSRange(text.startIndex..., in: text)
        guard let match = regex.firstMatch(in: text, options: [], range: range) else { return nil }
        var out: [String] = []
        for index in 1..<match.numberOfRanges {
            guard let r = Range(match.range(at: index), in: text) else { return nil }
            out.append(String(text[r]))
        }
        return out.isEmpty ? nil : out
    }
}

/// A run's timeline grouped into what the chat bubble shows: streamed text blocks, tool chips,
/// questions, approvals (latest state) and artifacts. Costs, state changes and notices are
/// not shown and do not split a text block (web runItems.buildItems).
public struct ChatItem: Identifiable, Sendable {
    public enum Kind: Sendable {
        case text(String, streaming: Bool)
        case tool(Nexus_V1_ToolCall, result: Nexus_V1_ToolResult?)
        case question(Nexus_V1_Question)
        case approval(Nexus_V1_Approval)
        case artifact(Nexus_V1_Attachment)
    }

    public var id: String
    public var kind: Kind

    public init(id: String, kind: Kind) {
        self.id = id
        self.kind = kind
    }

    public var isText: Bool {
        if case .text = kind { return true }
        return false
    }

    public static func build(_ entries: [TimelineItem]) -> [ChatItem] {
        var items: [ChatItem] = []
        var textIndex: Int?
        var approvalIndex: [String: Int] = [:]
        for entry in entries {
            switch entry.kind {
            case .output(let chunk, let isFinal):
                if let i = textIndex, case .text(let text, _) = items[i].kind {
                    items[i].kind = .text(text + chunk, streaming: !isFinal)
                } else if !chunk.isEmpty {
                    items.append(ChatItem(id: entry.id, kind: .text(chunk, streaming: !isFinal)))
                    textIndex = items.count - 1
                }
            case .toolCall(let call, let result):
                items.append(ChatItem(id: entry.id, kind: .tool(call, result: result)))
                textIndex = nil
            case .question(let question):
                items.append(ChatItem(id: entry.id, kind: .question(question)))
                textIndex = nil
            case .approval(let approval):
                if let i = approvalIndex[approval.id] {
                    items[i].kind = .approval(approval)
                } else {
                    approvalIndex[approval.id] = items.count
                    items.append(ChatItem(id: entry.id, kind: .approval(approval)))
                    textIndex = nil
                }
            case .artifact(let attachment):
                items.append(ChatItem(id: entry.id, kind: .artifact(attachment)))
                textIndex = nil
            case .userMessage:
                // A reply / answer ends the current text block (not shown in the bubble).
                textIndex = nil
            case .cost, .status, .notice:
                break
            }
        }
        return items
    }

    /// Plain text of the streamed text blocks.
    public static func replyText(_ items: [ChatItem]) -> String {
        items.compactMap { item -> String? in
            if case .text(let text, _) = item.kind { return text }
            return nil
        }
        .joined(separator: "\n\n")
        .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// The engine and exact model that served a run (web runMeta.ts).
public struct RunModel: Equatable, Sendable {
    /// Runtime id ("claude-code"), "" while unknown.
    public var runtime: String
    /// Exact model id ("claude-sonnet-4-5-20250929"), "" = CLI default / unknown.
    public var model: String
    /// A cost event named the runtime (the run started on a machine).
    public var known: Bool

    public init(runtime: String, model: String, known: Bool) {
        self.runtime = runtime
        self.model = model
        self.known = known
    }

    static let engines: [String: String] = [
        "claude-code": "Claude Code", "codex": "Codex", "openai": "OpenAI", "ollama": "Ollama",
        "anthrex": "Anthrex", "shell": "Shell", "browser": "Browser", "echo": "Echo", "workflow": "Workflow",
    ]

    /// From the latest cost event that names the runtime (runtimes announce it at start with a
    /// zero cost and repeat it in the final cost), else the task's accumulated cost.
    public static func from(_ entries: [TimelineItem], task: Nexus_V1_Task?) -> RunModel {
        for entry in entries.reversed() {
            if case .cost(let cost) = entry.kind, !cost.runtime.isEmpty {
                return RunModel(runtime: cost.runtime, model: cost.model, known: true)
            }
        }
        if let task, task.hasCost, !task.cost.runtime.isEmpty {
            return RunModel(runtime: task.cost.runtime, model: task.cost.model, known: true)
        }
        return RunModel(runtime: "", model: task?.cost.model ?? "", known: false)
    }

    public static func engineLabel(_ runtime: String) -> String {
        runtime.isEmpty ? "" : (engines[runtime] ?? runtime)
    }

    /// "claude-sonnet-4-5-20250929", or "Codex default" when the CLI picked its own model.
    public var modelLabel: String {
        if !model.isEmpty { return model }
        if runtime == "codex" { return "Codex default" }
        return known ? "default model" : ""
    }

    /// "Claude Code · claude-sonnet-4-5-20250929".
    public var label: String {
        [Self.engineLabel(runtime), modelLabel].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

/// Safety net: a final answer that claims work is still going on in the background while Nexus
/// tracks no running child task is flagged (web lib/backgroundClaim.ts).
public enum BackgroundClaim {
    public static let warning = "This answer mentions background work that Nexus isn't tracking."

    static let pattern = #"\b(running|run|working|continu\w*) in the background\b|\b(i['’]?ll|i will|will) (report|get|come) back\b|\bonce (it|they|the agent|the subagent)s? (finish|finishes|complete|completes|is done|are done)\b|\b(still|is|are) running in parallel\b|\bin a background (task|agent|process)\b"#

    static let regex = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive])

    public static func claims(_ text: String) -> Bool {
        guard let regex else { return false }
        return regex.firstMatch(in: text, options: [], range: NSRange(text.startIndex..., in: text)) != nil
    }

    public static func untracked(answer: String, taskActive: Bool, activeChildren: Int) -> Bool {
        !taskActive && activeChildren == 0 && !answer.isEmpty && claims(answer)
    }
}

public extension Nexus_V1_Task {
    /// A chat message's task (CommandContext.source "chat"; params carry chat=true).
    var isChat: Bool { source == "chat" }
}
