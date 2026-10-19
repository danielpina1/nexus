import Foundation
import NexusAPI
import SwiftProtobuf

// Human-readable tool calls, shared by the chat chips, the run timeline, the inbox approval
// cards and the approval sheet (port of web/src/lib/tools.ts + components/ToolView.tsx).
//
// Tool names arrive as Nexus names ("memory.search"), Claude Code built-ins ("Bash", "Edit"),
// Claude Code MCP names ("mcp__nexus__memory_search", "mcp__github__create_issue") or Codex
// names ("shell", "apply_patch", "nexus.memory.search").

/// A tool name reduced to its canonical form.
public struct NormalTool: Equatable, Sendable {
    /// Nexus dotted name, or the client's own tool name.
    public var name: String
    /// MCP server of a foreign MCP tool ("github"), "" otherwise.
    public var server: String
}

/// One-line, past-tense summary of a tool call plus an SF Symbol.
public struct ToolTitle: Equatable, Sendable {
    public var title: String
    public var symbol: String
}

/// How a tool's details are rendered (one renderer per family).
public enum ToolDetailKind: Equatable, Sendable {
    /// Amount and merchant up front (checkout, shops).
    case spend(amount: String, merchant: String)
    /// Shell command, optional cwd and description.
    case command(command: String, cwd: String, description: String)
    /// Line diff of an edit.
    case edit(path: String, lines: [DiffLine])
    /// File written with its first lines.
    case write(path: String, preview: String, lineCount: Int)
    /// URL fetch (+ prompt).
    case fetch(url: String, prompt: String)
    /// Web search query.
    case search(query: String)
    /// AskUserQuestion: the questions with their options.
    case questions([QuestionPreview])
    /// Any other tool (MCP, Nexus tools): its inputs as a key/value list.
    case inputs(label: String, pairs: [KeyValue])
}

public struct KeyValue: Equatable, Sendable, Identifiable {
    public var id: String { key }
    public var key: String
    public var value: String
}

public struct QuestionPreview: Equatable, Sendable, Identifiable {
    public var id: String { question }
    public var header: String
    public var question: String
    public var options: [Option]

    public struct Option: Equatable, Sendable, Identifiable {
        public var id: String { label }
        public var label: String
        public var description: String
    }
}

/// One line of a unified diff body.
public struct DiffLine: Equatable, Sendable, Identifiable {
    public enum Kind: Sendable { case context, added, removed, separator }
    public var id: Int
    public var kind: Kind
    public var text: String
}

public enum ToolText {
    static let nexusNamespaces: Set<String> = [
        "memory", "vault", "web", "shell", "fs", "team", "workflow", "browser", "nexus", "shops", "facts",
        "architect", "github", "email", "git", "anthrex", "router", "tools",
    ]

    public static func normalize(_ tool: String) -> NormalTool {
        var t = tool
        if t.hasPrefix("mcp__") {
            let parts = t.dropFirst(5).components(separatedBy: "__")
            let server = parts.first ?? ""
            let name = parts.dropFirst().joined(separator: "__")
            if server != "nexus" { return NormalTool(name: name, server: server) }
            t = name
        }
        let dotted = t.split(separator: ".", omittingEmptySubsequences: false)
        if t.hasPrefix("nexus."), dotted.count > 1, nexusNamespaces.contains(String(dotted[1])) {
            t = String(t.dropFirst(6))
        }
        if !t.contains("."), let underscore = t.firstIndex(of: "_"), underscore != t.startIndex {
            let prefix = String(t[..<underscore])
            if nexusNamespaces.contains(prefix) {
                t = prefix + "." + String(t[t.index(after: underscore)...])
            }
        }
        return NormalTool(name: t, server: "")
    }

    /// "nexus · vault.write" / "github · create_issue" / "Bash".
    public static func friendlyName(_ tool: String) -> String {
        let n = normalize(tool)
        if !n.server.isEmpty { return "\(n.server) · \(n.name)" }
        if tool.hasPrefix("mcp__nexus__") || tool.contains(".") { return "nexus · \(n.name)" }
        return n.name
    }

    public static func clip(_ s: String, _ n: Int) -> String {
        s.count > n ? String(s.prefix(n - 1)) + "…" : s
    }

