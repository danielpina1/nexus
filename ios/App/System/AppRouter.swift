import Foundation
import NexusCore
import Observation

/// Identifiable wrapper so `.sheet(item:)` can present an approval by id.
struct ApprovalRoute: Identifiable, Hashable {
    let id: String
}

/// Navigation state + deep-link handling (`nexus://approval/<id>`, `nexus://task/<id>`, …).
@Observable
@MainActor
final class AppRouter {
    enum Tab: Hashable {
        case home, chat, tasks, vault, machines
    }

    var tab: Tab = .home
    var approval: ApprovalRoute?
    var tasksPath: [String] = []
    var machinesPath: [String] = []
    var showCapture = false
    var showSettings = false
    var showKillSwitchConfirmation = false
    var focusComposer = false
    /// Pairing payload scanned from a `nexus://pair?...` link while already paired.
    var pendingPairing: PairingPayload?

    func openApproval(_ id: String) {
        approval = ApprovalRoute(id: id)
    }

    func openTask(_ id: String) {
        tab = .tasks
        tasksPath = [id]
    }

    func handle(url: URL) {
        guard url.scheme?.lowercased() == DeepLink.scheme else { return }
        let parts = url.pathComponents.filter { $0 != "/" }
        switch url.host?.lowercased() {
        case "approval":
            if let id = parts.first { openApproval(id) }
        case "task":
            if let id = parts.first { openTask(id) }
        case "inbox":
            tab = .home
        case "machines":
            tab = .machines
        case "capture":
            showCapture = true
        case "killswitch":
            showKillSwitchConfirmation = true
        case "pair":
            pendingPairing = try? PairingPayload(string: url.absoluteString)
        default:
            break
        }
    }

    func handle(_ action: PendingAction) {
        switch action {
        case .killSwitch:
            showKillSwitchConfirmation = true
        case .capture:
            showCapture = true
        case .askNexus:
            tab = .chat
            focusComposer = true
        }
    }

    /// Picks up an action requested by a widget / control / Siri intent.
    func consumePendingAction() {
        if let action = PendingAction.take() {
            handle(action)
        }
    }
}
