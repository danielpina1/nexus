import Foundation
import NexusAPI

/// Teams (docs/SWARMS.md) and the Agent Architect (docs/ARCHITECT.md) over `TeamService` and
/// `ArchitectService`. Unary calls only; live progress arrives on the event stream
/// (`TaskUpdated` for the tree, `TeamEvent` for the timeline) and through polling in the views.
/// Demo mode answers reads from `MockData` and refuses writes.
public extension NexusStore {
    // MARK: Teams

    func listTeams(includeDisabled: Bool = true) async throws -> [Nexus_V1_Team] {
        if isDemo { return MockData.teams }
        let hub = try requireHub()
        var request = Nexus_V1_ListTeamsRequest()
        request.includeDisabled = includeDisabled
        return try await hub.call { headers in
            await hub.teams.listTeams(request: request, headers: headers)
        }.teams
    }

    /// The team definition and its Markdown note.
    func team(id: String) async throws -> (team: Nexus_V1_Team, markdown: String) {
        if isDemo {
            guard let team = MockData.teams.first(where: { $0.id == id }) else { throw HubError.demoMode }
            return (team, "")
        }
        let hub = try requireHub()
        var request = Nexus_V1_GetTeamRequest()
        request.id = id
        let response = try await hub.call { headers in
            await hub.teams.getTeam(request: request, headers: headers)
        }
        return (response.team, response.markdown)
    }

    /// Recent runs (root tasks) of a team, newest first. Merged into `tasks`.
    func teamRuns(teamID: String, limit: Int32 = 20) async throws -> [Nexus_V1_Task] {
        if isDemo {
            return MockData.teamRunTasks.filter { $0.teamID == teamID && $0.parentID.isEmpty }
        }
        let hub = try requireHub()
        var request = Nexus_V1_ListTasksRequest()
        request.teamID = teamID
        request.rootsOnly = true
        var page = Nexus_V1_PageRequest()
        page.pageSize = limit
        request.page = page
        let runs = try await hub.call { headers in
            await hub.tasks.listTasks(request: request, headers: headers)
        }.tasks
        upsert(tasks: runs)
        return runs.sorted { $0.createdAt.date > $1.createdAt.date }
    }

    /// The live picture of a team run / task tree. Its tasks are merged into `tasks`, so the
    /// tree keeps updating from the event stream between polls.
    func teamRun(rootTaskID: String) async throws -> Nexus_V1_TeamRun {
        if isDemo { return MockData.teamRun }
        let hub = try requireHub()
        var request = Nexus_V1_GetTeamRunRequest()
        request.rootTaskID = rootTaskID
        let run = try await hub.call { headers in
            await hub.teams.getTeamRun(request: request, headers: headers)
        }.run
        upsert(tasks: run.tasks.map(\.task) + (run.hasRoot ? [run.root] : []))
        return run
    }

    /// Talks to a running team (`to`: role, task id, "orchestrator" or "all").
    func sendTeamMessage(rootTaskID: String, to: String, text: String) async throws -> Nexus_V1_TeamMessage {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_SendTeamMessageRequest()
        request.rootTaskID = rootTaskID
        request.to = to
        request.text = text
        return try await hub.call { headers in
            await hub.teams.sendTeamMessage(request: request, headers: headers)
        }.message
    }

    /// Starts a team run: the task becomes the root of the team's tree.
    func runTeam(teamID: String, prompt: String) async throws -> Nexus_V1_Task {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_CreateTaskRequest()
        request.prompt = prompt
        request.teamID = teamID
        request.source = "ios"
        request.idempotencyKey = .idempotencyKey()
        let task = try await hub.call { headers in
            await hub.tasks.createTask(request: request, headers: headers)
        }.task
        upsert(tasks: [task])
        return task
    }

    // MARK: Architect

    func drafts(limit: Int32 = 100) async throws -> [Nexus_V1_AgentDraft] {
        if isDemo { return MockData.drafts }
        let hub = try requireHub()
        var request = Nexus_V1_ListDraftsRequest()
        request.limit = limit
        return try await hub.call { headers in
            await hub.architect.listDrafts(request: request, headers: headers)
        }.drafts
    }

    func draft(id: String) async throws -> Nexus_V1_AgentDraft {
        if isDemo {
            guard let draft = MockData.drafts.first(where: { $0.id == id }) else { throw HubError.demoMode }
            return draft
        }
        let hub = try requireHub()
        var request = Nexus_V1_GetDraftRequest()
        request.id = id
        return try await hub.call { headers in
            await hub.architect.getDraft(request: request, headers: headers)
        }.draft
    }