    static func base(_ path: String) -> String {
        path.split(separator: "/").last.map(String.init) ?? path
    }

    static func host(_ url: String) -> String {
        URL(string: url)?.host ?? url
    }

    /// "Ran go test ./...", "Searched the vault for “x”", "Edited main.go", …
    public static func title(_ tool: String, input: Google_Protobuf_Struct = Google_Protobuf_Struct()) -> ToolTitle {
        let n = normalize(tool)
        let path = input.string("file_path", "path", "note", "notebook_path")
        let q = input.string("query", "q", "pattern")
        switch n.name {
        case "Bash", "shell", "shell.exec", "shell.run":
            let cmd = clip(input.string("command", "cmd"), 70)
            return ToolTitle(title: cmd.isEmpty ? "Ran a command" : "Ran \(cmd)", symbol: "terminal")
        case "Read", "fs.read", "NotebookRead":
            return ToolTitle(title: "Read file \(base(path))", symbol: "doc.text")
        case "Edit", "MultiEdit", "NotebookEdit", "apply_patch":
            return ToolTitle(title: path.isEmpty ? "Edited files" : "Edited \(base(path))", symbol: "pencil.line")
        case "Write":
            return ToolTitle(title: "Wrote \(base(path))", symbol: "square.and.pencil")
        case "Grep", "Glob", "LS":
            return ToolTitle(title: q.isEmpty ? "Listed \(path.isEmpty ? "files" : base(path))" : "Searched files for “\(clip(q, 50))”", symbol: "magnifyingglass")
        case "WebFetch", "web.fetch":
            return ToolTitle(title: "Fetched \(host(input.string("url")))", symbol: "globe")
        case "WebSearch", "web_search", "web.search":
            return ToolTitle(title: "Searched the web for “\(clip(q, 60))”", symbol: "globe")
        case "memory.search":
            return ToolTitle(title: q.isEmpty ? "Searched the vault" : "Searched the vault for “\(clip(q, 60))”", symbol: "books.vertical")
        case "vault.read":
            return ToolTitle(title: "Read note \(base(path))".trimmingCharacters(in: .whitespaces), symbol: "books.vertical")
        case "vault.write", "vault.daily_append":
            return ToolTitle(title: "Wrote note \(base(path))".trimmingCharacters(in: .whitespaces), symbol: "books.vertical")
        case "vault.query", "vault.related", "vault.tasks":
            return ToolTitle(title: "Looked through the vault", symbol: "books.vertical")
        case "spawn_agent":
            let agent = input.string("agent_id")
            return ToolTitle(title: "Delegated to \(agent.isEmpty ? "an agent" : agent)", symbol: "cpu")
        case "team.delegate", "team.delegate_many", "team.spawn":
            let role = input.string("role", "agent")
            return ToolTitle(title: "Delegated to \(role.isEmpty ? "team members" : role)", symbol: "person.2")
        case "team.await":
            return ToolTitle(title: "Waited for team results", symbol: "person.2")
        case "ask_user", "AskUserQuestion":
            return ToolTitle(title: "Asked you a question", symbol: "questionmark.bubble")
        case "nexus.status":
            return ToolTitle(title: "Checked Nexus status", symbol: "safari")
        case "workflow.list":
            return ToolTitle(title: "Listed workflows", symbol: "list.bullet.rectangle")
        case "workflow.run":
            return ToolTitle(title: "Started workflow \(input.string("workflow_id", "id"))", symbol: "list.bullet.rectangle")
        case "TodoWrite":
            return ToolTitle(title: "Updated its plan", symbol: "checklist")
        case "notify":
            return ToolTitle(title: "Sent you a notification", symbol: "bell")
        default:
            break
        }
        if n.name.hasPrefix("browser.") {
            return ToolTitle(title: "Browser · " + n.name.dropFirst(8).replacingOccurrences(of: "_", with: " "), symbol: "globe")
        }
        if !n.server.isEmpty { return ToolTitle(title: "Used \(n.server) · \(n.name)", symbol: "wrench.and.screwdriver") }
        return ToolTitle(title: "Used \(n.name)", symbol: "wrench.and.screwdriver")
    }

