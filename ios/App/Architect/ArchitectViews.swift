import NexusCore
import SwiftUI

// Agent Architect (docs/ARCHITECT.md; web pages Architect.tsx and ArchitectDraft.tsx): describe
// what you need, the Architect drafts the notes, validates and tests them in a sandbox, and
// publishes only after you approve.

/// Work → Architect: start a draft, and the drafts list.
struct ArchitectSection: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    enum Filter: String, CaseIterable, Identifiable {
        case open = "Open"
        case published = "Published"
        case all = "All"
        var id: String { rawValue }
    }

    @State private var drafts: [Nexus_V1_AgentDraft]?
    @State private var errorMessage: String?
    @State private var filter: Filter = .open

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s6) {
            NXPageHeader("Agent Architect", subtitle: "Describe what you need in plain language. The Architect designs the agent, validates and tests it in a sandbox, and publishes only after you approve.")
            ArchitectStartCard { draft in
                drafts = [draft] + (drafts ?? []).filter { $0.id != draft.id }
                router.workPath.append(DraftRoute(id: draft.id))
            }
            draftList
        }
        .task { await load() }
    }

    private func load() async {
        do {
            drafts = try await store.drafts()
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private var draftList: some View {
        let sorted = (drafts ?? []).sorted { $0.updatedAt.date > $1.updatedAt.date }
        let shown = sorted.filter { draft in
            switch filter {
            case .all: return true
            case .open: return draft.state.isOpen
            case .published: return draft.state == .published
            }
        }
        let openCount = sorted.filter { $0.state.isOpen }.count
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Drafts", count: sorted.count)
            Text("Kept until you publish or discard them")
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
            Picker("Filter drafts", selection: $filter) {
                ForEach(Filter.allCases) { item in
                    Text(item == .open && openCount > 0 ? "Open (\(openCount))" : item.rawValue).tag(item)
                }
            }
            .pickerStyle(.segmented)
            if drafts == nil && errorMessage == nil {
                NXSkeletonRows(rows: 3)
            } else if let errorMessage, drafts == nil {
                NXErrorState(message: errorMessage, compact: true) { Task { await load() } }
                    .nxCard(padding: 0)
            } else if shown.isEmpty {
                NXEmptyState(
                    systemImage: "pencil.and.outline",
                    title: filter == .open ? "No open drafts" : (filter == .published ? "Nothing published yet" : "No drafts yet"),
                    message: filter == .open ? "Describe an agent above to start one." : nil,
                    compact: true
                )
                .nxCard(padding: 0)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(shown.enumerated()), id: \.element.id) { index, draft in
                        NavigationLink(value: DraftRoute(id: draft.id)) {
                            DraftRow(draft: draft)
                        }
                        .buttonStyle(NXPlainPressStyle())
                        if index < shown.count - 1 { NXDivider(inset: 60) }
                    }
                }
                .nxCard(padding: 0)
            }
        }
    }
}

extension Nexus_V1_DraftState {
    /// web draftStateTone.
    var tone: NXTone {
        switch self {
        case .drafting, .testing: return .info
        case .ready: return .accent
        case .invalid, .failed: return .danger
        case .pendingApproval: return .warn
        case .published: return .ok
        default: return .neutral
        }
    }
}

/// Draft state pill (spinner while drafting / testing).
struct DraftStateBadge: View {
    let state: Nexus_V1_DraftState

    var body: some View {
        NXBadge(state.label, tone: state.tone, isSpinning: state.isBusy)
            .accessibilityLabel("Status: \(state.label)")
    }
}

struct DraftRow: View {
    let draft: Nexus_V1_AgentDraft

