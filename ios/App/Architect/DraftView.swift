import NexusCore
import SwiftUI

/// One Architect draft (web ArchitectDraft): progress, problems and fixes, publish (with the
/// inline approval), generated notes (preview / Markdown / diff), sandbox tests, rationale and
/// risks, refine with feedback, request, discard. Polled while the Hub works on it.
struct DraftView: View {
    let draftID: String

    @Environment(NexusStore.self) private var store
    @Environment(ToastCenter.self) private var toasts
    @Environment(\.dismiss) private var dismiss

    @State private var draft: Nexus_V1_AgentDraft?
    @State private var errorMessage: String?
    @State private var busyAction: String?
    @State private var feedback = ""

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s6) {
                if let draft {
                    content(draft)
                } else if let errorMessage {
                    NXErrorState(message: errorMessage) { Task { await load() } }
                } else {
                    NXSkeleton(height: 28, width: 240)
                    NXSkeleton(height: 96, cornerRadius: NXRadius.lg)
                    NXSkeleton(height: 320, cornerRadius: NXRadius.lg)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(draft?.displayTitle ?? "Draft")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await load() }
        .task(id: draftID) {
            while !Task.isCancelled {
                await load()
                let state = draft?.state ?? .drafting
                let polling = state.isBusy || state == .pendingApproval
                try? await Task.sleep(for: .seconds(polling ? 3 : 30))
            }
        }
    }

    private func load() async {
        do {
            draft = try await store.draft(id: draftID)
            errorMessage = nil
        } catch {
            if draft == nil { errorMessage = error.localizedDescription }
        }
    }

    // MARK: Content

    @ViewBuilder
    private func content(_ d: Nexus_V1_AgentDraft) -> some View {
        let busy = d.state.isBusy
        let closed = d.state == .published || d.state == .discarded

        header(d, closed: closed)
        DraftProgress(draft: d)

        if !d.error.isEmpty {
            NXInlineError(d.error)
        }

        if !d.problems.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                Label(d.problems.count == 1 ? "1 problem blocks publishing" : "\(d.problems.count) problems block publishing", systemImage: "exclamationmark.shield")
                    .nxText(NXType.title3)
                    .foregroundStyle(NXColor.danger)
                    .accessibilityAddTraits(.isHeader)
                Text("Validation could not fix these automatically. Refine the draft with feedback to resolve them.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                ForEach(d.problems, id: \.self) { problem in
                    HStack(alignment: .top, spacing: NXSpace.s2) {
                        Image(systemName: "exclamationmark.triangle")
                            .foregroundStyle(NXColor.danger)
                            .accessibilityHidden(true)
                        Text(LocalizedStringKey(problem))
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg)
                    }
                    .padding(NXSpace.s3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                }
            }
            .padding(NXSpace.s4)
            .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous).strokeBorder(NXColor.danger.opacity(0.6), lineWidth: 2))
            .accessibilityElement(children: .contain)
        }

        if !d.fixes.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                Label("Validation fixed \(d.fixes.count) \(d.fixes.count == 1 ? "thing" : "things") automatically", systemImage: "wrench.and.screwdriver")
                    .nxText(NXType.body.weight(.semibold))
                    .foregroundStyle(NXColor.fg)
                ForEach(d.fixes, id: \.self) { fix in
                    Label {
                        Text(LocalizedStringKey(fix)).nx(NXType.footnote).foregroundStyle(NXColor.fg2)
                    } icon: {
                        Image(systemName: "checkmark.circle").foregroundStyle(NXColor.ok)
                    }
                }
            }
            .nxCard()
        }

        publishCard(d, busy: busy)

        DraftNotesView(draft: d)

        testsCard(d, busy: busy, closed: closed)

        if !d.rationale.isEmpty || !d.risks.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s3) {
                if !d.rationale.isEmpty {
                    Label("Why this design", systemImage: "lightbulb")
                        .nxText(NXType.body.weight(.semibold))
                        .foregroundStyle(NXColor.fg)
                    MarkdownText(markdown: d.rationale)
                        .nxText(NXType.body)
                        .foregroundStyle(NXColor.fg2)
                }
                if !d.risks.isEmpty {
                    Label("Risks", systemImage: "exclamationmark.triangle")
                        .nxText(NXType.body.weight(.semibold))
                        .foregroundStyle(NXColor.warn)
                    ForEach(d.risks, id: \.self) { risk in
                        HStack(alignment: .firstTextBaseline, spacing: 6) {
                            Text("•").accessibilityHidden(true)
                            Text(risk)
                        }
                        .nxText(NXType.footnote)
                        .foregroundStyle(NXColor.fg2)
                    }
                }
            }
            .nxCard()
        }

        refineCard(d, busy: busy, closed: closed)

        VStack(alignment: .leading, spacing: NXSpace.s2) {
            NXOverline("Request")
            Text(d.request)
                .nx(NXType.body)
                .foregroundStyle(NXColor.fg2)
                .textSelection(.enabled)
            if !d.context.isEmpty {
                Text(d.context)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            HStack(spacing: 6) {
                Text(d.id).nx(NXType.mono)
                if !d.via.isEmpty { Text("· \(d.via)") }
            }
            .nxText(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)
            if !d.taskID.isEmpty {
                NavigationLink(value: TaskRoute(id: d.taskID)) {
                    Text("Drafting task")
                }
                .buttonStyle(NXButtonStyle(kind: .ghost, size: .small))
            }
        }
        .nxCard()
    }

    private func header(_ d: Nexus_V1_AgentDraft, closed: Bool) -> some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(alignment: .top, spacing: NXSpace.s3) {
                NXIconTile(systemImage: d.kind.symbolName, tone: .accent, size: 44)
                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    Text(d.displayTitle)
                        .nx(NXType.title1)
                        .foregroundStyle(NXColor.fg)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                    FlowBadges {
                        DraftStateBadge(state: d.state)
                        NXBadge(d.kind.label)
                        Text("revision \(d.revision)" + (d.hasUpdatedAt ? " · updated " + Format.relative(d.updatedAt.date) : ""))
                            .nx(NXType.caption.weight(.regular))
                            .foregroundStyle(NXColor.muted)
                    }
                    if !d.updateAgentID.isEmpty {
                        NavigationLink(value: AgentRoute(id: d.updateAgentID)) {
                            Text("Improves \(d.updateAgentID)")
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.accent)
                                .frame(minHeight: NXSize.touchMin)
                        }
                        .buttonStyle(NXPlainPressStyle())
                    }
                }
            }
            if !closed {
                NXConfirmButton("Discard", confirmTitle: "Confirm discard", systemImage: "trash", kind: .ghost, size: .small, isLoading: busyAction == "discard") {
                    act("discard", fail: "Discard failed", ok: "Draft discarded") { try await store.discardDraft(id: d.id) }
                }
            }
        }
    }

    // MARK: Publish

    private func publishCard(_ d: Nexus_V1_AgentDraft, busy: Bool) -> some View {
        let reason: String = {
            if !d.problems.isEmpty {
                return "Resolve the \(d.problems.count == 1 ? "problem" : "\(d.problems.count) problems") above first (refine the draft)."
            }
            if busy { return "Wait until the current step has finished." }
            if d.state == .failed { return "Drafting failed; refine to try again." }
            if d.state == .ready || d.state == .pendingApproval { return "" }
            return "Not ready to publish."
        }()
        let approval = d.approvalID.isEmpty ? nil : store.approvals[d.approvalID]
        let agentNote = d.notes.first { $0.kind == "agent" }
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            Label("Publish", systemImage: "paperplane")
                .nxText(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
            if d.state == .published {
                Label("Published to the vault.", systemImage: "checkmark.circle")
                    .nxText(NXType.body)
                    .foregroundStyle(NXColor.ok)
                ForEach(d.notes, id: \.path) { note in
                    Group {
                        if note.kind == "agent" {
                            NavigationLink(value: AgentRoute(id: note.id)) { noteLink(note) }
                        } else if note.kind == "team" {
                            NavigationLink(value: TeamRoute(id: note.id)) { noteLink(note) }
                        } else {
                            NavigationLink(value: NoteRoute(id: "", path: note.path, title: note.id)) { noteLink(note) }
                        }
                    }
                    .buttonStyle(NXPlainPressStyle())
                }
            } else {
                Text("Publishing asks for your approval; the notes are written only when it is granted (all or nothing).")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
                if let approval {
                    ApprovalCardView(approval: approval, compact: true)
                } else if !d.approvalID.isEmpty {
                    Text("Approval \(d.approvalID) requested — it is in your Inbox.")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
                if d.state != .pendingApproval {
                    NXButton(agentNote?.exists == true ? "Publish update" : "Publish", systemImage: "paperplane.fill", kind: .primary, isLoading: busyAction == "publish", fullWidth: true) {
                        publish(d)
                    }
                    .disabled(!reason.isEmpty)
                    if !reason.isEmpty {
                        Text(reason)
                            .nx(NXType.footnote)
                            .foregroundStyle(d.problems.isEmpty ? NXColor.muted : NXColor.danger)
                    }
                }
            }
        }
        .nxCard(urgency: d.state == .pendingApproval ? .warn : nil)
    }

    private func noteLink(_ note: Nexus_V1_DraftNote) -> some View {
        Label(note.path, systemImage: note.kind == "team" ? "person.3" : (note.kind == "workflow" ? "arrow.triangle.branch" : "cpu"))
            .nxText(NXType.mono)
            .foregroundStyle(NXColor.accent)
            .frame(minHeight: NXSize.touchMin)
    }

    private func publish(_ d: Nexus_V1_AgentDraft) {
        busyAction = "publish"
        Task {
            defer { busyAction = nil }
            do {
                let result = try await store.publishDraft(id: d.id)
                draft = result.draft
                toasts.success("Approval requested", "Approve it here or in the Inbox to publish.")
            } catch {
                toasts.error("Publish failed", error) { publish(d) }
            }
        }
    }

    // MARK: Tests

    private func testsCard(_ d: Nexus_V1_AgentDraft, busy: Bool, closed: Bool) -> some View {
        let results = Dictionary(d.testResults.map { ($0.name, $0) }, uniquingKeysWith: { _, new in new })
        let names: [String] = d.testCases.isEmpty ? d.testResults.map(\.name) : d.testCases.map(\.name)
        let passed = d.testResults.filter(\.passed).count
        let blocked = d.testResults.reduce(0) { $0 + $1.blockedActions.count }
        let disabledReason: String = busy ? "Busy…" : (!d.problems.isEmpty ? "Fix the problems before testing." : (closed ? "This draft is closed." : (d.state == .pendingApproval ? "Waiting for approval." : (d.state == .failed ? "Drafting failed." : ""))))
        let subtitle: String = d.state == .testing
            ? "Running in the sandbox…"
            : (d.testResults.isEmpty
                ? "\(names.count) \(names.count == 1 ? "case" : "cases") · not run yet"
                : "\(passed)/\(d.testResults.count) passed" + (blocked > 0 ? " · \(blocked) blocked \(blocked == 1 ? "action" : "actions")" : ""))
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Test cases") {
                NXButton(d.testResults.isEmpty ? "Test run" : "Run again", systemImage: "flask", kind: .secondary, size: .small, isLoading: busyAction == "test" || d.state == .testing) {
                    act("test", fail: "Test run failed", ok: nil) { try await store.testDraft(id: d.id) }
                }
                .disabled(!disabledReason.isEmpty)
                .accessibilityHint(disabledReason.isEmpty ? "Runs every case in a sandbox (no external, spend or destructive actions)." : disabledReason)
            }
            Text(subtitle)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
            if names.isEmpty {
                NXEmptyState(systemImage: "flask", title: "No test cases", message: "Refine the draft and ask for test cases.", compact: true)
                    .nxCard(padding: 0)
            } else {
                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    ForEach(Array(names.enumerated()), id: \.offset) { index, name in
                        let testCase = d.testCases.first { $0.name == name }
                        DraftTestCaseView(name: name, input: testCase?.input ?? results[name]?.input ?? "", expect: testCase?.expect ?? [], result: results[name])
                        if index < names.count - 1 { NXDivider() }
                    }
                }
                .nxCard()
            }
        }
    }

    // MARK: Refine

    private func refineCard(_ d: Nexus_V1_AgentDraft, busy: Bool, closed: Bool) -> some View {
        let disabledReason = busy ? "Wait for the current step to finish." : (closed ? "This draft is closed." : (d.state == .pendingApproval ? "Waiting for approval; deny it to keep refining." : ""))
        return VStack(alignment: .leading, spacing: NXSpace.s3) {
            Label("Refine with feedback", systemImage: "text.bubble")
                .nxText(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
            NXTextField(
                placeholder: d.problems.isEmpty ? "e.g. Also check Amazon.es, and only notify me once per day." : "e.g. It doesn't need to change code — drop the anthrex tools.",
                text: $feedback,
                axis: .vertical
            )
            .lineLimit(3...8)
            .disabled(!disabledReason.isEmpty)
            HStack(spacing: NXSpace.s2) {
                Text(disabledReason.isEmpty ? "Makes revision \(d.revision + 1)." : disabledReason)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .frame(maxWidth: .infinity, alignment: .leading)
                NXButton("Refine", systemImage: "wrench", kind: .secondary, size: .small, isLoading: busyAction == "refine") {
                    let text = feedback.trimmingCharacters(in: .whitespacesAndNewlines)
                    act("refine", fail: "Refine failed", ok: "Refining — a new revision is on its way") {
                        let updated = try await store.refineDraft(id: d.id, feedback: text)
                        feedback = ""
                        return updated
                    }
                }
                .disabled(feedback.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !disabledReason.isEmpty)
            }
            if !d.feedback.isEmpty {
                DisclosureGroup("Earlier feedback (\(d.feedback.count))") {
                    VStack(alignment: .leading, spacing: NXSpace.s1) {
                        ForEach(Array(d.feedback.enumerated()), id: \.offset) { index, item in
                            Text("\(index + 1). \(item)")
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.fg2)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, NXSpace.s2)
                }
                .nxText(NXType.footnote)
                .foregroundStyle(NXColor.muted)
                .tint(NXColor.muted)
            }
        }
        .nxCard()
    }

    // MARK: Actions

    private func act(_ key: String, fail: String, ok: String?, _ action: @escaping () async throws -> Nexus_V1_AgentDraft) {
        busyAction = key
        Task {
            defer { busyAction = nil }
            do {
                draft = try await action()
                if let ok { toasts.success(ok) }
            } catch {
                toasts.error(fail, error) { act(key, fail: fail, ok: ok, action) }
            }
        }
    }
}

