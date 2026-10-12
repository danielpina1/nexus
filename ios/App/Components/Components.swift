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
    let label: String
    let color: Color

    init(state: ConnectionState) {
        label = state.label
        switch state {
        case .connected: color = .green
        case .demo: color = .purple
        case .connecting, .reconnecting: color = .orange
        case .disconnected: color = .red
        }
    }

    init(label: String, color: Color) {
        self.label = label
        self.color = color
    }

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(color).frame(width: 8, height: 8)
            Text(label).font(.caption2.weight(.medium))
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(.thinMaterial, in: Capsule())
    }
}

/// Calm banner shown on Home and Chat while the Hub can't be reached (docs/NETWORK.md §4):
/// away from home with the VPN off, or no network at all. Commands are queued meanwhile.
struct HubUnreachableBanner: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        if !store.isDemo && store.reachability.isUnavailable {
            content
        }
    }

    private var isOffline: Bool { store.reachability == .offline }

    private var content: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: isOffline ? "wifi.slash" : "house.and.flag")
                    .font(.title3)
                    .foregroundStyle(.secondary)
                    .frame(width: 28)
                VStack(alignment: .leading, spacing: 2) {
                    Text(isOffline ? "No internet connection" : "Can't reach your Nexus at home")
                        .font(.subheadline.weight(.semibold))
                    Text(isOffline
                         ? "Notifications still arrive when you're back online."
                         : "Turn on your home VPN (WireGuard / WiFiman Teleport)")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            HStack {
                if !store.outbox.isEmpty {
                    Label(
                        "\(store.outbox.count) queued — sent when you're back",
                        systemImage: "tray.and.arrow.up"
                    )
                    .font(.caption)
                    .foregroundStyle(.secondary)
                } else if let last = store.lastContact {
                    Text("Last contact \(Format.relative(last))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Button {
                    store.retryConnection()
                } label: {
                    if store.isProbing {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Retry")
                    }
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(store.isProbing)
            }
        }
        .padding(12)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14))
        .accessibilityElement(children: .contain)
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

/// Toolbar items shared by tab roots. (Environment is read inside the item views.)
struct CommonToolbar: ToolbarContent {
    var body: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            LiveConnectionPill()
        }
        ToolbarItem(placement: .topBarTrailing) {
            SettingsButton()
        }
    }
}

struct LiveConnectionPill: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        switch store.reachability {
        case .offline where !store.isDemo:
            ConnectionPill(label: "No network", color: .gray)
        case .unreachable where !store.isDemo:
            ConnectionPill(label: "Away · VPN off?", color: .orange)
        default:
            ConnectionPill(state: store.connection)
        }
    }
}

struct SettingsButton: View {
    @Environment(AppRouter.self) private var router

    var body: some View {
        Button {
            router.showSettings = true
        } label: {
            Image(systemName: "gearshape")
        }
        .accessibilityLabel("Settings")
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
