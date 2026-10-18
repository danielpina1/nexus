import NexusCore
import SwiftUI

// App components built on the design-system kit (ios/DesignSystem/Kit). They read the store or
// the router, so they live in the app target rather than in the shared kit.

/// Agent tile in its categorical series colour (MASTER.md §8), or the agent's emoji.
struct AgentIconView: View {
    let agent: Nexus_V1_Agent?
    var agentID: String?
    var size: CGFloat = 32

    init(agent: Nexus_V1_Agent?, agentID: String? = nil, size: CGFloat = 32) {
        self.agent = agent
        self.agentID = agentID
        self.size = size
    }

    private var tint: Color {
        let key = agent?.id ?? agentID ?? ""
        guard !key.isEmpty else { return NXColor.accent }
        let index = key.unicodeScalars.reduce(0) { ($0 &* 31 &+ Int($1.value)) & 0x7FFF_FFFF } % NXColor.series.count
        return NXColor.series[index]
    }

    var body: some View {
        Group {
            if let emoji = agent?.emoji {
                Text(emoji)
                    .font(.system(size: size * 0.5))
                    .frame(width: size, height: size)
                    .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: size >= 40 ? NXRadius.lg : NXRadius.md, style: .continuous))
            } else {
                NXIconTile(systemImage: agent?.symbolName ?? "sparkles", tint: tint, background: tint.opacity(0.14), size: size)
            }
        }
        .accessibilityHidden(true)
    }
}

/// Team tile (icon from the team note).
struct TeamIconView: View {
    let symbol: String
    var size: CGFloat = 40

    var body: some View {
        NXIconTile(systemImage: symbol, tone: .accent, size: size)
    }
}

/// Task state pill (kit badge).
struct TaskStateBadge: View {
    let state: Nexus_V1_TaskState

    var body: some View {
        NXTaskStateBadge(state: state)
    }
}

/// Connection indicator (MASTER.md §9): dot plus label. Live pulses slowly.
struct ConnectionPill: View {
    let label: String
    let tone: NXTone
    var pulse = false

    init(state: ConnectionState) {
        label = state.label
        switch state {
        case .connected:
            tone = .ok
            pulse = true
        case .demo: tone = .accent
        case .connecting, .reconnecting: tone = .warn
        case .disconnected: tone = .danger
        }
    }

    init(label: String, tone: NXTone) {
        self.label = label
        self.tone = tone
    }

    var body: some View {
        HStack(spacing: 6) {
            NXDot(color: tone.color, size: 8, pulse: pulse)
            Text(label)
                .nx(NXType.caption)
                .foregroundStyle(NXColor.fg2)
                .lineLimit(1)
        }
        .padding(.horizontal, 10)
        .frame(minHeight: 28)
        .background(NXColor.surface, in: Capsule())
        .overlay(Capsule().strokeBorder(NXColor.border, lineWidth: 1))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Connection: \(label)")
    }
}

/// Calm banner shown while the Hub can't be reached (docs/NETWORK.md §4): away from home with
/// the VPN off, or no network at all. Commands are queued meanwhile.
struct HubUnreachableBanner: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        if !store.isDemo && store.reachability.isUnavailable {
            content
        }
    }

    private var isOffline: Bool { store.reachability == .offline }

    private var content: some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(alignment: .top, spacing: NXSpace.s3) {
                NXIconTile(systemImage: isOffline ? "wifi.slash" : "house.and.flag", tone: .warn, size: 32)
                VStack(alignment: .leading, spacing: 2) {
                    Text(isOffline ? "No internet connection" : "Can't reach your Nexus at home")
                        .nx(NXType.body.weight(.semibold))
                        .foregroundStyle(NXColor.fg)
                    Text(isOffline
                         ? "Notifications still arrive when you're back online."
                         : "Turn on your home VPN (WireGuard / WiFiman Teleport)")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
                Spacer(minLength: 0)
            }
            HStack(spacing: NXSpace.s3) {
                if !store.outbox.isEmpty {
                    Label(
                        "\(store.outbox.count) queued — sent when you're back",
                        systemImage: "tray.and.arrow.up"
                    )
                    .nxText(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                } else if let last = store.lastContact {
                    Text("Last contact \(Format.relative(last))")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
                Spacer()
                NXButton("Retry", systemImage: "arrow.clockwise", kind: .secondary, size: .small, isLoading: store.isProbing) {
                    store.retryConnection()
                }
            }
        }
        .nxCard(urgency: .warn)
        .accessibilityElement(children: .contain)
    }
}

/// Dismissable error message (kit inline error).
struct ErrorBanner: View {
    let message: String
    var onDismiss: (() -> Void)?