    var body: some View {
        HStack(spacing: NXSpace.s3) {
            NXIconTile(systemImage: draft.kind.symbolName, tone: .neutral, size: 32)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(draft.displayTitle)
                        .nx(draft.notes.isEmpty ? NXType.body.weight(.medium) : NXType.mono.weight(.medium))
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                    if !draft.updateAgentID.isEmpty { NXBadge("update") }
                    if !draft.problems.isEmpty {
                        NXBadge("\(draft.problems.count)", systemImage: "exclamationmark.triangle", tone: .danger)
                            .accessibilityLabel("\(draft.problems.count) problems")
                    }
                }
                Text(draft.request)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            VStack(alignment: .trailing, spacing: 4) {
                DraftStateBadge(state: draft.state)
                Text("rev \(draft.revision)" + (draft.hasUpdatedAt ? " · " + Format.relative(draft.updatedAt.date) : ""))
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s2 + 2)
        .frame(minHeight: 60)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// "Create new" / "Improve an agent" form (web StartCard).
struct ArchitectStartCard: View {
    var onCreated: (Nexus_V1_AgentDraft) -> Void

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(ToastCenter.self) private var toasts

    enum Mode: String, CaseIterable, Identifiable {
        case new = "Create new"
        case improve = "Improve an agent"
        var id: String { rawValue }
    }

    private struct KindOption: Identifiable {
        let kind: Nexus_V1_DraftKind
        let label: String
        let hint: String
        let symbol: String
        var id: Int { kind.rawValue }
    }

    private static let kinds: [KindOption] = [
        KindOption(kind: .auto, label: "Let it decide", hint: "Agent, plus a workflow or team if needed", symbol: "sparkles"),
        KindOption(kind: .agent, label: "Agent", hint: "One agent note", symbol: "cpu"),
        KindOption(kind: .team, label: "Team", hint: "An orchestrator and members", symbol: "person.3"),
        KindOption(kind: .workflow, label: "Workflow", hint: "A scheduled or triggered workflow", symbol: "arrow.triangle.branch"),
    ]

    private static let examples = [
        "Tell me when the AirPods Pro drop below €150 at Worten or Fnac",
        "Every Friday, summarise what changed in my nexus repo this week",
        "A team that plans a weekend trip: one researches places, one checks the weather, one books",
        "Watch my electricity bill emails and alert me if a month is 20 % above average",
    ]

    @State private var mode: Mode = .new
    @State private var kind: Nexus_V1_DraftKind = .auto
    @State private var agentID = ""
    @State private var request = ""
    @State private var context = ""
    @State private var showContext = false
    @State private var isCreating = false

    private var improving: Bool { mode == .improve }

    private var canSubmit: Bool {
        request.trimmingCharacters(in: .whitespacesAndNewlines).count > 3 && !(improving && agentID.isEmpty) && !isCreating
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s4) {
            Picker("What to do", selection: $mode) {
                ForEach(Mode.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)

            if improving {
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    fieldLabel("Agent to improve", hint: "The Architect drafts a new version with a diff against the current note.")
                    Picker("Agent to improve", selection: $agentID) {
                        Text("Choose an agent…").tag("")
                        ForEach(store.agents, id: \.id) { agent in
                            Text("\(agent.name) (\(agent.id))").tag(agent.id)
                        }
                    }
                    .pickerStyle(.menu)
                    .tint(NXColor.fg)
                    .frame(maxWidth: .infinity, minHeight: NXSize.touchMin, alignment: .leading)
                    .padding(.horizontal, NXSpace.s1)
                    .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous).strokeBorder(NXColor.borderInput, lineWidth: 1))
                }
            }

            VStack(alignment: .leading, spacing: NXSpace.s2) {
                fieldLabel(improving ? "What should change?" : "Describe the agent you want", hint: nil)
                NXTextField(
                    placeholder: improving ? "e.g. It keeps substituting lactose milk; never do that, ask instead." : "e.g. “Tell me when the AirPods drop below €150”",
                    text: $request,
                    axis: .vertical
                )
                .lineLimit(3...8)
                if !improving && request.isEmpty {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: NXSpace.s2) {
                            ForEach(Self.examples, id: \.self) { example in
                                Button {
                                    request = example
                                } label: {
                                    Text(example)
                                        .nx(NXType.caption.weight(.regular))
                                        .foregroundStyle(NXColor.fg2)
                                        .lineLimit(1)
                                        .frame(maxWidth: 260)
                                        .padding(.horizontal, NXSpace.s3)
                                        .frame(minHeight: NXSize.touchMin)
                                        .background(NXColor.surface, in: Capsule())
                                        .overlay(Capsule().strokeBorder(NXColor.border, lineWidth: 1))
                                }
                                .buttonStyle(NXPlainPressStyle(cornerRadius: 22))
                                .accessibilityLabel("Example: \(example)")
                            }
                        }
                    }
                }
            }

            if !improving {
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    fieldLabel("What should it make?", hint: nil)
                    LazyVGrid(columns: [GridItem(.flexible(), spacing: NXSpace.s2), GridItem(.flexible(), spacing: NXSpace.s2)], spacing: NXSpace.s2) {
                        ForEach(Self.kinds) { option in
                            kindTile(option)
                        }
                    }
                }
            }

            VStack(alignment: .leading, spacing: NXSpace.s2) {
                Button {
                    showContext.toggle()
                } label: {
                    Label("Add context (optional)", systemImage: showContext ? "chevron.down" : "chevron.right")
                        .nxText(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                        .frame(minHeight: NXSize.touchMin)
                }
                .buttonStyle(NXPlainPressStyle())
                .accessibilityValue(showContext ? "expanded" : "collapsed")
                if showContext {
                    NXTextField(placeholder: "Anything the Architect should know: accounts, stores, constraints, how you'd like to be notified…", text: $context, axis: .vertical)
                        .lineLimit(2...6)
                }
            }

            NXDivider()
            HStack(spacing: 6) {
                ForEach(Array(["Draft", "Validate & auto-fix", "Test in a sandbox", "Publish after you approve"].enumerated()), id: \.offset) { index, step in
                    if index > 0 {
                        Image(systemName: "chevron.right")
                            .font(.system(size: 9, weight: .semibold))
                            .accessibilityHidden(true)
                    }
                    Text(step)
                }
            }
            .nxText(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .accessibilityElement(children: .combine)
            .accessibilityLabel("How it works: draft, validate and auto-fix, test in a sandbox, publish after you approve.")

            NXButton(improving ? "Draft an improvement" : "Draft it", systemImage: "sparkles", kind: .primary, isLoading: isCreating, fullWidth: true) {
                submit()
            }
            .disabled(!canSubmit)
        }
        .nxCard()
        .onAppear(perform: takePrefill)
        .onChange(of: router.architectImproveAgentID) { _, _ in takePrefill() }
    }

    private func takePrefill() {
        if let id = router.architectImproveAgentID {
            mode = .improve
            agentID = id
            router.architectImproveAgentID = nil
        }
    }

    private func fieldLabel(_ title: String, hint: String?) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .nx(NXType.body.weight(.medium))
                .foregroundStyle(NXColor.fg)
            if let hint {
                Text(hint)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
            }
        }
    }

    private func kindTile(_ option: KindOption) -> some View {
        let selected = kind == option.kind
        return Button {
            kind = option.kind
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Label(option.label, systemImage: option.symbol)
                    .nxText(NXType.footnote.weight(.semibold))
                    .foregroundStyle(selected ? NXColor.accent : NXColor.fg)
                Text(option.hint)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(NXSpace.s3)
            .frame(maxWidth: .infinity, minHeight: 64, alignment: .topLeading)
            .background(selected ? NXColor.accentSoft : NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                    .strokeBorder(selected ? NXColor.accent : NXColor.borderInput, lineWidth: 1)
            }
        }
        .buttonStyle(NXPlainPressStyle())
        .accessibilityAddTraits(selected ? [.isSelected, .isButton] : .isButton)
        .accessibilityHint(option.hint)
    }

    private func submit() {
        guard canSubmit else { return }
        isCreating = true
        let text = request.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            defer { isCreating = false }
            do {
                let draft = try await store.createDraft(
                    request: text,
                    kind: improving ? .agent : kind,
                    context: context.trimmingCharacters(in: .whitespacesAndNewlines),
                    updateAgentID: improving ? agentID : ""
                )
                request = ""
                context = ""
                onCreated(draft)
            } catch {
                toasts.error("Couldn't start the draft", error) { submit() }
            }
        }
    }
}
