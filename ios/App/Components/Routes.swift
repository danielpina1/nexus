import NexusCore
import SwiftUI

// Typed navigation values shared by every tab's NavigationStack (web routes: /tasks/:id,
// /machines/:id, /teams/:id, /teams/runs/:id, /architect/:id, /agents/:id, /vault?note=…).

struct TaskRoute: Hashable {
    let id: String
}

struct MachineRoute: Hashable {
    let id: String
}

struct FilesRoute: Hashable {
    let nodeID: String
    let path: String
}

struct TeamRoute: Hashable {
    let id: String
}

struct TeamRunRoute: Hashable {
    let rootTaskID: String
}

struct DraftRoute: Hashable {
    let id: String
}

struct AgentRoute: Hashable {
    let id: String
}

struct NoteRoute: Hashable {
    let id: String
    let path: String
    let title: String
}

/// Sections under the More tab.
enum MoreRoute: Hashable {
    case knowledge, machines, settings
}

/// Registers every destination once, at the root of a tab's NavigationStack.
struct NexusDestinations: ViewModifier {
    func body(content: Content) -> some View {
        content
            .navigationDestination(for: TaskRoute.self) { TaskDetailView(taskID: $0.id) }
            .navigationDestination(for: MachineRoute.self) { MachineDetailView(nodeID: $0.id) }
            .navigationDestination(for: FilesRoute.self) { FilesView(nodeID: $0.nodeID, path: $0.path) }
            .navigationDestination(for: TeamRoute.self) { TeamDetailView(teamID: $0.id) }
            .navigationDestination(for: TeamRunRoute.self) { TeamRunView(rootTaskID: $0.rootTaskID) }
            .navigationDestination(for: DraftRoute.self) { DraftView(draftID: $0.id) }
            .navigationDestination(for: AgentRoute.self) { AgentDetailView(agentID: $0.id) }
            .navigationDestination(for: NoteRoute.self) { NoteView(route: $0) }
            .navigationDestination(for: ChatThread.self) { ChatThreadView(thread: $0) }
            .navigationDestination(for: MoreRoute.self) { route in
                switch route {
                case .knowledge: KnowledgeView()
                case .machines: MachinesView()
                case .settings: SettingsForm(isSheet: false)
                }
            }
    }
}

extension View {
    func nexusDestinations() -> some View {
        modifier(NexusDestinations())
    }
}
