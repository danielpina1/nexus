import NexusCore
import SwiftUI

/// Inbox (web Inbox): approvals, questions and results that need you, ranked by urgency, with
/// filters and the decided history.
struct InboxView: View {
    @Environment(NexusStore.self) private var store

    enum Filter: String, CaseIterable, Identifiable {
        case all, approvals, questions, notices
        var id: String { rawValue }

        var title: String {
            switch self {
            case .all: return "All"
            case .approvals: return "Approvals"
            case .questions: return "Questions"
            case .notices: return "Notices"
            }
        }
    }

    @State private var filter: Filter = .all

    private func items(_ filter: Filter) -> [InboxItem] {
        store.inbox.filter { item in
            switch (filter, item) {
            case (.all, _): return true
            case (.approvals, .approval): return true
            case (.questions, .question): return true
            case (.notices, .notice): return true
            default: return false
            }
        }
    }

    var body: some View {
        let visible = items(filter)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: NXSpace.s3, pinnedViews: [.sectionHeaders]) {
                Section {
                    HubUnreachableBanner()
                        .padding(.top, NXSpace.s2)
                    if visible.isEmpty {
                        NXEmptyState(
                            systemImage: "checkmark.circle",
                            title: "All clear",
                            message: "Nothing needs you right now. New approvals and questions appear here instantly."
                        )
                    } else {
                        ForEach(visible) { item in
                            InboxCard(item: item)
                        }
                    }
                } header: {
                    NXSectionTabs(Filter.allCases, selection: $filter, title: { $0.title }, badge: { items($0).count })
                        .padding(.horizontal, -NXSpace.s4)
                        .nxGlass(showsBorder: false)
                }
            }
            .padding(.horizontal, NXSpace.s4)
            .padding(.bottom, NXSpace.s8)
        }
        .nxScreenBackground()
        .navigationTitle("Inbox")
        .toolbar { CommonToolbar() }
        .refreshable { await store.refresh() }
    }
}

/// One inbox card, the same on Home, in the Inbox and in team runs (CLIENTS.md §1 "Cards").
struct InboxCard: View {
    let item: InboxItem
    var compact = false

    var body: some View {
        switch item {
        case .approval(let approval):
            ApprovalCardView(approval: approval, compact: compact)
        case .question(let question):
            QuestionCardView(question: question, compact: compact)
        case .notice(let notice):
            NoticeCardView(notice: notice, compact: compact)
        }
    }
}

/// The kit approval card wired to the store, with the readable action (command, diff, URL,
/// amount…; raw JSON only behind "Raw details"). Approve signs with the Face ID–bound key;
/// in the Inbox biometric approvals open the full review first (what you see is what you
/// sign). `inline` (chat) approves biometric ones in place: the action is shown above the
/// button and the signer re-verifies the action hash before Face ID.
struct ApprovalCardView: View {
    let approval: Nexus_V1_Approval
    var compact = false
    var inline = false

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(ToastCenter.self) private var toasts
    @State private var deciding: Bool?
    @State private var errorMessage: String?

    var body: some View {
        let agent = store.agent(approval.agentID)
        let task = store.tasks[approval.taskID]
        NXApprovalCard(
            model: NXApprovalCardModel(
                title: approval.readableTitle,
                summary: approval.summary,
                isBiometric: approval.isBiometric,
                actionClass: approval.actionClass.label.lowercased(),
                state: modelState,
                nodeName: store.node(approval.nodeID)?.name ?? (approval.nodeID.isEmpty ? nil : approval.nodeID),
                createdAt: approval.createdAt.optionalDate,
                expiresAt: approval.expiresDate,
                context: [agent?.name, task?.displayTitle].compactMap { $0 }.joined(separator: " · ")
            ),
            compact: compact,
            deciding: deciding,
            error: errorMessage,
            onApprove: {
                if approval.isBiometric && !inline {
                    router.openApproval(approval.id)
                } else {
                    decide(approve: true)
                }
            },
            onDeny: { decide(approve: false) },
            onReview: { router.openApproval(approval.id) },
            onRetry: { errorMessage = nil },
            detail: detail
        )
    }