    var body: some View {
        NXInlineError(message, dismiss: onDismiss)
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
        Group {
            switch store.reachability {
            case .offline where !store.isDemo:
                ConnectionPill(label: "Offline", tone: .danger)
            case .unreachable where !store.isDemo:
                ConnectionPill(label: "Away · VPN off?", tone: .warn)
            default:
                ConnectionPill(state: store.connection)
            }
        }
        .contentShape(Rectangle())
        .onTapGesture {
            if !store.isDemo && !store.reachability.isConnected {
                store.retryConnection()
            }
        }
        .accessibilityAddTraits(store.reachability.isConnected || store.isDemo ? [] : .isButton)
        .accessibilityHint(store.reachability.isConnected || store.isDemo ? "" : "Retries the connection now.")
    }
}

struct SettingsButton: View {
    @Environment(AppRouter.self) private var router

    var body: some View {
        NXIconButton(systemImage: "gearshape", label: "Settings", tint: NXColor.fg2) {
            router.showSettings = true
        }
    }
}

enum Format {
    static func cost(cents: Int, currency: String = "EUR") -> String {
        NXFormat.money(cents: cents, currency: currency)
    }

    static func relative(_ date: Date?) -> String {
        NXFormat.relative(date)
    }

    static func bytes(_ count: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: count, countStyle: .file)
    }

    static func duration(_ seconds: TimeInterval) -> String {
        NXFormat.duration(seconds)
    }

    /// "Good morning" etc. (web Overview greeting).
    static func greeting(_ date: Date = Date()) -> String {
        let hour = Calendar.current.component(.hour, from: date)
        if hour < 5 { return "Good night" }
        if hour < 12 { return "Good morning" }
        if hour < 18 { return "Good afternoon" }
        return "Good evening"
    }
}

/// Renders Markdown with `AttributedString(markdown:)`, block by block so headings and lists
/// keep their line structure. Wiki-links `[[Note]]` are shown as plain bold text.
struct MarkdownText: View {
    let markdown: String

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
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
            inline(String(line.dropFirst(4))).nx(NXType.title3).foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
        } else if line.hasPrefix("## ") {
            inline(String(line.dropFirst(3))).nx(NXType.title2).foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
        } else if line.hasPrefix("# ") {
            inline(String(line.dropFirst(2))).nx(NXType.title1).foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
        } else if line.hasPrefix("- ") || line.hasPrefix("* ") {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text("•").foregroundStyle(NXColor.muted).accessibilityHidden(true)
                inline(String(line.dropFirst(2)))
            }
        } else if line.hasPrefix("> ") {
            inline(String(line.dropFirst(2)))
                .foregroundStyle(NXColor.fg2)
                .padding(.leading, NXSpace.s2)
                .overlay(alignment: .leading) { Rectangle().fill(NXColor.borderStrong).frame(width: 3) }
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

/// One task row (≥ 60 pt): agent tile, title, state + meta, cost.
struct TaskRow: View {
    let task: Nexus_V1_Task
    @Environment(NexusStore.self) private var store

    var body: some View {
        HStack(spacing: NXSpace.s3) {
            AgentIconView(agent: store.agent(task.agentID), agentID: task.agentID, size: 32)
            VStack(alignment: .leading, spacing: 4) {
                Text(task.displayTitle)
                    .nx(NXType.body.weight(.medium))
                    .foregroundStyle(NXColor.fg)
                    .lineLimit(2)
                HStack(spacing: 6) {
                    TaskStateBadge(state: task.state)
                    Text(meta)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if task.costCents > 0 {
                Text(Format.cost(cents: task.costCents))
                    .nx(NXType.caption)
                    .monospacedDigit()
                    .foregroundStyle(NXColor.fg2)
            }
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s3)
        .frame(minHeight: 60)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    private var meta: String {
        var parts: [String] = []
        if let agent = store.agent(task.agentID) { parts.append(agent.name) }
        if let date = task.updatedAt.optionalDate { parts.append(Format.relative(date)) }
        return parts.joined(separator: " · ")
    }
}

/// A list of task rows in one card with dividers; each row opens the task.
struct TaskList: View {
    let tasks: [Nexus_V1_Task]

    var body: some View {
        VStack(spacing: 0) {
            ForEach(Array(tasks.enumerated()), id: \.element.id) { index, task in
                NavigationLink(value: TaskRoute(id: task.id)) {
                    TaskRow(task: task)
                }
                .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                if index < tasks.count - 1 { NXDivider(inset: 60) }
            }
        }
        .nxCard(padding: 0)
    }
}
