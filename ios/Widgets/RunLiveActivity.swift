import ActivityKit
import AppIntents
import NexusCore
import SwiftUI
import WidgetKit

/// Run Live Activity — Lock Screen + Dynamic Island (IOS_LIVE.md §2.2).
struct RunLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RunActivityAttributes.self) { context in
            RunLockScreenView(attributes: context.attributes, state: context.state, isStale: context.isStale)
                .activitySystemActionForegroundColor(.white)
                .widgetURL(RunActivityStyle.url(attributes: context.attributes, state: context.state))
        } dynamicIsland: { context in
            let attributes = context.attributes
            let state = context.state
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Label {
                        Text(attributes.agentName).font(.headline).lineLimit(1)
                    } icon: {
                        Image(systemName: attributes.agentSymbol)
                            .foregroundStyle(RunActivityStyle.tint(state.phase))
                    }
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text(attributes.nodeName)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(state.title).font(.subheadline.weight(.semibold)).lineLimit(1)
                        if let detail = state.detail {
                            Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                        if let progress = state.progress, state.phase == .running {
                            ProgressView(value: min(max(progress, 0), 1))
                                .tint(RunActivityStyle.tint(state.phase))
                        }
                        RunActionButtons(attributes: attributes, state: state, compact: true)
                    }
                }
            } compactLeading: {
                HStack(spacing: 4) {
                    Image(systemName: attributes.agentSymbol)
                        .foregroundStyle(RunActivityStyle.tint(state.phase))
                    if let step = state.stepText {
                        Text(step).font(.caption2.monospacedDigit())
                    }
                }
            } compactTrailing: {
                switch state.phase {
                case .waitingApproval:
                    Label("approve?", systemImage: "hourglass")
                        .labelStyle(.titleAndIcon)
                        .font(.caption2)
                        .foregroundStyle(.orange)
                case .waitingInput:
                    Image(systemName: "questionmark.bubble.fill").foregroundStyle(.orange)
                case .done:
                    Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                case .failed:
                    Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
                case .running:
                    Text(state.costText).font(.caption2.monospacedDigit())
                }
            } minimal: {
                Image(systemName: state.isWaitingForUser ? "hand.raised.fill" : attributes.agentSymbol)
                    .foregroundStyle(RunActivityStyle.tint(state.phase))
            }
            .widgetURL(RunActivityStyle.url(attributes: attributes, state: state))
            .keylineTint(RunActivityStyle.tint(state.phase))
        }
    }
}

enum RunActivityStyle {
    static func tint(_ phase: RunActivityAttributes.ContentState.Phase) -> Color {
        switch phase {
        case .running: return .blue
        case .waitingApproval, .waitingInput: return .orange
        case .done: return .green
        case .failed: return .red
        }
    }

    static func phaseLabel(_ phase: RunActivityAttributes.ContentState.Phase) -> String {
        switch phase {
        case .running: return "Running"
        case .waitingApproval: return "Waiting for approval"
        case .waitingInput: return "Waiting for you"
        case .done: return "Done"
        case .failed: return "Failed"
        }
    }

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
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: attributes.agentSymbol)
                    .font(.title3)
                    .foregroundStyle(RunActivityStyle.tint(state.phase))
                Text(attributes.agentName).font(.headline).lineLimit(1)
                Text("· \(attributes.nodeName)").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                Spacer()
                Text(RunActivityStyle.phaseLabel(state.phase))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(RunActivityStyle.tint(state.phase))
            }

            if let progress = state.progress, state.phase == .running {
                HStack {
                    ProgressView(value: min(max(progress, 0), 1))
                        .tint(RunActivityStyle.tint(state.phase))
                    Text(state.title + (state.stepText.map { " (\($0))" } ?? ""))
                        .font(.caption)
                        .lineLimit(1)
                }
            } else {
                Text(state.title).font(.subheadline.weight(.semibold)).lineLimit(2)
            }

            HStack(spacing: 6) {
                if let detail = state.detail {
                    Text(detail).lineLimit(1)
                    Text("·")
                }
                Text(state.costText)
                if isStale {
                    Text("· last update a while ago")
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)

            RunActionButtons(attributes: attributes, state: state, compact: false)
        }
        .padding()
    }
}

/// Buttons per phase. Normal approvals / stop / try harder run as background
/// `LiveActivityIntent`s; biometric approvals open the app (Face ID cannot run in background).
struct RunActionButtons: View {
    let attributes: RunActivityAttributes
    let state: RunActivityAttributes.ContentState
    let compact: Bool

    var body: some View {
        HStack(spacing: 8) {
            switch state.phase {
            case .running:
                Link(destination: DeepLink.task(attributes.taskId)) {
                    pill("Open", systemImage: "arrow.up.forward.app")
                }
                Button(intent: TryHarderIntent(taskID: attributes.taskId)) {
                    pill("Try harder", systemImage: "bolt.fill")
                }
                Button(intent: StopTaskIntent(taskID: attributes.taskId)) {
                    pill("Stop", systemImage: "stop.fill")
                }
                .tint(.red)

            case .waitingApproval:
                if let approvalID = state.approvalId {
                    // Face ID approvals — and, while the Hub verifies every decision with the
                    // biometric approval key, all decisions — must open the in-app sheet.
                    if state.isBiometric || !ApprovalSignature.backgroundDecisionsSupported {
                        Link(destination: DeepLink.task(attributes.taskId)) {
                            pill("Review", systemImage: "doc.text.magnifyingglass")
                        }
                        Link(destination: DeepLink.approval(approvalID)) {
                            pill(state.isBiometric ? "Approve with Face ID" : "Approve", systemImage: "faceid")
                        }
                    } else {
                        Button(intent: ApproveIntent(approvalID: approvalID, approve: false)) {
                            pill("Deny", systemImage: "xmark")
                        }
                        .tint(.red)
                        Button(intent: ApproveIntent(approvalID: approvalID, approve: true)) {
                            pill("Approve", systemImage: "checkmark")
                        }
                        .tint(.green)
                    }
                } else {
                    Link(destination: DeepLink.task(attributes.taskId)) {
                        pill("Review", systemImage: "doc.text.magnifyingglass")
                    }
                }

            case .waitingInput:
                if let choices = state.choices, !choices.isEmpty {
                    ForEach(Array(choices.prefix(compact ? 2 : 3)), id: \.self) { choice in
                        Button(intent: AnswerChoiceIntent(taskID: attributes.taskId, choice: choice)) {
                            pill(choice, systemImage: nil)
                        }
                    }
                } else {
                    Link(destination: DeepLink.task(attributes.taskId)) {
                        pill("Reply", systemImage: "arrowshape.turn.up.left.fill")
                    }
                }

            case .done:
                Link(destination: DeepLink.task(attributes.taskId)) {
                    pill("Open result", systemImage: "checkmark.circle")
                }

            case .failed:
                Button(intent: TryHarderIntent(taskID: attributes.taskId)) {
                    pill("Retry", systemImage: "arrow.clockwise")
                }
                Link(destination: DeepLink.task(attributes.taskId)) {
                    pill("Open", systemImage: "arrow.up.forward.app")
                }
            }
        }
        .buttonStyle(.bordered)
        .font(compact ? .caption2 : .caption)
    }

    @ViewBuilder
    private func pill(_ title: String, systemImage: String?) -> some View {
        if let systemImage {
            Label(title, systemImage: systemImage)
                .lineLimit(1)
                .frame(maxWidth: .infinity)
        } else {
            Text(title)
                .lineLimit(1)
                .frame(maxWidth: .infinity)
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