    private var detail: AnyView? {
        guard let readable = approval.readableDetail else { return nil }
        return AnyView(
            VStack(alignment: .leading, spacing: 0) {
                ToolDetailView(detail: readable)
                RawDetailsDisclosure(text: approval.payloadPreview)
            }
        )
    }

    private var modelState: NXApprovalCardModel.State {
        switch approval.state {
        case .approved: return .approved
        case .denied: return .denied
        case .expired: return .expired
        default: return .pending
        }
    }

    private func decide(approve: Bool) {
        deciding = approve
        errorMessage = nil
        Task {
            defer { deciding = nil }
            do {
                let delivery = try await store.decide(approval, approve: approve)
                if delivery.isQueued {
                    toasts.info(approve ? "Approved — waiting to send" : "Denied — waiting to send", "Sent when your Nexus is reachable.")
                }
            } catch {
                errorMessage = error.localizedDescription
                toasts.error("Couldn't send the decision", error) { decide(approve: approve) }
            }
        }
    }
}

/// Results, alerts, suggestions and facts to confirm.
struct NoticeCardView: View {
    let notice: NoticeItem
    var compact = false

    @Environment(NexusStore.self) private var store

    var body: some View {
        let kind = notice.notice.kind
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: 6) {
                NXBadge(Self.label(for: kind), systemImage: Self.symbol(for: kind), tone: Self.tone(for: kind))
                Text(Format.relative(notice.at))
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
            }
            Text(notice.notice.title)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
            if !notice.notice.body.isEmpty {
                Text(notice.notice.body)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                    .lineLimit(compact ? 2 : 6)
            }
            HStack(spacing: NXSpace.s2) {
                if !notice.taskID.isEmpty {
                    NavigationLink(value: TaskRoute(id: notice.taskID)) {
                        Text("Open task")
                    }
                    .buttonStyle(NXButtonStyle(kind: .secondary, size: .small))
                }
                NXButton("Dismiss", kind: .ghost, size: .small) {
                    store.dismissNotice(notice.id)
                }
            }
        }
        .nxCard(urgency: kind == .alert ? .danger : nil)
        .accessibilityElement(children: .contain)
    }

    static func label(for kind: Nexus_V1_NoticeKind) -> String {
        switch kind {
        case .result: return "Result"
        case .alert: return "Alert"
        case .suggestion: return "Suggestion"
        case .factReview: return "Fact to confirm"
        case .brief: return "Brief"
        default: return "Notice"
        }
    }

    static func symbol(for kind: Nexus_V1_NoticeKind) -> String {
        switch kind {
        case .result: return "checkmark.seal"
        case .alert: return "exclamationmark.triangle"
        case .suggestion: return "lightbulb"
        case .factReview: return "brain.head.profile"
        case .brief: return "sun.max"
        default: return "bell"
        }
    }

    static func tone(for kind: Nexus_V1_NoticeKind) -> NXTone {
        switch kind {
        case .result: return .ok
        case .alert: return .danger
        case .suggestion: return .accent
        case .factReview: return .info
        default: return .neutral
        }
    }
}

/// Text field with the control boundary (`border-input`), 44 pt, 16 pt text (MASTER.md §9).
struct NXTextField: View {
    let placeholder: String
    @Binding var text: String
    var axis: Axis = .horizontal
    var mono = false

    init(placeholder: String, text: Binding<String>, axis: Axis = .horizontal, mono: Bool = false) {
        self.placeholder = placeholder
        self._text = text
        self.axis = axis
        self.mono = mono
    }

    @FocusState private var focused: Bool

    var body: some View {
        TextField(placeholder, text: $text, axis: axis)
            .nxText(mono ? NXType.mono : NXType.bodyLg)
            .foregroundStyle(NXColor.fg)
            .focused($focused)
            .padding(.horizontal, NXSpace.s3)
            .padding(.vertical, 10)
            .frame(minHeight: NXSize.touchMin)
            .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                    .strokeBorder(focused ? NXColor.ring : NXColor.borderInput, lineWidth: focused ? 2 : 1)
            }
            .accessibilityLabel(placeholder)
    }
}
