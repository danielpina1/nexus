import NexusCore
import SwiftUI

// Readable tool calls and approval payloads (web components/ToolView.tsx): one renderer per tool
// family — Bash command + cwd, a line diff for edits, the URL for fetches, the query for
// searches, MCP inputs as a list, amount and merchant for spend. Raw JSON only behind
// "Raw details".

/// The readable body of a tool call or approval.
struct ToolDetailView: View {
    let detail: ToolDetailKind

    var body: some View {
        switch detail {
        case .spend(let amount, let merchant):
            HStack(spacing: NXSpace.s3) {
                Image(systemName: "creditcard")
                    .font(.system(size: 20, weight: .medium))
                    .foregroundStyle(NXColor.warn)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text(amount)
                        .nx(NXType.title2)
                        .monospacedDigit()
                        .foregroundStyle(NXColor.fg)
                    if !merchant.isEmpty {
                        Text("at \(merchant)")
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg2)
                    }
                }
                Spacer(minLength: 0)
            }
            .padding(NXSpace.s3)
            .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
            .accessibilityElement(children: .combine)
            .accessibilityLabel(merchant.isEmpty ? "Spend \(amount)" : "Spend \(amount) at \(merchant)")

        case .command(let command, let cwd, let description):
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                CodeBlock(text: "$ " + command)
                    .accessibilityLabel("Command: \(command)")
                if !description.isEmpty {
                    Text(description)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
                if !cwd.isEmpty {
                    (Text("in ") + Text(cwd).font(NXType.mono.font))
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                        .textSelection(.enabled)
                }
            }

        case .edit(let path, let lines):
            let stats = ToolText.diffStats(lines)
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                HStack(spacing: NXSpace.s2) {
                    Image(systemName: "pencil.line")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(NXColor.muted)
                        .accessibilityHidden(true)
                    Text(path)
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                        .truncationMode(.head)
                    Spacer(minLength: 0)
                    Text("+\(stats.added)").nx(NXType.caption).monospacedDigit().foregroundStyle(NXColor.ok)
                    Text("−\(stats.removed)").nx(NXType.caption).monospacedDigit().foregroundStyle(NXColor.danger)
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel("Edit \(path), \(stats.added) lines added, \(stats.removed) removed")
                DiffLinesView(lines: lines)
            }

        case .write(let path, let preview, let lineCount):
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                HStack(spacing: NXSpace.s2) {
                    Image(systemName: "square.and.pencil")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(NXColor.muted)
                        .accessibilityHidden(true)
                    Text(path)
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                        .truncationMode(.head)
                    Spacer(minLength: 0)
                    Text("\(lineCount) lines")
                        .nx(NXType.caption.weight(.regular))
                        .monospacedDigit()
                        .foregroundStyle(NXColor.muted)
                }
                CodeBlock(text: preview)
            }

        case .fetch(let url, let prompt):
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                if let link = URL(string: url), link.scheme == "https" || link.scheme == "http" {
                    Link(url, destination: link)
                        .font(NXType.footnote.font)
                        .foregroundStyle(NXColor.accent)
                        .lineLimit(3)
                } else {
                    Text(url).nx(NXType.footnote).foregroundStyle(NXColor.fg)
                }
                if !prompt.isEmpty {
                    Text(prompt)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.fg2)
                }
            }

        case .search(let query):
            Label("“\(query)”", systemImage: "magnifyingglass")
                .nxText(NXType.footnote)
                .foregroundStyle(NXColor.fg)

        case .questions(let questions):
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                ForEach(questions) { question in
                    VStack(alignment: .leading, spacing: NXSpace.s1) {
                        if !question.header.isEmpty {
                            NXBadge(question.header, tone: .info)
                        }
                        Text(question.question)
                            .nx(NXType.footnote.weight(.medium))
                            .foregroundStyle(NXColor.fg)
                        Text(question.options.map(\.label).joined(separator: " · "))
                            .nx(NXType.caption.weight(.regular))
                            .foregroundStyle(NXColor.fg2)
                    }
                }
            }

        case .inputs(let label, let pairs):
            VStack(alignment: .leading, spacing: NXSpace.s1) {
                Text(label)
                    .nx(NXType.mono)
                    .foregroundStyle(NXColor.fg2)
                if pairs.isEmpty {
                    Text("No inputs.")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                } else {
                    ForEach(pairs) { pair in
                        HStack(alignment: .firstTextBaseline, spacing: NXSpace.s2) {
                            Text(pair.key)
                                .nx(NXType.mono)
                                .foregroundStyle(NXColor.muted)
                                .frame(minWidth: 72, alignment: .leading)
                            Text(pair.value)
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.fg)
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
            }
        }
    }
}

/// Monospaced block on surface-2 (commands, file previews, raw JSON). Long text is clipped to
/// `maxLines` (the full text stays selectable / in "Raw details").
struct CodeBlock: View {
    let text: String
    var maxLines = 24

    var body: some View {
        Text(text)
            .nx(NXType.mono)
            .foregroundStyle(NXColor.fg)
            .lineLimit(maxLines)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(NXSpace.s2)
            .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
    }
}

