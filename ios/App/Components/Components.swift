import NexusCore
import SwiftUI

/// Agent icon: SF Symbol or emoji from the agent note.
struct AgentIconView: View {
    let agent: Nexus_V1_Agent?
    var size: CGFloat = 28

    var body: some View {
        ZStack {
            Circle().fill(Color.accentColor.opacity(0.15))
            if let emoji = agent?.emoji {
                Text(emoji).font(.system(size: size * 0.55))
            } else {
                Image(systemName: agent?.symbolName ?? "sparkles")
                    .font(.system(size: size * 0.45, weight: .semibold))
                    .foregroundStyle(Color.accentColor)
            }
        }
        .frame(width: size, height: size)
    }
}

struct TaskStateBadge: View {
    let state: Nexus_V1_TaskState

    var body: some View {
        Label(state.label, systemImage: state.symbol)
            .font(.caption.weight(.medium))
            .foregroundStyle(color)
            .labelStyle(.titleAndIcon)
    }

    private var color: Color {
        switch state {
        case .running, .escalating, .assigned: return .blue
        case .waitingApproval, .waitingInput, .needsInput: return .orange
        case .succeeded: return .green
        case .failed: return .red
        default: return .secondary
        }
    }
}

struct ConnectionPill: View {
    let state: ConnectionState

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(color).frame(width: 8, height: 8)
            Text(state.label).font(.caption2.weight(.medium))
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(.thinMaterial, in: Capsule())
    }

    private var color: Color {
        switch state {
        case .connected: return .green
        case .demo: return .purple
        case .connecting, .reconnecting: return .orange
        case .disconnected: return .red
        }
    }
}

struct ErrorBanner: View {
    let message: String
    var onDismiss: (() -> Void)?

    var body: some View {
        HStack(alignment: .top) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
            Text(message).font(.footnote)
            Spacer()
            if let onDismiss {
                Button(action: onDismiss) { Image(systemName: "xmark") }
                    .buttonStyle(.plain)
            }
        }
        .padding(10)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
    }
}

/// Toolbar buttons shared by tab roots.
struct CommonToolbar: ToolbarContent {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            ConnectionPill(state: store.connection)
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                router.showSettings = true
            } label: {
                Image(systemName: "gearshape")
            }
            .accessibilityLabel("Settings")
        }
    }
}

enum Format {
    static func cost(cents: Int, currency: String = "EUR") -> String {
        (Decimal(cents) / 100).formatted(.currency(code: currency))
    }

    static func relative(_ date: Date?) -> String {
        guard let date else { return "" }
        return date.formatted(.relative(presentation: .named))
    }

    static func bytes(_ count: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: count, countStyle: .file)
    }
}

/// Renders Markdown with `AttributedString(markdown:)`, block by block so headings and lists
/// keep their line structure. Wiki-links `[[Note]]` are shown as plain bold text.
struct MarkdownText: View {
    let markdown: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                blockView(block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var blocks: [String] {
        var body = markdown
        // Strip YAML frontmatter.
        if body.hasPrefix("---\n"), let end = body.range(of: "\n---", range: body.index(body.startIndex, offsetBy: 4)..<body.endIndex) {
            body = String(body[end.upperBound...])
        }
        body = body.replacingOccurrences(of: "[[", with: "**").replacingOccurrences(of: "]]", with: "**")
        return body.components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    @ViewBuilder
    private func blockView(_ line: String) -> some View {
        if line.hasPrefix("### ") {
            inline(String(line.dropFirst(4))).font(.headline)
        } else if line.hasPrefix("## ") {
            inline(String(line.dropFirst(3))).font(.title3.bold())
        } else if line.hasPrefix("# ") {
            inline(String(line.dropFirst(2))).font(.title2.bold())
        } else if line.hasPrefix("- ") || line.hasPrefix("* ") {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text("•")
                inline(String(line.dropFirst(2)))
            }
        } else if line.hasPrefix("> ") {
            inline(String(line.dropFirst(2)))
                .foregroundStyle(.secondary)
                .padding(.leading, 8)
                .overlay(alignment: .leading) { Rectangle().fill(.tertiary).frame(width: 3) }
        } else {
            inline(line)
        }
    }

    private func inline(_ text: String) -> Text {
        let options = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        if let attributed = try? AttributedString(markdown: text, options: options) {
            return Text(attributed)
        }
        return Text(text)
    }
}
