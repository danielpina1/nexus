import ActivityKit
import AppIntents
import NexusCore
import SwiftUI
import WidgetKit

/// Run Live Activity — Lock Screen + Dynamic Island (IOS_LIVE.md §2.2), in the design system:
/// state badge words and tones as in the app, Montserrat numbers, Inter text.
struct RunLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RunActivityAttributes.self) { context in
            RunLockScreenView(attributes: context.attributes, state: context.state, isStale: context.isStale)
                .activityBackgroundTint(NXColor.surface)
                .activitySystemActionForegroundColor(NXColor.fg)
                .widgetURL(RunActivityStyle.url(attributes: context.attributes, state: context.state))
        } dynamicIsland: { context in
            let attributes = context.attributes
            let state = context.state
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    HStack(spacing: 6) {
                        Image(systemName: attributes.agentSymbol)
                            .foregroundStyle(state.phase.tone.color)
                            .accessibilityHidden(true)
                        Text(attributes.agentName)
                            .nx(NXType.title3)
                            .lineLimit(1)
                    }
                    .environment(\.colorScheme, .dark)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text(attributes.nodeName)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(1)
                        .environment(\.colorScheme, .dark)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(spacing: 6) {
                            NXBadge(state.phase.label, systemImage: state.phase.symbol, tone: state.phase.tone)
                            Text(state.title)
                                .nx(NXType.body.weight(.semibold))
                                .foregroundStyle(NXColor.fg)
                                .lineLimit(1)
                        }
                        if let detail = state.detail {
                            Text(detail)
                                .nx(NXType.caption.weight(.regular))
                                .foregroundStyle(NXColor.muted)
                                .lineLimit(1)
                        }
                        if let progress = state.progress, state.phase == .running {
                            NXMeter(value: progress, label: "Progress", tone: .info)
                        }
                        RunActionButtons(attributes: attributes, state: state, compact: true)
                    }
                    .environment(\.colorScheme, .dark)
                }
            } compactLeading: {
                HStack(spacing: 4) {
                    Image(systemName: attributes.agentSymbol)
                        .foregroundStyle(state.phase.tone.color)
                    if let step = state.stepText {
                        Text(step)
                            .nx(NXType.caption)
                            .monospacedDigit()
                    }
                }
                .environment(\.colorScheme, .dark)
                .accessibilityElement(children: .combine)
                .accessibilityLabel("\(attributes.agentName), \(state.phase.label)")
            } compactTrailing: {
                Group {
                    switch state.phase {
                    case .waitingApproval:
                        Label("approve?", systemImage: "hand.raised")
                            .labelStyle(.titleAndIcon)
                            .nxText(NXType.caption)
                            .foregroundStyle(NXColor.warn)
                    case .waitingInput:
                        Image(systemName: "questionmark.bubble").foregroundStyle(NXColor.warn)
                            .accessibilityLabel("Waiting input")
                    case .done:
                        Image(systemName: "checkmark.circle").foregroundStyle(NXColor.ok)
                            .accessibilityLabel("Succeeded")
                    case .failed:
                        Image(systemName: "xmark.circle").foregroundStyle(NXColor.danger)
                            .accessibilityLabel("Failed")
                    case .running:
                        Text(state.costText)
                            .nx(NXType.caption)
                            .monospacedDigit()
                    }
                }
                .environment(\.colorScheme, .dark)
            } minimal: {
                Image(systemName: state.isWaitingForUser ? "hand.raised" : attributes.agentSymbol)
                    .foregroundStyle(state.phase.tone.color)
                    .environment(\.colorScheme, .dark)
                    .accessibilityLabel(state.phase.label)
            }
            .widgetURL(RunActivityStyle.url(attributes: attributes, state: state))
            .keylineTint(state.phase.tone.color)
        }
    }
}

enum RunActivityStyle {
    /// Tapping the activity opens the approval sheet when one is pending, else the task.
    static func url(attributes: RunActivityAttributes, state: RunActivityAttributes.ContentState) -> URL {
        if state.phase == .waitingApproval, let id = state.approvalId {
            return DeepLink.approval(id)
        }
        return DeepLink.task(attributes.taskId)
    }
}

struct RunLockScreenView: View {
    let attributes: RunActivityAttributes
    let state: RunActivityAttributes.ContentState
    let isStale: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: NXSpace.s2) {
                NXIconTile(systemImage: attributes.agentSymbol, tone: state.phase.tone, size: 28)
                VStack(alignment: .leading, spacing: 0) {
                    Text(attributes.agentName)
                        .nx(NXType.title3)
                        .foregroundStyle(NXColor.fg)
                        .lineLimit(1)
                    Text(attributes.nodeName)
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(1)
                }
                Spacer(minLength: 0)
                NXBadge(state.phase.label, systemImage: state.phase.symbol, tone: state.phase.tone)
            }

            Text(state.title + (state.stepText.map { " (\($0))" } ?? ""))
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .lineLimit(2)

            if let progress = state.progress, state.phase == .running {
                NXMeter(value: progress, label: "Progress", tone: .info)
            }

            HStack(spacing: 6) {
                if let detail = state.detail {
                    Text(detail).lineLimit(1)
                    Text("·").accessibilityHidden(true)
                }
                Text(state.costText).monospacedDigit()
                if isStale {
                    Text("· last update a while ago")
                }
            }
            .nxText(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)

            RunActionButtons(attributes: attributes, state: state, compact: false)
        }
        .padding(NXSpace.s4)
    }
}

