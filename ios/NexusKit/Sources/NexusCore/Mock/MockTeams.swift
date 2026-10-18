import Foundation
import NexusAPI
import SwiftProtobuf

/// Demo-mode teams, a live team run and Architect drafts (docs/SWARMS.md, docs/ARCHITECT.md).
public extension MockData {
    static let teamRunRootID = "tsk_trip"

    static let teams: [Nexus_V1_Team] = {
        func member(_ agent: String, _ role: String, count: Int32 = 1, when: String = "") -> Nexus_V1_TeamMember {
            var m = Nexus_V1_TeamMember()
            m.agentID = agent
            m.role = role
            m.count = count
            m.when = when
            return m
        }
        var trip = Nexus_V1_Team()
        trip.id = "trip-planner"
        trip.name = "Trip planner"
        trip.description_p = "Plans a weekend trip: places, weather and bookings."
        trip.icon = "airplane"
        trip.orchestratorAgentID = "assistant"
        trip.members = [
            member("researcher", "scout", count: 2, when: "Find places, food and opening hours"),
            member("assistant", "weather", when: "Check the forecast for the dates"),
            member("assistant", "booker", when: "Draft bookings; never pay without approval"),
        ]
        trip.strategy = .hierarchical
        trip.blackboard = true
        var limits = Nexus_V1_TeamLimits()
        limits.maxParallel = 3
        limits.maxTasks = 12
        limits.maxDepth = 2
        limits.maxRuns = 20
        limits.maxCost = money(300)
        limits.timeoutSeconds = 1800
        trip.limits = limits
        trip.enabled = true
        trip.version = 3
        var note = Nexus_V1_NoteRef()
        note.path = "teams/trip-planner.md"
        note.title = "Trip planner"
        trip.note = note

        var review = Nexus_V1_Team()
        review.id = "code-review"
        review.name = "Code review panel"
        review.description_p = "Three reviewers debate a pull request; a judge writes the verdict."
        review.icon = "checklist"
        review.members = [
            member("code-fixer", "security"),
            member("code-fixer", "performance"),
            member("researcher", "judge"),
        ]
        review.strategy = .debate
        review.debateRounds = 2
        review.blackboard = true
        var reviewLimits = Nexus_V1_TeamLimits()
        reviewLimits.maxParallel = 3
        reviewLimits.maxTasks = 8
        reviewLimits.maxCost = money(150)
        review.limits = reviewLimits
        review.enabled = true
        review.version = 1
        review.warnings = ["Member \"judge\" has no `when:`; the orchestrator may not use it well."]
        return [trip, review]
    }()

    /// The root and member tasks of the demo team run.
    static let teamRunTasks: [Nexus_V1_Task] = {
        func task(_ id: String, _ title: String, agent: String, role: String, state: Nexus_V1_TaskState, depth: Int32, cents: Int64, ago: TimeInterval, summary: String = "", phase: String = "") -> Nexus_V1_Task {
            var t = Nexus_V1_Task()
            t.id = id
            t.title = title
            t.prompt = title
            t.agentID = agent
            t.state = state
            t.role = role
            t.depth = depth
            t.phase = phase
            t.teamID = "trip-planner"
            t.parentID = depth == 0 ? "" : teamRunRootID
            t.rootID = teamRunRootID
            t.nodeID = "node_srv"
            t.summary = summary
            t.source = "web"
            var c = Nexus_V1_Cost()
            c.amount = money(cents)
            c.runtime = "openai"
            c.model = "gpt-5-mini"
            t.cost = c
            t.createdAt = ts(ago)
            t.updatedAt = ts(max(ago - 60, 5))
            return t
        }
        var root = task(teamRunRootID, "Weekend in Porto, 14–16 Nov", agent: "assistant", role: "orchestrator", state: .running, depth: 0, cents: 22, ago: 540)
        var treeCost = Nexus_V1_Cost()
        treeCost.amount = money(118)
        root.treeCost = treeCost
        return [
            root,
            task("tsk_trip_scout1", "Find 5 places to eat near Ribeira", agent: "researcher", role: "scout", state: .succeeded, depth: 1, cents: 41, ago: 480, summary: "5 places with hours and prices on the blackboard."),
            task("tsk_trip_scout2", "Museums open on Sunday", agent: "researcher", role: "scout", state: .running, depth: 1, cents: 27, ago: 300),
            task("tsk_trip_weather", "Forecast for 14–16 Nov", agent: "assistant", role: "weather", state: .succeeded, depth: 1, cents: 6, ago: 420, summary: "Rain Saturday morning, dry Sunday."),
            task("tsk_trip_booker", "Hold a table for Saturday dinner", agent: "assistant", role: "booker", state: .waitingApproval, depth: 1, cents: 22, ago: 180),
            task("tsk_trip_hotel", "Compare 3 hotels", agent: "researcher", role: "scout", state: .queued, depth: 1, cents: 0, ago: 60, phase: "waiting_slot"),
        ]
    }()

