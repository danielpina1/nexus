import Foundation
import NexusCore
import UIKit
import UserNotifications

/// Notification categories and actions (IOS_LIVE.md §3.1).
/// Interruption levels are set by the Hub in `aps` and enforced by the service extension.
enum NotificationCategories {
    enum Category {
        static let approval = "APPROVAL"
        static let approvalBiometric = "APPROVAL_BIOMETRIC"
        static let question = "QUESTION"
        static let result = "RESULT"
        static let alert = "ALERT"
        static let brief = "BRIEF"
        static let factReview = "FACT_REVIEW"
        static let suggestion = "SUGGESTION"
    }

    enum Action {
        static let approve = "APPROVE"
        static let deny = "DENY"
        static let details = "DETAILS"
        static let reviewFaceID = "REVIEW_FACEID"
        static let reply = "REPLY"
        static let open = "OPEN"
        static let saveToVault = "SAVE_TO_VAULT"
        static let share = "SHARE"
        static let investigate = "INVESTIGATE"
        static let autoFix = "AUTO_FIX"
        static let mute = "MUTE_1H"
        static let correct = "FACT_CORRECT"
        static let wrong = "FACT_WRONG"
        static let doIt = "DO_IT"
        static let notNow = "NOT_NOW"
        static let never = "NEVER"
    }

    static func register() {
        func action(_ id: String, _ title: String, _ icon: String, _ options: UNNotificationActionOptions = []) -> UNNotificationAction {
            UNNotificationAction(identifier: id, title: title, options: options, icon: UNNotificationActionIcon(systemImageName: icon))
        }
        // All approval actions require an unlocked device. While the Hub verifies every decision
        // with the Face ID-bound approval key (ApprovalSigner.swift), they must also bring the
        // app to the foreground so the approval sheet can run Face ID.
        let decisionOptions: UNNotificationActionOptions = ApprovalSignature.backgroundDecisionsSupported
            ? [.authenticationRequired]
            : [.authenticationRequired, .foreground]
        let approve = action(Action.approve, "Approve", "checkmark", decisionOptions)
        let deny = action(Action.deny, "Deny", "xmark", decisionOptions.union(.destructive))
        let details = action(Action.details, "Details", "doc.text.magnifyingglass", [.foreground, .authenticationRequired])
        let reviewFaceID = action(Action.reviewFaceID, "Review & Face ID", "faceid", [.foreground, .authenticationRequired])
        let reply = UNTextInputNotificationAction(
            identifier: Action.reply,
            title: "Reply",
            options: [.authenticationRequired],
            icon: UNNotificationActionIcon(systemImageName: "arrowshape.turn.up.left"),
            textInputButtonTitle: "Send",
            textInputPlaceholder: "Answer the agent…"
        )
        let open = action(Action.open, "Open", "arrow.up.forward.app", [.foreground])

        let categories: Set<UNNotificationCategory> = [
            UNNotificationCategory(identifier: Category.approval, actions: [approve, deny, details], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.approvalBiometric, actions: [reviewFaceID, deny], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.question, actions: [reply, open], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.result, actions: [
                open,
                action(Action.saveToVault, "Save to vault", "tray.and.arrow.down"),
                action(Action.share, "Share", "square.and.arrow.up", [.foreground]),
            ], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.alert, actions: [
                action(Action.investigate, "Investigate", "magnifyingglass", [.foreground]),
                action(Action.autoFix, "Auto-fix", "wrench.and.screwdriver", [.authenticationRequired]),
                action(Action.mute, "Mute 1 h", "bell.slash"),
            ], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.brief, actions: [open], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.factReview, actions: [
                action(Action.correct, "Correct", "checkmark"),
                action(Action.wrong, "Wrong", "xmark"),
            ], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: Category.suggestion, actions: [
                action(Action.doIt, "Do it", "bolt", [.authenticationRequired]),
                action(Action.notNow, "Not now", "clock"),
                action(Action.never, "Never", "nosign", [.destructive]),
            ], intentIdentifiers: [], options: []),
        ]
        UNUserNotificationCenter.current().setNotificationCategories(categories)
    }