    /// Spend payloads (browser checkout, shops): amount and merchant.
    public static func spend(_ input: Google_Protobuf_Struct) -> (amount: String, merchant: String)? {
        let keys = ["amount", "total", "price", "amount_eur", "total_eur"]
        guard let value = keys.lazy.compactMap({ input.fields[$0] }).first else { return nil }
        let merchant = input.string("merchant", "shop", "store", "site", "vendor")
        let currency = input.string("currency")
        switch value.kind {
        case .numberValue(let number):
            let formatter = NumberFormatter()
            formatter.numberStyle = .currency
            formatter.currencyCode = currency.isEmpty ? "EUR" : currency
            return (formatter.string(from: NSNumber(value: number)) ?? String(number), merchant)
        case .stringValue(let text):
            return text.isEmpty ? nil : (text, merchant)
        default:
            return nil
        }
    }

    /// The readable body of a tool call (command, diff, URL, inputs…).
    public static func detail(_ tool: String, input: Google_Protobuf_Struct, cwd: String = "") -> ToolDetailKind {
        if let spend = spend(input) {
            return .spend(amount: spend.amount, merchant: spend.merchant)
        }
        let n = normalize(tool)
        switch n.name {
        case "Bash", "shell", "shell.exec", "shell.run":
            return .command(
                command: input.string("command", "cmd"),
                cwd: cwd.isEmpty ? input.string("cwd") : cwd,
                description: input.string("description")
            )
        case "Edit", "MultiEdit":
            var edits: [(String, String)] = []
            if case .listValue(let list)? = input.fields["edits"]?.kind {
                for value in list.values {
                    if case .structValue(let edit) = value.kind {
                        edits.append((edit.string("old_string"), edit.string("new_string")))
                    }
                }
            } else {
                edits.append((input.string("old_string"), input.string("new_string")))
            }
            var lines: [DiffLine] = []
            for (index, edit) in edits.enumerated() {
                if index > 0 { lines.append(DiffLine(id: lines.count, kind: .separator, text: "@@")) }
                for line in lineDiff(edit.0, edit.1) {
                    lines.append(DiffLine(id: lines.count, kind: line.kind, text: line.text))
                }
            }
            return .edit(path: input.string("file_path"), lines: lines)
        case "Write":
            let content = input.string("content")
            let all = content.components(separatedBy: "\n")
            let preview = all.prefix(40).joined(separator: "\n") + (all.count > 40 ? "\n…" : "")
            return .write(path: input.string("file_path"), preview: preview, lineCount: all.count)
        case "WebFetch", "web.fetch":
            return .fetch(url: input.string("url"), prompt: input.string("prompt"))
        case "WebSearch", "web.search", "web_search":
            return .search(query: input.string("query", "q"))
        case "AskUserQuestion":
            return .questions(questionPreviews(input))
        default:
            let label = n.server.isEmpty ? friendlyName(tool) : "\(n.server) · \(n.name)"
            return .inputs(label: label, pairs: keyValues(input))
        }
    }

    public static func keyValues(_ input: Google_Protobuf_Struct) -> [KeyValue] {
        input.fields.keys.sorted().compactMap { key in
            guard let value = input.fields[key] else { return nil }
            let text = value.displayString
            if text.isEmpty { return nil }
            return KeyValue(key: key, value: clip(text, 600))
        }
    }

    static func questionPreviews(_ input: Google_Protobuf_Struct) -> [QuestionPreview] {
        guard case .listValue(let list)? = input.fields["questions"]?.kind else { return [] }
        return list.values.compactMap { value in
            guard case .structValue(let q) = value.kind else { return nil }
            var options: [QuestionPreview.Option] = []
            if case .listValue(let opts)? = q.fields["options"]?.kind {
                for option in opts.values {
                    if case .structValue(let o) = option.kind {
                        options.append(.init(label: o.string("label"), description: o.string("description")))
                    }
                }
            }
            return QuestionPreview(header: q.string("header"), question: q.string("question"), options: options)
        }
    }

    // MARK: Diffs