    static let teamRun: Nexus_V1_TeamRun = {
        var run = Nexus_V1_TeamRun()
        run.root = teamRunTasks[0]
        run.team = teams[0]
        run.tasks = teamRunTasks.map { task in
            var item = Nexus_V1_TeamRunTask()
            item.task = task
            item.runs = task.state == .queued ? 0 : 1
            item.memberRole = task.role == "orchestrator" ? "" : task.role
            return item
        }
        func entry(_ key: String, _ text: String, role: String, version: Int32, ago: TimeInterval) -> Nexus_V1_BlackboardEntry {
            var e = Nexus_V1_BlackboardEntry()
            e.key = key
            e.value = Google_Protobuf_Value(stringValue: text)
            e.authorRole = role
            e.version = version
            e.updatedAt = ts(ago)
            return e
        }
        run.blackboard = [
            entry("plan", "1. Places to eat (scouts)\n2. Weather (weather)\n3. Dinner booking for Saturday (booker, needs approval)\n4. Hotel shortlist", role: "orchestrator", version: 2, ago: 500),
            entry("restaurants", "Cantinho do Avillez · O Gaveto · Taberna dos Mercadores · Casa Guedes · Majestic Café", role: "scout", version: 1, ago: 400),
            entry("weather", "Sat: rain until 11:00, 14 °C. Sun: dry, 16 °C.", role: "weather", version: 1, ago: 380),
        ]
        func message(_ id: String, from: String, to: String, _ text: String, ago: TimeInterval) -> Nexus_V1_TeamMessage {
            var m = Nexus_V1_TeamMessage()
            m.id = id
            m.fromRole = from
            m.to = to
            m.text = text
            m.at = ts(ago)
            return m
        }
        run.messages = [
            message("msg_1", from: "orchestrator", to: "all", "Budget is €150 for dinners. Prefer places near Ribeira.", ago: 470),
            message("msg_2", from: "user", to: "orchestrator", "No seafood on Saturday, please.", ago: 200),
        ]
        var total = Nexus_V1_Cost()
        total.amount = money(118)
        run.totalCost = total
        run.totalTasks = Int32(teamRunTasks.count)
        run.totalRuns = 5
        run.activeTasks = 2
        run.queuedTasks = 1
        run.limits = teams[0].limits
        run.deadline = ts(540 - 1800)
        return run
    }()

    static let teamEvents: [TeamEventItem] = {
        func event(_ kind: Nexus_V1_TeamEventKind, task: String, role: String, _ text: String, key: String = "", ago: TimeInterval) -> TeamEventItem {
            var e = Nexus_V1_TeamEvent()
            e.kind = kind
            e.teamID = "trip-planner"
            e.rootTaskID = teamRunRootID
            e.taskID = task
            e.role = role
            e.text = text
            e.key = key
            return TeamEventItem(id: "demo_\(task)_\(Int(ago))", at: Date().addingTimeInterval(-ago), event: e)
        }
        return [
            event(.started, task: teamRunRootID, role: "orchestrator", "Weekend in Porto, 14–16 Nov", ago: 540),
            event(.blackboardUpdated, task: teamRunRootID, role: "orchestrator", "", key: "plan", ago: 500),
            event(.delegated, task: "tsk_trip_scout1", role: "scout", "Find 5 places to eat near Ribeira", ago: 480),
            event(.delegated, task: "tsk_trip_weather", role: "weather", "Forecast for 14–16 Nov", ago: 420),
            event(.memberFinished, task: "tsk_trip_weather", role: "weather", "Rain Saturday morning, dry Sunday.", ago: 380),
            event(.memberFinished, task: "tsk_trip_scout1", role: "scout", "5 places with hours and prices.", ago: 360),
            event(.delegated, task: "tsk_trip_scout2", role: "scout", "Museums open on Sunday", ago: 300),
            event(.delegated, task: "tsk_trip_booker", role: "booker", "Hold a table for Saturday dinner", ago: 180),
            event(.approvalPending, task: "tsk_trip_booker", role: "booker", "Reserve a table for 2 at O Gaveto, Sat 20:30", ago: 150),
            event(.limitReached, task: "tsk_trip_hotel", role: "scout", "max_parallel 3 reached; the hotel comparison waits for a slot", ago: 60),
        ]
    }()