    /// Asks for permission (once) and registers for remote notifications.
    @MainActor
    static func requestAuthorizationAndRegister() async {
        let center = UNUserNotificationCenter.current()
        let settings = await center.notificationSettings()
        if settings.authorizationStatus == .notDetermined {
            _ = try? await center.requestAuthorization(options: [.alert, .badge, .sound, .providesAppNotificationSettings])
        }
        UIApplication.shared.registerForRemoteNotifications()
    }
}

/// Handles notification action responses → Hub calls.
@MainActor
enum NotificationResponseHandler {
    static func handle(_ response: UNNotificationResponse, store: NexusStore, router: AppRouter) async {
        let content = response.notification.request.content
        let info = content.userInfo
        let approvalID = info["approval_id"] as? String
        let taskID = info["task_id"] as? String
        let questionID = info["question_id"] as? String
        let actions = NexusActions.shared()

        switch response.actionIdentifier {
        case NotificationCategories.Action.approve, NotificationCategories.Action.deny:
            guard let approvalID else { return }
            // Every decision is signed with the Face ID key today, so the button opens the
            // sheet. The sheet also works while the Hub is unreachable (away, VPN off): it shows
            // the cached approval, signs it, and queues the signed decision until the Hub answers
            // or the decision expires.
            guard ApprovalSignature.backgroundDecisionsSupported, !store.reachability.isUnavailable else {
                router.openApproval(approvalID)
                return
            }
            let approve = response.actionIdentifier == NotificationCategories.Action.approve
            do {
                try await actions?.decide(approvalID: approvalID, approve: approve, allowBiometric: false)
            } catch NexusActions.DecideError.requiresBiometric {
                router.openApproval(approvalID)
            } catch let error where HubClient.isTransportFailure(error) {
                router.openApproval(approvalID)
            } catch {
                await postFailure("Could not send decision: \(error.localizedDescription)")
            }

        case NotificationCategories.Action.details, NotificationCategories.Action.reviewFaceID:
            if let approvalID { router.openApproval(approvalID) }

        case NotificationCategories.Action.reply:
            guard let taskID, let text = (response as? UNTextInputNotificationResponse)?.userText, !text.isEmpty else { return }
            // Queued in the outbox if the Hub is unreachable right now.
            _ = try? await store.sendMessage(taskID: taskID, text: text, questionID: questionID)

        case NotificationCategories.Action.saveToVault:
            _ = try? await store.capture("\(content.title)\n\n\(content.body)")

        case NotificationCategories.Action.autoFix,
             NotificationCategories.Action.doIt,
             NotificationCategories.Action.notNow,
             NotificationCategories.Action.never,
             NotificationCategories.Action.mute,
             NotificationCategories.Action.correct,
             NotificationCategories.Action.wrong:
            // Sent to the task as a choice; the agent / Hub interprets it.
            guard let taskID else { return }
            let choice = response.actionIdentifier.lowercased()
            try? await actions?.sendMessage(taskID: taskID, text: choice, questionID: questionID, choice: choice)

        case NotificationCategories.Action.open,
             NotificationCategories.Action.investigate,
             NotificationCategories.Action.share,
             UNNotificationDefaultActionIdentifier:
            if let approvalID {
                router.openApproval(approvalID)
            } else if let taskID {
                router.openTask(taskID)
            } else {
                router.openInbox()
            }

        default:
            break
        }
        if !store.reachability.isUnavailable {
            await store.refresh()
        }
    }

    private static func postFailure(_ message: String) async {
        let content = UNMutableNotificationContent()
        content.title = "Nexus"
        content.body = message
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        try? await UNUserNotificationCenter.current().add(request)
    }
}