    /// Minimal line diff (LCS). Inputs are small (tool edits); huge inputs fall back to −/+.
    public static func lineDiff(_ oldText: String, _ newText: String) -> [DiffLine] {
        func split(_ s: String) -> [String] {
            guard !s.isEmpty else { return [] }
            var body = s
            if body.hasSuffix("\n") { body.removeLast() }
            return body.components(separatedBy: "\n")
        }
        let a = split(oldText)
        let b = split(newText)
        var out: [DiffLine] = []
        func add(_ kind: DiffLine.Kind, _ text: String) { out.append(DiffLine(id: out.count, kind: kind, text: text)) }
        if a.count * b.count > 250_000 {
            a.forEach { add(.removed, $0) }
            b.forEach { add(.added, $0) }
            return out
        }
        var dp = Array(repeating: Array(repeating: 0, count: b.count + 1), count: a.count + 1)
        if !a.isEmpty && !b.isEmpty {
            for i in stride(from: a.count - 1, through: 0, by: -1) {
                for j in stride(from: b.count - 1, through: 0, by: -1) {
                    dp[i][j] = a[i] == b[j] ? dp[i + 1][j + 1] + 1 : max(dp[i + 1][j], dp[i][j + 1])
                }
            }
        }
        var i = 0
        var j = 0
        while i < a.count && j < b.count {
            if a[i] == b[j] {
                add(.context, a[i]); i += 1; j += 1
            } else if dp[i + 1][j] >= dp[i][j + 1] {
                add(.removed, a[i]); i += 1
            } else {
                add(.added, b[j]); j += 1
            }
        }
        while i < a.count { add(.removed, a[i]); i += 1 }
        while j < b.count { add(.added, b[j]); j += 1 }
        return out
    }

    public static func diffStats(_ lines: [DiffLine]) -> (added: Int, removed: Int) {
        (lines.filter { $0.kind == .added }.count, lines.filter { $0.kind == .removed }.count)
    }
}

// MARK: - Struct / Value helpers

public extension Google_Protobuf_Value {
    /// Strings as they are, null as "", anything else as compact JSON (web `str()`).
    var displayString: String {
        switch kind {
        case .stringValue(let s): return s
        case .nullValue, .none: return ""
        case .boolValue(let b): return b ? "true" : "false"
        case .numberValue(let n):
            if n.rounded() == n, abs(n) < 1e15 { return String(Int64(n)) }
            return String(n)
        default:
            return (try? jsonString()) ?? ""
        }
    }
}

public extension Google_Protobuf_Struct {
    /// The first non-empty value among `keys`, as display text.
    func string(_ keys: String...) -> String {
        for key in keys {
            if let value = fields[key] {
                let text = value.displayString
                if !text.isEmpty { return text }
            }
        }
        return ""
    }

    /// Pretty-printed JSON (sorted keys) for "Raw details".
    var prettyJSON: String {
        guard let json = try? jsonString(), let data = json.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data),
              let pretty = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys]),
              let text = String(data: pretty, encoding: .utf8) else {
            return (try? jsonString()) ?? "{}"
        }
        return text
    }
}

// MARK: - Approvals

public extension Nexus_V1_Approval {
    /// The tool call behind a tool-permission approval (`payload = {tool, input, cwd}`).
    var toolAction: (tool: String, input: Google_Protobuf_Struct, cwd: String)? {
        guard hasPayload, case .stringValue(let tool)? = payload.fields["tool"]?.kind, !tool.isEmpty else { return nil }
        var input = Google_Protobuf_Struct()
        if case .structValue(let s)? = payload.fields["input"]?.kind { input = s }
        return (tool, input, payload.string("cwd"))
    }

    /// The readable body of the approval: the tool call, or the payload's fields.
    var readableDetail: ToolDetailKind? {
        if let action = toolAction {
            return ToolText.detail(action.tool, input: action.input, cwd: action.cwd)
        }
        guard hasPayload, !payload.fields.isEmpty else { return nil }
        return ToolText.detail(title, input: payload)
    }

    /// Older approvals say "X wants to use Bash"; give them the readable form.
    var readableTitle: String {
        let marker = " wants to use "
        guard let range = title.range(of: marker, options: .backwards), let action = toolAction else { return title }
        let rest = title[range.upperBound...]
        guard !rest.isEmpty, !rest.contains(" ") else { return title }
        let t = ToolText.title(action.tool, input: action.input).title
        if t.hasPrefix("Used ") { return title }
        return "\(title[..<range.lowerBound]) wants to: \(t.prefix(1).lowercased())\(t.dropFirst())"
    }
}