/// Buttons per phase. Normal approvals / stop / try harder run as background
/// `LiveActivityIntent`s; biometric approvals open the app (Face ID cannot run in background).
struct RunActionButtons: View {
    let attributes: RunActivityAttributes
    let state: RunActivityAttributes.ContentState
    let compact: Bool

    private var height: CGFloat { compact ? 36 : NXSize.touchMin }

    var body: some View {
        HStack(spacing: NXSpace.s2) {
            switch state.phase {
            case .running:
                Link(destination: DeepLink.task(attributes.taskId)) {
                    WidgetPill(title: "Open", systemImage: "arrow.up.forward.app", minHeight: height)
                }
                Button(intent: TryHarderIntent(taskID: attributes.taskId)) {
                    WidgetPill(title: "Try harder", systemImage: "arrow.up.circle", minHeight: height)
                }
                .buttonStyle(.plain)
                Button(intent: StopTaskIntent(taskID: attributes.taskId)) {
                    WidgetPill(title: "Stop", systemImage: "stop.circle", kind: .destructive, minHeight: height)
                }
                .buttonStyle(.plain)

            case .waitingApproval:
                if let approvalID = state.approvalId {
                    // Face ID approvals — and, while the Hub verifies every decision with the
                    // biometric approval key, all decisions — must open the in-app sheet.
                    if state.isBiometric || !ApprovalSignature.backgroundDecisionsSupported {
                        Link(destination: DeepLink.task(attributes.taskId)) {
                            WidgetPill(title: "Review", systemImage: "doc.text.magnifyingglass", minHeight: height)
                        }
                        Link(destination: DeepLink.approval(approvalID)) {
                            WidgetPill(title: state.isBiometric ? "Approve with Face ID" : "Approve", systemImage: state.isBiometric ? "faceid" : "checkmark", kind: .success, minHeight: height)
                        }
                    } else {
                        Button(intent: ApproveIntent(approvalID: approvalID, approve: false)) {
                            WidgetPill(title: "Deny", systemImage: "xmark", minHeight: height)
                        }
                        .buttonStyle(.plain)
                        Button(intent: ApproveIntent(approvalID: approvalID, approve: true)) {
                            WidgetPill(title: "Approve", systemImage: "checkmark", kind: .success, minHeight: height)
                        }
                        .buttonStyle(.plain)
                    }
                } else {
                    Link(destination: DeepLink.task(attributes.taskId)) {
                        WidgetPill(title: "Review", systemImage: "doc.text.magnifyingglass", minHeight: height)
                    }
                }

            case .waitingInput:
                if let choices = state.choices, !choices.isEmpty {
                    ForEach(Array(choices.prefix(compact ? 2 : 3)), id: \.self) { choice in
                        Button(intent: AnswerChoiceIntent(taskID: attributes.taskId, choice: choice)) {
                            WidgetPill(title: choice, minHeight: height)
                        }
                        .buttonStyle(.plain)
                    }
                } else {
                    Link(destination: DeepLink.task(attributes.taskId)) {
                        WidgetPill(title: "Reply", systemImage: "arrowshape.turn.up.left", kind: .primary, minHeight: height)
                    }
                }

            case .done:
                Link(destination: DeepLink.task(attributes.taskId)) {
                    WidgetPill(title: "Open result", systemImage: "checkmark.circle", kind: .primary, minHeight: height)
                }

            case .failed:
                Button(intent: TryHarderIntent(taskID: attributes.taskId)) {
                    WidgetPill(title: "Retry", systemImage: "arrow.clockwise", kind: .primary, minHeight: height)
                }
                .buttonStyle(.plain)
                Link(destination: DeepLink.task(attributes.taskId)) {
                    WidgetPill(title: "Open", systemImage: "arrow.up.forward.app", minHeight: height)
                }
            }
        }
    }
}

#Preview("Lock Screen", as: .content, using: RunActivityAttributes(
    taskId: "tsk_fix", agentId: "code-fixer", agentName: "Code fixer", agentSymbol: "hammer", nodeName: "mac-studio"
)) {
    RunLiveActivity()
} contentStates: {
    RunActivityAttributes.ContentState(phase: .running, title: "Running tests", detail: "+42 −13 · 3 files", progress: 0.8, step: 4, totalSteps: 5, costCents: 81)
    RunActivityAttributes.ContentState(phase: .waitingApproval, title: "Cart ready", detail: "22 items · €84.30", progress: 1, costCents: 31, approvalId: "apr_1", approvalLevel: "biometric")
}