/// Draft → Validate → Test → Publish steps.
struct DraftProgress: View {
    let draft: Nexus_V1_AgentDraft

    private var current: Int {
        switch draft.state {
        case .drafting, .failed: return 0
        case .invalid: return 1
        case .ready: return draft.testResults.isEmpty ? 2 : 3
        case .testing: return 2
        case .pendingApproval: return 3
        case .published: return 4
        default: return 0
        }
    }

    var body: some View {
        let steps = ["Draft", "Validate", "Test", "Publish"]
        HStack(spacing: NXSpace.s2) {
            ForEach(Array(steps.enumerated()), id: \.offset) { index, step in
                let done = index < current
                let now = index == current
                VStack(alignment: .leading, spacing: 6) {
                    Capsule()
                        .fill(done ? NXColor.ok : (now ? NXColor.accent : NXColor.surface3))
                        .frame(height: 4)
                    HStack(spacing: 4) {
                        if done {
                            Image(systemName: "checkmark")
                                .font(.system(size: 9, weight: .bold))
                                .accessibilityHidden(true)
                        }
                        Text(step)
                    }
                    .nxText(NXType.caption)
                    .foregroundStyle(done ? NXColor.ok : (now ? NXColor.fg : NXColor.muted))
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(step): \(done ? "done" : (now ? "current step" : "to do"))")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Progress")
    }
}

/// Generated notes with a picker among them and Preview / Markdown / Diff.
struct DraftNotesView: View {
    let draft: Nexus_V1_AgentDraft

    enum ViewMode: String, CaseIterable, Identifiable {
        case preview = "Preview"
        case markdown = "Markdown"
        case diff = "Diff"
        var id: String { rawValue }
    }

    @State private var index = 0
    @State private var mode: ViewMode = .preview

    var body: some View {
        let notes = draft.notes
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            NXSectionHeader("Generated notes", count: notes.count)
            Text("\(notes.count) \(notes.count == 1 ? "note" : "notes") written to the vault on publish")
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
            if notes.isEmpty {
                if draft.state == .drafting {
                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        NXSkeleton(height: 16, width: 200)
                        NXSkeleton(height: 180, cornerRadius: NXRadius.md)
                    }
                    .nxCard()
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("Drafting")
                } else {
                    NXEmptyState(systemImage: "doc.text", title: "No notes yet", message: "They appear when the draft is ready.", compact: true)
                        .nxCard(padding: 0)
                }
            } else {
                let note = notes[min(index, notes.count - 1)]
                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    if notes.count > 1 {
                        Picker("Note", selection: $index) {
                            ForEach(Array(notes.enumerated()), id: \.offset) { offset, item in
                                Text("\(item.kind): \(item.id)").tag(offset)
                            }
                        }
                        .pickerStyle(.menu)
                        .tint(NXColor.accent)
                    }
                    HStack(spacing: 6) {
                        Text(note.path)
                            .nx(NXType.mono)
                            .foregroundStyle(NXColor.fg2)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        NXBadge(note.exists ? "update" : "new", tone: note.exists ? .warn : .ok)
                    }
                    Picker("View", selection: $mode) {
                        ForEach(ViewMode.allCases.filter { $0 != .diff || !note.diff.isEmpty }) { item in
                            Text(item.rawValue).tag(item)
                        }
                    }
                    .pickerStyle(.segmented)
                    Group {
                        switch mode {
                        case .preview:
                            MarkdownText(markdown: note.markdown)
                                .nxText(NXType.body)
                                .foregroundStyle(NXColor.fg)
                        case .markdown:
                            ScrollView(.horizontal) {
                                Text(note.markdown)
                                    .nx(NXType.mono)
                                    .foregroundStyle(NXColor.fg2)
                                    .textSelection(.enabled)
                                    .padding(NXSpace.s3)
                            }
                            .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                        case .diff:
                            DiffText(diff: note.diff)
                        }
                    }
                }
                .nxCard()
                .onChange(of: index) { _, _ in
                    mode = notes[min(index, notes.count - 1)].diff.isEmpty ? .preview : .diff
                }
            }
        }
        .onAppear {
            if let first = draft.notes.first, !first.diff.isEmpty { mode = .diff }
        }
    }
}

