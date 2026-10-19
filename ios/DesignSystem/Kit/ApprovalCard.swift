import SwiftUI

// The approval card (MASTER.md §9; web InboxCards.ApprovalCard). Pending: warn/40 border
// (biometric: danger/40), class badge, action class, expiry countdown (danger under 10 min),
// Approve (success) and Deny (secondary) inside the card they act on. Pure view: the caller
// wires the actions (Face ID signing lives in the app).

struct NXApprovalCardModel {
    enum State {
        case pending, approved, denied, expired
    }

    var title: String
    var summary: String
    var isBiometric: Bool
    var actionClass: String
    var state: State = .pending
    var nodeName: String?
    var createdAt: Date?
    var expiresAt: Date?
    /// "Grocery (Continente) · Weekly groceries"
    var context: String?
}

struct NXApprovalCard: View {
    let model: NXApprovalCardModel
    var compact = false
    /// Which decision is being sent (spinner on that button).
    var deciding: Bool?
    var error: String?
    var onApprove: () -> Void
    var onDeny: () -> Void
    /// Opens the full review (exact action, hash, comment).
    var onReview: (() -> Void)?
    var onRetry: (() -> Void)?
    /// The readable action (command, diff, amount…), shown under the summary.
    var detail: AnyView?

    init(
        model: NXApprovalCardModel,
        compact: Bool = false,
        deciding: Bool? = nil,
        error: String? = nil,
        onApprove: @escaping () -> Void,
        onDeny: @escaping () -> Void,
        onReview: (() -> Void)? = nil,
        onRetry: (() -> Void)? = nil,
        detail: AnyView? = nil
    ) {
        self.model = model
        self.compact = compact
        self.deciding = deciding
        self.error = error
        self.onApprove = onApprove
        self.onDeny = onDeny
        self.onReview = onReview
        self.onRetry = onRetry
        self.detail = detail
    }

    private var isPending: Bool { model.state == .pending }

    private var urgency: NXTone? {
        isPending ? (model.isBiometric ? .danger : .warn) : nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            meta
            Text(model.title)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .fixedSize(horizontal: false, vertical: true)
            if !model.summary.isEmpty {
                Text(model.summary)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                    .lineLimit(compact ? 2 : nil)
            }
            if let detail {
                detail
            }
            if !compact, let context = model.context, !context.isEmpty {
                Text(context)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
            if let error {
                NXInlineError(error, retry: onRetry)
            }
            actions
                .padding(.top, NXSpace.s1)
        }
        .nxCard(urgency: urgency)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(accessibilitySummary)
    }

    private var meta: some View {
        // Wraps on narrow screens / large text.
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) { metaItems }
            VStack(alignment: .leading, spacing: 4) { metaItems }
        }
    }

    @ViewBuilder
    private var metaItems: some View {
        HStack(spacing: 6) {
            if isPending {
                NXApprovalClassBadge(isBiometric: model.isBiometric)
            }
            NXBadge(model.actionClass)
        }
        if isPending, let expires = model.expiresAt {
            NXExpiryLabel(expiresAt: expires)
        }
        HStack(spacing: 4) {
            if let node = model.nodeName, !node.isEmpty {
                Text("on \(node)")
            }
            if let created = model.createdAt {
                Text("· \(NXFormat.relative(created))")
            }
        }
        .nxText(NXType.caption.weight(.regular))
        .foregroundStyle(NXColor.muted)
        .lineLimit(1)
    }

    @ViewBuilder
    private var actions: some View {
        if isPending {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: NXSpace.s2) { actionButtons }
                VStack(alignment: .leading, spacing: NXSpace.s2) { actionButtons }
            }
        } else {
            NXBadge(
                stateLabel,
                systemImage: model.state == .approved ? "checkmark.circle" : "xmark",
                tone: model.state == .approved ? .ok : .neutral
            )
        }
    }

    @ViewBuilder
    private var actionButtons: some View {
        NXApproveButton(isBiometric: model.isBiometric, isLoading: deciding == true, size: .small, action: onApprove)
            .disabled(deciding != nil)
        NXButton("Deny", systemImage: "xmark", kind: .secondary, size: .small, isLoading: deciding == false, action: onDeny)
            .disabled(deciding != nil)
        if let onReview {
            NXButton(compact ? "Review" : "Details", kind: .ghost, size: .small, action: onReview)
                .accessibilityHint("Shows the exact action, its hash and options.")
        }
    }

    private var stateLabel: String {
        switch model.state {
        case .approved: return "approved"
        case .denied: return "denied"
        case .expired: return "expired"
        case .pending: return "pending"
        }
    }

    private var accessibilitySummary: String {
        var parts: [String] = [model.isBiometric ? "Face ID approval" : "Approval", model.title]
        if isPending, let expires = model.expiresAt {
            parts.append("expires in " + NXFormat.spokenDuration(expires.timeIntervalSinceNow))
        }
        if !isPending { parts.append(stateLabel) }
        return parts.joined(separator: ", ")
    }
}

/// "expires in 42m 10s", live, danger under 10 minutes.
struct NXExpiryLabel: View {
    let expiresAt: Date

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            let left = expiresAt.timeIntervalSince(context.date)
            HStack(spacing: 3) {
                Image(systemName: "clock")
                    .font(.system(size: 10, weight: .semibold))
                    .accessibilityHidden(true)
                Text(left > 0 ? "expires in \(NXFormat.duration(left))" : "expired")
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
            }
            .foregroundStyle(left < 600 ? NXColor.danger : NXColor.muted)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(left > 0 ? "Expires in \(NXFormat.spokenDuration(left))" : "Expired")
        }
    }
}