    static let teamApproval: Nexus_V1_Approval = {
        var a = Nexus_V1_Approval()
        a.id = "apr_demo_table"
        a.taskID = "tsk_trip_booker"
        a.runID = "run_tsk_trip_booker"
        a.agentID = "assistant"
        a.actionClass = .writeExternal
        a.level = .approve
        a.title = "Reserve a table for 2 at O Gaveto"
        a.summary = "Saturday 20:30 · no deposit · cancel until 18:00"
        a.payload = (try? Google_Protobuf_Struct(jsonString: #"{"restaurant":"O Gaveto","people":2,"time":"Sat 20:30"}"#)) ?? Google_Protobuf_Struct()
        a.payloadHash = (try? CanonicalJSON.actionHash(of: a.payload)) ?? Data()
        a.challenge = Data(repeating: 0x03, count: 32)
        a.state = .pending
        a.createdAt = ts(150)
        a.expiresAt = ts(-540)
        a.nodeID = "node_srv"
        a.rootTaskID = teamRunRootID
        a.teamID = "trip-planner"
        a.role = "booker"
        return a
    }()

    static let drafts: [Nexus_V1_AgentDraft] = {
        func note(_ kind: String, _ id: String, _ markdown: String, diff: String = "", exists: Bool = false) -> Nexus_V1_DraftNote {
            var n = Nexus_V1_DraftNote()
            n.kind = kind
            n.id = id
            n.path = "\(kind)s/\(id).md"
            n.markdown = markdown
            n.diff = diff
            n.exists = exists
            return n
        }
        var ready = Nexus_V1_AgentDraft()
        ready.id = "drf_airpods"
        ready.kind = .agent
        ready.state = .ready
        ready.request = "Tell me when the AirPods Pro drop below €150 at Worten or Fnac"
        ready.notes = [
            note("agent", "price-watch-airpods", "---\nname: Price watch (AirPods)\nicon: tag\nruntime: browser\ntier: simple\ntools: [browser.read, notify]\n---\n\n# Price watch\n\nCheck **Worten** and **Fnac** for AirPods Pro 2 every morning.\n\n- Notify when the price is below €150.\n- Never buy anything.\n"),
            note("workflow", "price-watch-airpods-daily", "---\ntrigger: cron 0 8 * * *\nagent: price-watch-airpods\n---\n\nRun the price watch every day at 08:00.\n"),
        ]
        ready.rationale = "A small **browser** agent on the simple tier is enough: it only reads two product pages. A daily workflow runs it; notifications go to your iPhone."
        ready.risks = ["Store pages change layout; the agent may misread a price.", "Promotions behind a login are not visible."]
        var case1 = Nexus_V1_DraftTestCase()
        case1.name = "Price above threshold"
        case1.input = "Worten shows €179"
        case1.expect = ["not_contains: buy", "no_calls: notify"]
        var case2 = Nexus_V1_DraftTestCase()
        case2.name = "Price drop"
        case2.input = "Fnac shows €149"
        case2.expect = ["calls: notify", "Mentions the store"]
        ready.testCases = [case1, case2]
        var result1 = Nexus_V1_DraftTestResult()
        result1.name = case1.name
        result1.input = case1.input
        result1.passed = true
        result1.output = "No drop yet (Worten €179, Fnac €182)."
        result1.checks = ["✓ not_contains: buy", "✓ no_calls: notify"]
        result1.mode = "native_loop"
        ready.testResults = [result1]
        ready.fixes = ["Added `notify` to tools: the request asks to be told."]
        ready.via = "agent:agent-architect"
        ready.revision = 2
        ready.feedback = ["Also check Fnac."]
        ready.createdAt = ts(3600)
        ready.updatedAt = ts(600)

        var invalid = Nexus_V1_AgentDraft()
        invalid.id = "drf_bills"
        invalid.kind = .auto
        invalid.state = .invalid
        invalid.request = "Watch my electricity bill emails and alert me if a month is 20 % above average"
        invalid.notes = [note("agent", "bill-watch", "---\nname: Bill watch\nruntime: openai\ntools: [email.read, anthrex.exec]\n---\n\nRead EDP bills and compare with the 12-month average.\n")]
        invalid.problems = ["Tool `anthrex.exec` is not allowed for a read-only agent."]
        invalid.via = "llm:claude-sonnet"
        invalid.revision = 1
        invalid.createdAt = ts(7200)
        invalid.updatedAt = ts(7000)

        var published = Nexus_V1_AgentDraft()
        published.id = "drf_digest"
        published.kind = .workflow
        published.state = .published
        published.request = "Every Friday, summarise what changed in my nexus repo this week"
        published.notes = [note("workflow", "weekly-repo-digest", "---\ntrigger: cron 0 17 * * 5\nagent: researcher\n---\n\nSummarise this week's commits in nexus.\n")]
        published.via = "llm:claude-sonnet"
        published.revision = 1
        published.createdAt = ts(400_000)
        published.updatedAt = ts(390_000)
        return [ready, invalid, published]
    }()
}