/// A unified diff: removed lines on danger-soft, added lines on ok-soft (never colour alone: −/+).
struct DiffLinesView: View {
    let lines: [DiffLine]
    var maxLines = 80

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(lines.prefix(maxLines)) { line in
                HStack(alignment: .top, spacing: 6) {
                    Text(marker(line.kind))
                        .foregroundStyle(color(line.kind))
                        .accessibilityHidden(true)
                    Text(line.text.isEmpty ? " " : line.text)
                        .foregroundStyle(line.kind == .context ? NXColor.fg2 : NXColor.fg)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .font(NXType.mono.font)
                .padding(.horizontal, NXSpace.s2)
                .padding(.vertical, 1)
                .background(background(line.kind))
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken(line))
            }
            if lines.count > maxLines {
                Text("\(lines.count - maxLines) more lines")
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .padding(.horizontal, NXSpace.s2)
                    .padding(.top, NXSpace.s1)
            }
        }
        .padding(.vertical, NXSpace.s1)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
    }

    private func marker(_ kind: DiffLine.Kind) -> String {
        switch kind {
        case .added: return "+"
        case .removed: return "−"
        case .separator: return "@"
        case .context: return " "
        }
    }

    private func color(_ kind: DiffLine.Kind) -> Color {
        switch kind {
        case .added: return NXColor.ok
        case .removed: return NXColor.danger
        default: return NXColor.muted
        }
    }

    private func background(_ kind: DiffLine.Kind) -> Color {
        switch kind {
        case .added: return NXColor.okSoft
        case .removed: return NXColor.dangerSoft
        default: return Color.clear
        }
    }

    private func spoken(_ line: DiffLine) -> String {
        switch line.kind {
        case .added: return "Added: \(line.text)"
        case .removed: return "Removed: \(line.text)"
        case .separator: return "Next change"
        case .context: return line.text
        }
    }
}

/// "Raw details" disclosure for the JSON behind a card.
struct RawDetailsDisclosure: View {
    let text: String
    var label = "Raw details"
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s1) {
            Button {
                open.toggle()
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "chevron.right")
                        .font(.system(size: 10, weight: .semibold))
                        .rotationEffect(.degrees(open ? 90 : 0))
                        .accessibilityHidden(true)
                    Text(label)
                        .nx(NXType.caption.weight(.regular))
                }
                .foregroundStyle(NXColor.muted)
                .frame(minHeight: NXSize.touchMin)
                .contentShape(Rectangle())
            }
            .buttonStyle(NXPlainPressStyle())
            .accessibilityLabel(label)
            .accessibilityValue(open ? "Expanded" : "Collapsed")
            if open {
                CodeBlock(text: text, maxLines: 200)
            }
        }
    }
}

/// A compact tool chip (chat) that expands to the readable details; raw input / output only
/// behind "Raw details".
struct ToolChipView: View {
    let call: Nexus_V1_ToolCall
    let result: Nexus_V1_ToolResult?
    /// The run is still going (a call without a result shows a spinner).
    var active = true
    @State private var expanded = false

    private var failed: Bool { result?.error.isEmpty == false }
    private var running: Bool { result == nil && active }

    var body: some View {
        let title = ToolText.title(call.tool, input: call.input)
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            Button {
                expanded.toggle()
            } label: {
                HStack(spacing: 6) {
                    if running {
                        ProgressView().controlSize(.mini).accessibilityHidden(true)
                    } else {
                        Image(systemName: failed ? "xmark.circle" : title.symbol)
                            .font(.system(size: 12, weight: .medium))
                            .foregroundStyle(failed ? NXColor.danger : NXColor.muted)
                            .accessibilityHidden(true)
                    }
                    Text(title.title)
                        .nx(NXType.caption.weight(.medium))
                        .foregroundStyle(failed ? NXColor.danger : NXColor.fg2)
                        .lineLimit(expanded ? 3 : 1)
                    if let result, result.durationMs >= 100 {
                        Text("\(Double(result.durationMs) / 1000, specifier: "%.1f")s")
                            .nx(NXType.caption.weight(.regular))
                            .monospacedDigit()
                            .foregroundStyle(NXColor.muted)
                    }
                    Image(systemName: "chevron.right")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(NXColor.muted)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                        .accessibilityHidden(true)
                }
                .padding(.horizontal, 10)
                .frame(minHeight: 32)
                .background(NXColor.surface2, in: Capsule())
                .overlay(Capsule().strokeBorder(failed ? NXColor.danger.opacity(0.4) : NXColor.border, lineWidth: 1))
                .frame(minHeight: NXSize.touchMin)
                .contentShape(Rectangle())
            }
            .buttonStyle(NXPlainPressStyle(cornerRadius: 16))
            .accessibilityLabel("\(title.title), \(running ? "running" : (failed ? "failed" : "done"))")
            .accessibilityHint(expanded ? "Hides the details." : "Shows the details.")

            if expanded {
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    ToolDetailView(detail: ToolText.detail(call.tool, input: call.input))
                    if let result, !result.error.isEmpty {
                        Text(result.error)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.danger)
                            .textSelection(.enabled)
                    }
                    RawDetailsDisclosure(text: rawText)
                }
                .padding(NXSpace.s3)
                .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.border, lineWidth: 1))
            }
        }
    }

    private var rawText: String {
        var text = "tool: \(call.tool)\n\ninput:\n\(call.input.prettyJSON)"
        if let result {
            text += result.error.isEmpty ? "\n\noutput:\n\(result.output.prettyJSON)" : "\n\nerror:\n\(result.error)"
        }
        return text
    }
}
