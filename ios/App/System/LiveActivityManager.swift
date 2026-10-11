import ActivityKit
import Foundation
import NexusCore

/// Starts Run activities ("Follow live"), keeps them updated from the event stream while the
/// app runs, and registers push-to-start + per-activity update tokens with the Hub
/// (`DeviceService.RegisterLiveActivityToken`) so the Hub can drive them remotely.
@MainActor
final class LiveActivityManager {
    private var observers: [Task<Void, Never>] = []
    private var observedActivityIDs: Set<String> = []
    private var started = false

    var areActivitiesEnabled: Bool {
        ActivityAuthorizationInfo().areActivitiesEnabled
    }

    /// Begin observing tokens. Safe to call more than once.
    func start() {
        guard !started else { return }
        started = true

        // Push-to-start tokens (iOS 17.2+): one per activity type.
        observers.append(Task {
            for await token in Activity<RunActivityAttributes>.pushToStartTokenUpdates {
                await Self.register(token, kind: .pushToStart, type: RunActivityAttributes.activityType)
            }
        })
        observers.append(Task {
            for await token in Activity<PulseActivityAttributes>.pushToStartTokenUpdates {
                await Self.register(token, kind: .pushToStart, type: PulseActivityAttributes.activityType)
            }
        })

        // Activities that already exist (e.g. started by push while we were not running)...
        for activity in Activity<RunActivityAttributes>.activities {
            observe(activity)
        }
        for activity in Activity<PulseActivityAttributes>.activities {
            observePulse(activity)
        }
        // ...and new ones, including push-started ones.
        observers.append(Task { [weak self] in
            for await activity in Activity<RunActivityAttributes>.activityUpdates {
                self?.observe(activity)
            }
        })
        observers.append(Task { [weak self] in
            for await activity in Activity<PulseActivityAttributes>.activityUpdates {
                self?.observePulse(activity)
            }
        })
    }

    private func observe(_ activity: Activity<RunActivityAttributes>) {
        guard !observedActivityIDs.contains(activity.id) else { return }
        observedActivityIDs.insert(activity.id)
        let activityID = activity.id
        let taskID = activity.attributes.taskId
        observers.append(Task {
            for await token in activity.pushTokenUpdates {
                await Self.register(token, kind: .update, type: RunActivityAttributes.activityType, activityID: activityID, taskID: taskID)
            }
        })
    }

    private func observePulse(_ activity: Activity<PulseActivityAttributes>) {
        guard !observedActivityIDs.contains(activity.id) else { return }
        observedActivityIDs.insert(activity.id)
        let activityID = activity.id
        observers.append(Task {
            for await token in activity.pushTokenUpdates {
                await Self.register(token, kind: .update, type: PulseActivityAttributes.activityType, activityID: activityID)
            }
        })
    }

    private static func register(_ token: Data, kind: Nexus_V1_LiveActivityTokenKind, type: String, activityID: String = "", taskID: String = "") async {
        guard let actions = NexusActions.shared() else { return }
        try? await actions.registerLiveActivityToken(
            token,
            kind: kind,
            activityType: type,
            activityID: activityID,
            taskID: taskID,
            environment: PushEnvironment.current
        )
    }

    // MARK: Run activities

    func activity(for taskID: String) -> Activity<RunActivityAttributes>? {
        Activity<RunActivityAttributes>.activities.first { $0.attributes.taskId == taskID }
    }

    /// "Follow live" from task detail.
    @discardableResult
    func follow(task: Nexus_V1_Task, agent: Nexus_V1_Agent?, nodeName: String) throws -> Activity<RunActivityAttributes> {
        if let existing = activity(for: task.id) { return existing }
        let attributes = RunActivityAttributes(
            taskId: task.id,
            agentId: task.agentID,
            agentName: agent?.name ?? "Nexus",
            agentSymbol: agent?.symbolName ?? "sparkles",
            nodeName: nodeName
        )
        let state = Self.contentState(for: task, approval: nil)
        let content = ActivityContent(state: state, staleDate: Date().addingTimeInterval(15 * 60), relevanceScore: 50)
        let activity = try Activity<RunActivityAttributes>.request(attributes: attributes, content: content, pushType: .token)
        observe(activity)
        return activity
    }

    /// Local update while the app is running (the Hub also pushes updates).
    func update(task: Nexus_V1_Task, approval: Nexus_V1_Approval?) {
        guard let activity = activity(for: task.id) else { return }
        let state = Self.contentState(for: task, approval: approval, previous: activity.content.state)
        let isFinal = task.state.isTerminal
        Task {
            let content = ActivityContent(
                state: state,
                staleDate: isFinal ? nil : Date().addingTimeInterval(15 * 60),
                relevanceScore: state.isWaitingForUser ? 100 : 50
            )
            if isFinal {
                await activity.end(content, dismissalPolicy: .after(Date().addingTimeInterval(3600)))
            } else {
                await activity.update(content)
            }
        }
    }

    func endAll() {
        for activity in Activity<RunActivityAttributes>.activities {
            Task { await activity.end(nil, dismissalPolicy: .immediate) }
        }
        for activity in Activity<PulseActivityAttributes>.activities {
            Task { await activity.end(nil, dismissalPolicy: .immediate) }
        }
    }

    static func contentState(
        for task: Nexus_V1_Task,
        approval: Nexus_V1_Approval?,
        previous: RunActivityAttributes.ContentState? = nil
    ) -> RunActivityAttributes.ContentState {
        let phase: RunActivityAttributes.ContentState.Phase
        switch task.state {
        case .waitingApproval: phase = .waitingApproval
        case .waitingInput, .needsInput: phase = .waitingInput
        case .succeeded: phase = .done
        case .failed, .cancelled: phase = .failed
        default: phase = .running
        }
        var state = previous ?? RunActivityAttributes.ContentState(phase: phase, title: task.displayTitle)
        state.phase = phase
        state.costCents = task.costCents
        switch phase {
        case .done:
            state.title = task.summary.isEmpty ? "Done" : task.summary
            state.progress = 1
        case .failed:
            state.title = task.state == .cancelled ? "Cancelled" : (task.summary.isEmpty ? "Failed" : task.summary)
        case .waitingApproval:
            if let approval {
                state.title = approval.title
                state.detail = approval.summary.isEmpty ? nil : approval.summary
                state.approvalId = approval.id
                state.approvalLevel = approval.level == .biometric ? "biometric" : "approve"
            }
        case .waitingInput:
            state.title = "Waiting for you"
        case .running:
            state.title = task.state.label
            state.approvalId = nil
            state.approvalLevel = nil
        }
        return state
    }
}