/// Unified diff with added / removed lines tinted (and +/− kept for VoiceOver and colour blindness).
struct DiffText: View {
    let diff: String

    var body: some View {
        ScrollView(.horizontal) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(diff.components(separatedBy: "\n").enumerated()), id: \.offset) { _, line in
                    Text(line.isEmpty ? " " : line)
                        .nx(NXType.mono)
                        .foregroundStyle(color(line))
                        .padding(.horizontal, NXSpace.s2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(background(line))
                }
            }
            .padding(.vertical, NXSpace.s2)
        }
        .background(NXColor.surface2, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .textSelection(.enabled)
    }

    private func color(_ line: String) -> Color {
        if line.hasPrefix("+") && !line.hasPrefix("+++") { return NXColor.ok }
        if line.hasPrefix("-") && !line.hasPrefix("---") { return NXColor.danger }
        if line.hasPrefix("@@") { return NXColor.info }
        return NXColor.fg2
    }

    private func background(_ line: String) -> Color {
        if line.hasPrefix("+") && !line.hasPrefix("+++") { return NXColor.okSoft }
        if line.hasPrefix("-") && !line.hasPrefix("---") { return NXColor.dangerSoft }
        return Color.clear
    }
}

/// One test case: input, expectations and the sandbox result.
struct DraftTestCaseView: View {
    let name: String
    let input: String
    let expect: [String]
    let result: Nexus_V1_DraftTestResult?

