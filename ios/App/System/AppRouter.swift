import Foundation
import NexusCore
import Observation
import SwiftUI

/// Identifiable wrapper so `.sheet(item:)` can present an approval by id.
struct ApprovalRoute: Identifiable, Hashable {
    let id: String
}

/// Navigation state + deep-link handling (`nexus://approval/<id>`, `nexus://task/<id>`, …).
///
/// Tabs match the web's phone tab bar (Home, Inbox, Chat, Work, More); Work has the web's
/// section tabs (Tasks, Teams, Agents, Architect, Spend) and More holds Knowledge, Machines and
/// Settings. Every tab has its own `NavigationStack` with the shared destinations
/// (`.nexusDestinations()`).
@Observable
@MainActor
final class AppRouter {
    enum Tab: Hashable {
        case home, inbox, chat, work, more
    }

    enum WorkSection: String, CaseIterable, Identifiable, Hashable {
        case tasks, teams, agents, architect, spend

        var id: String { rawValue }

        var title: String {
            switch self {
            case .tasks: return "Tasks"
            case .teams: return "Teams"
            case .agents: return "Agents"
            case .architect: return "Architect"
            case .spend: return "Spend"
            }
        }
    }

    var tab: Tab = .home
    var workSection: WorkSection = .tasks
    var homePath = NavigationPath()
    var inboxPath = NavigationPath()
    var workPath = NavigationPath()
    var morePath = NavigationPath()
    var approval: ApprovalRoute?
    var showCapture = false
    var showSettings = false
    var showKillSwitchConfirmation = false
    var focusComposer = false
    /// Pairing payload scanned from a `nexus://pair?...` link while already paired.
    var pendingPairing: PairingPayload?
    /// Agent to pre-select in the Architect's "Improve an agent" form.
    var architectImproveAgentID: String?

    func openApproval(_ id: String) {
        approval = ApprovalRoute(id: id)
    }

    func openTask(_ id: String) {
        tab = .work
        workSection = .tasks
        workPath = NavigationPath([TaskRoute(id: id)])
    }

    func openInbox() {
        tab = .inbox
        inboxPath = NavigationPath()
    }

    func openMachines(nodeID: String? = nil) {
        tab = .more
        var path = NavigationPath()
        path.append(MoreRoute.machines)
        if let nodeID {
            path.append(MachineRoute(id: nodeID))
        }
        morePath = path
    }

    func openWork(_ section: WorkSection) {
        tab = .work
        workSection = section
        workPath = NavigationPath()
    }

    /// Opens the Architect with "Improve an agent" pre-filled.
    func improveAgent(_ agentID: String) {
        architectImproveAgentID = agentID
        openWork(.architect)
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
            openInbox()
        case "machines":
            openMachines(nodeID: parts.first)
        case "teams":
            openWork(.teams)
        case "architect":
            openWork(.architect)
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
