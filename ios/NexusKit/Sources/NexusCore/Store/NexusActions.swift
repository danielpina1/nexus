import Foundation
import NexusAPI
import SwiftProtobuf

/// Stateless Hub actions shared by the app store, App Intents (Siri, widgets, Live Activity
/// buttons) and notification actions. All calls go to the Hub API only.
public struct NexusActions: Sendable {
    public let hub: HubClient

    public init(hub: HubClient) {
        self.hub = hub
    }

    /// Actions bound to the stored configuration, or nil when not paired / in demo mode.
    public static func shared() -> NexusActions? {
        if AppGroup.defaults.bool(forKey: AppGroup.Key.demoMode) { return nil }
        guard let hub = try? HubClient.shared() else { return nil }
        return NexusActions(hub: hub)
    }

    // MARK: Commands

    public func execute(
        _ input: String,
        attachments: [Nexus_V1_Attachment] = [],
        agentID: String? = nil,
        source: String = "ios-chat",
        idempotencyKey: String = .idempotencyKey()
    ) async throws -> Nexus_V1_ExecuteResponse {
        var request = Nexus_V1_ExecuteRequest()
        request.input = input
        request.attachments = attachments
        request.idempotencyKey = idempotencyKey
        var context = Nexus_V1_CommandContext()
        context.source = source
        request.context = context
        if let agentID, !agentID.isEmpty {
            var route = Nexus_V1_Route()
            route.kind = .task
            route.agentID = agentID
            route.confidence = 1
            route.reason = "chosen by user"
            request.route = route
        }
        return try await hub.call { headers in
            await hub.commands.execute(request: request, headers: headers)
        }
    }

    public func ask(_ question: String) async throws -> Nexus_V1_AskResponse {
        var request = Nexus_V1_AskRequest()
        request.question = question
        return try await hub.call { headers in
            await hub.vault.ask(request: request, headers: headers)
        }
    }

    public func capture(_ text: String, url: String? = nil, source: String = "ios") async throws -> Nexus_V1_NoteRef {
        var request = Nexus_V1_CaptureRequest()
        request.text = text
        request.url = url ?? ""
        request.source = source
        request.idempotencyKey = .idempotencyKey()
        return try await hub.call { headers in
            await hub.vault.capture(request: request, headers: headers)
        }.note
    }

    // MARK: Task steering

    public func sendMessage(taskID: String, text: String, questionID: String? = nil, choice: String? = nil, attachments: [Nexus_V1_Attachment] = []) async throws {
        var request = Nexus_V1_SendMessageRequest()
        request.taskID = taskID
        request.text = text
        request.questionID = questionID ?? ""
        request.choice = choice ?? ""
        request.attachments = attachments
        request.idempotencyKey = .idempotencyKey()
        _ = try await hub.call { headers in
            await hub.tasks.sendMessage(request: request, headers: headers)
        }
    }

    public func cancel(taskID: String) async throws {
        var request = Nexus_V1_CancelTaskRequest()
        request.id = taskID
        _ = try await hub.call { headers in
            await hub.tasks.cancelTask(request: request, headers: headers)
        }
    }

    @discardableResult
    public func escalate(taskID: String, hint: String = "") async throws -> Nexus_V1_Task {
        var request = Nexus_V1_EscalateTaskRequest()
        request.id = taskID
        request.hint = hint
        return try await hub.call { headers in
            await hub.tasks.escalateTask(request: request, headers: headers)
        }.task
    }

    /// Kill switch. TODO(owner root key): attach an owner-signed KIND_LOCK statement in
    /// `owner_lock_statement` so Nodes lock even if the Hub misbehaves (SECURITY.md §6).
    @discardableResult
    public func stopAll(pauseTriggers: Bool = true) async throws -> Int {
        var request = Nexus_V1_StopAllRequest()
        request.pauseTriggers = pauseTriggers
        let response = try await hub.call { headers in
            await hub.tasks.stopAll(request: request, headers: headers)
        }
        return Int(response.cancelled)
    }

    // MARK: Approvals

    public func pendingApprovals() async throws -> [Nexus_V1_Approval] {
        var request = Nexus_V1_ListApprovalsRequest()
        request.states = [.pending]
        return try await hub.call { headers in
            await hub.approvals.listApprovals(request: request, headers: headers)
        }.approvals
    }

    public func approval(id: String) async throws -> Nexus_V1_Approval? {
        try await pendingApprovals().first { $0.id == id }
    }

    public enum DecideError: Error, LocalizedError {
        case notFound
        case requiresBiometric

        public var errorDescription: String? {
            switch self {
            case .notFound: return "This approval is no longer pending."
            case .requiresBiometric: return "This approval needs Face ID in the app."
            }
        }
    }

    /// Signs and sends a decision.
    ///
    /// - Parameter allowBiometric: false from background contexts (Live Activity / notification
    ///   actions); a biometric approval then throws `requiresBiometric` and the caller should
    ///   deep-link to the in-app approval sheet instead.
    @discardableResult
    public func decide(
        approval: Nexus_V1_Approval,
        approve: Bool,
        comment: String = "",
        remember: Bool = false,
        allowBiometric: Bool,
        actionSHA256: Data? = nil
    ) async throws -> Nexus_V1_Approval {
        if approve && approval.level == .biometric && !allowBiometric {
            throw DecideError.requiresBiometric
        }
        guard let deviceID = hub.configuration.deviceID else { throw HubError.notPaired }
        let signed = try await ApprovalSigner().sign(
            approval: approval,
            approve: approve,
            deviceID: deviceID,
            actionSHA256: actionSHA256
        )
        var request = Nexus_V1_DecideRequest()
        request.decision = signed
        request.comment = comment
        request.remember = remember
        return try await hub.call { headers in
            await hub.approvals.decide(request: request, headers: headers)
        }.approval
    }

    /// Looks the approval up by id, then decides (used by notification and intent buttons).
    @discardableResult
    public func decide(approvalID: String, approve: Bool, allowBiometric: Bool) async throws -> Nexus_V1_Approval {
        guard let approval = try await self.approval(id: approvalID) else { throw DecideError.notFound }
        return try await decide(approval: approval, approve: approve, allowBiometric: allowBiometric)
    }

    // MARK: Devices / Live Activities

    public func registerPushToken(_ token: Data, environment: String) async throws {
        var request = Nexus_V1_RegisterPushTokenRequest()
        request.pushToken = token.map { String(format: "%02x", $0) }.joined()
        request.environment = environment
        _ = try await hub.call { headers in
            await hub.devices.registerPushToken(request: request, headers: headers)
        }
    }

    public func registerLiveActivityToken(
        _ token: Data,
        kind: Nexus_V1_LiveActivityTokenKind,
        activityType: String,
        activityID: String = "",
        taskID: String = "",
        environment: String
    ) async throws {
        var request = Nexus_V1_RegisterLiveActivityTokenRequest()
        request.kind = kind
        request.activityType = activityType
        request.activityID = activityID
        request.taskID = taskID
        request.token = token
        request.environment = environment
        _ = try await hub.call { headers in
            await hub.devices.registerLiveActivityToken(request: request, headers: headers)
        }
    }
}

/// APNs environment for token registration ("sandbox" for Debug builds, "production"
/// otherwise; overridable via App Group defaults for TestFlight builds).
public enum PushEnvironment {
    public static var current: String {
        if let override = AppGroup.defaults.string(forKey: AppGroup.Key.pushEnvironment) { return override }
        #if DEBUG
        return "sandbox"
        #else
        return "production"
        #endif
    }
}