    /// Starts a draft (returns at once in state DRAFTING; poll `draft(id:)`).
    func createDraft(request text: String, kind: Nexus_V1_DraftKind, context: String = "", updateAgentID: String = "") async throws -> Nexus_V1_AgentDraft {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_DraftRequest()
        request.request = text
        request.kind = kind
        request.context = context
        request.updateAgentID = updateAgentID
        request.wait = false
        return try await hub.call { headers in
            await hub.architect.draft(request: request, headers: headers)
        }.draft
    }

    func refineDraft(id: String, feedback: String) async throws -> Nexus_V1_AgentDraft {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_RefineRequest()
        request.draftID = id
        request.feedback = feedback
        request.wait = false
        return try await hub.call { headers in
            await hub.architect.refine(request: request, headers: headers)
        }.draft
    }

    /// Runs the draft's test cases in a sandbox (no external, spend or destructive actions).
    func testDraft(id: String) async throws -> Nexus_V1_AgentDraft {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_TestRunRequest()
        request.draftID = id
        request.wait = false
        return try await hub.call { headers in
            await hub.architect.testRun(request: request, headers: headers)
        }.draft
    }

    /// Asks the owner to approve publishing; the notes are written when it is granted.
    func publishDraft(id: String) async throws -> (draft: Nexus_V1_AgentDraft, approval: Nexus_V1_Approval?) {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_PublishRequest()
        request.draftID = id
        let response = try await hub.call { headers in
            await hub.architect.publish(request: request, headers: headers)
        }
        if response.hasApproval {
            upsert(approval: response.approval)
        }
        return (response.draft, response.hasApproval ? response.approval : nil)
    }

    func discardDraft(id: String) async throws -> Nexus_V1_AgentDraft {
        if isDemo { throw HubError.demoMode }
        let hub = try requireHub()
        var request = Nexus_V1_DiscardRequest()
        request.id = id
        return try await hub.call { headers in
            await hub.architect.discard(request: request, headers: headers)
        }.draft
    }

    // MARK: Helpers

    private func requireHub() throws -> HubClient {
        guard let hub = hubClient else { throw HubError.notConfigured }
        return hub
    }
}

// MARK: - Display helpers

public extension Nexus_V1_TeamStrategy {
    /// Web wording (web/src/lib/teams.ts).
    var label: String {
        switch self {
        case .parallel: return "parallel"
        case .pipeline: return "pipeline"
        case .debate: return "debate"
        case .auto: return "auto"
        default: return "hierarchical"
        }
    }

    var hint: String {
        switch self {
        case .parallel: return "Every member gets the task at once; the orchestrator merges the results."
        case .pipeline: return "Members run in order, each consuming the previous output."
        case .debate: return "Members argue over several rounds; a judge decides."
        case .auto: return "The orchestrator picks: delegate itself or let the hub run a fixed pattern."
        default: return "The orchestrator plans, delegates, awaits and synthesises."
        }
    }
}

public extension Nexus_V1_Team {
    /// Worker slots across all members (pools counted).
    var memberSlots: Int {
        members.reduce(0) { $0 + max(1, Int($1.count)) }
    }

    /// The SF Symbol for the team (`icon` in the team note), or a default.
    var symbolName: String {
        AgentIcon.isSFSymbol(icon) ? icon : "person.3"
    }
}

public extension Nexus_V1_DraftState {
    /// Web wording (web/src/lib/architect.ts).
    var label: String {
        switch self {
        case .drafting: return "Drafting"
        case .ready: return "Ready"
        case .invalid: return "Has problems"
        case .testing: return "Testing"
        case .pendingApproval: return "Awaiting approval"
        case .published: return "Published"
        case .discarded: return "Discarded"
        case .failed: return "Failed"
        default: return "Unknown"
        }
    }

    /// Drafting or testing: the Hub is still working on it.
    var isBusy: Bool { self == .drafting || self == .testing }

    /// Kept in the "Open" list until published or discarded.
    var isOpen: Bool {
        switch self {
        case .drafting, .ready, .invalid, .testing, .pendingApproval, .failed: return true
        default: return false
        }
    }
}

public extension Nexus_V1_DraftKind {
    var label: String {
        switch self {
        case .agent: return "agent"
        case .team: return "team"
        case .workflow: return "workflow"
        default: return "auto"
        }
    }

    var symbolName: String {
        switch self {
        case .team: return "person.3"
        case .workflow: return "arrow.triangle.branch"
        default: return "cpu"
        }
    }
}

public extension Nexus_V1_AgentDraft {
    /// A short name: its first agent note id, else the request (web: draftTitle).
    var displayTitle: String {
        if let note = notes.first(where: { $0.kind == "agent" }) ?? notes.first, !note.id.isEmpty {
            return note.id
        }
        if !updateAgentID.isEmpty { return "Improve \(updateAgentID)" }
        if request.count > 70 { return String(request.prefix(68)) + "…" }
        return request.isEmpty ? id : request
    }
}