    @State private var showOutput = false

    private static let modes: [String: (label: String, hint: String, tone: NXTone)] = [
        "sandbox_task": ("sandbox", "An ephemeral sandboxed task: read/compute tools only, everything else needs Face ID.", .ok),
        "native_loop": ("native loop", "In-process model loop: read/compute tools really ran; anything else was refused and listed.", .info),
        "simulated": ("simulated", "No sandbox or model available: the agent-architect role-played the draft. Treat results as indicative.", .neutral),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: 6) {
                Text(name)
                    .nx(NXType.body.weight(.semibold))
                    .foregroundStyle(NXColor.fg)
                Spacer()
                if let result {
                    NXBadge(result.passed ? "passed" : "failed", systemImage: result.passed ? "checkmark.circle" : "xmark.circle", tone: result.passed ? .ok : .danger)
                } else {
                    NXBadge("not run")
                }
            }
            if !input.isEmpty {
                Text(input)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
            }
            if !expect.isEmpty {
                FlowBadges {
                    ForEach(expect, id: \.self) { item in
                        NXBadge(item)
                    }
                }
            }
            if let result {
                if let mode = Self.modes[result.mode] {
                    NXBadge("mode: \(mode.label)", tone: mode.tone)
                        .accessibilityHint(mode.hint)
                }
                if !result.error.isEmpty {
                    NXInlineError(result.error)
                }
                ForEach(Array(result.checks.enumerated()), id: \.offset) { _, check in
                    Text(check)
                        .nx(NXType.mono)
                        .foregroundStyle(check.hasPrefix("✓") ? NXColor.ok : (check.hasPrefix("✗") ? NXColor.danger : NXColor.muted))
                }
                if !result.blockedActions.isEmpty {
                    Label("Blocked by the sandbox: " + result.blockedActions.joined(separator: ", "), systemImage: "hand.raised.slash")
                        .nxText(NXType.footnote.weight(.medium))
                        .foregroundStyle(NXColor.warn)
                }
                if !result.toolCalls.isEmpty {
                    Text("Tool calls: " + result.toolCalls.joined(separator: ", "))
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
                if !result.taskID.isEmpty {
                    NavigationLink(value: TaskRoute(id: result.taskID)) {
                        Text("Sandbox task")
                    }
                    .buttonStyle(NXButtonStyle(kind: .ghost, size: .small))
                }
                if !result.output.isEmpty {
                    DisclosureGroup("Output", isExpanded: $showOutput) {
                        Text(result.output)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg2)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.top, NXSpace.s1)
                    }
                    .nxText(NXType.footnote)
                    .tint(NXColor.muted)
                    .frame(minHeight: NXSize.touchMin)
                }
            }
        }
        .accessibilityElement(children: .contain)
    }
}
