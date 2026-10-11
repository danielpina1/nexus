import Foundation
import NexusAPI
import SwiftProtobuf

/// Sample data for SwiftUI previews and demo mode (no Hub needed).
public enum MockData {
    static func ts(_ secondsAgo: TimeInterval) -> Google_Protobuf_Timestamp {
        Google_Protobuf_Timestamp(date: Date().addingTimeInterval(-secondsAgo))
    }

    static func money(_ cents: Int64) -> Nexus_V1_Money {
        var m = Nexus_V1_Money()
        m.currency = "EUR"
        m.minorUnits = cents
        return m
    }

    public static let agents: [Nexus_V1_Agent] = {
        func agent(_ id: String, _ name: String, _ icon: String, _ runtime: String, _ description: String) -> Nexus_V1_Agent {
            var a = Nexus_V1_Agent()
            a.id = id
            a.name = name
            a.icon = icon
            a.runtime = runtime
            a.description_p = description
            a.enabled = true
            return a
        }
        return [
            agent("code-fixer", "Code fixer", "hammer.fill", "claude-code", "Fixes failing tests and opens PRs"),
            agent("grocery", "Grocery", "cart.fill", "browser", "Fills the Continente cart"),
            agent("inbox", "Inbox triage", "tray.full.fill", "openai", "Sorts and drafts email replies"),
            agent("research", "Research", "magnifyingglass", "anthrex", "Deep research with sources"),
        ]
    }()

    public static let nodes: [Nexus_V1_Node] = {
        func node(_ id: String, _ name: String, _ os: String, online: Bool, cpu: Double, runs: Int32, battery: Double = 0) -> Nexus_V1_Node {
            var n = Nexus_V1_Node()
            n.id = id
            n.name = name
            n.os = os
            n.arch = "arm64"
            n.tags = [os]
            var rt = Nexus_V1_RuntimeInfo()
            rt.name = "claude-code"
            rt.version = "2.1"
            rt.available = true
            n.runtimes = [rt]
            var s = Nexus_V1_NodeStatus()
            s.nodeID = id
            s.online = online
            s.cpuPercent = cpu
            s.memoryPercent = 41
            s.diskPercent = 63
            s.batteryPercent = battery
            s.activeRuns = runs
            s.lastSeen = ts(online ? 5 : 3600)
            n.status = s
            return n
        }
        return [
            node("node_mac", "mac-mini", "macos", online: true, cpu: 23, runs: 2),
            node("node_srv", "server", "linux", online: true, cpu: 8, runs: 0),
            node("node_mbp", "macbook", "macos", online: false, cpu: 0, runs: 0, battery: 54),
        ]
    }()

    public static let tasks: [Nexus_V1_Task] = {
        func task(_ id: String, _ title: String, agent: String, state: Nexus_V1_TaskState, node: String, cents: Int64, ago: TimeInterval, summary: String = "", parent: String = "") -> Nexus_V1_Task {
            var t = Nexus_V1_Task()
            t.id = id
            t.title = title
            t.prompt = title
            t.agentID = agent
            t.state = state
            t.nodeID = node
            t.summary = summary
            t.parentID = parent
            t.rootID = parent.isEmpty ? id : parent
            t.currentRunID = "run_" + id
            t.source = "ios-chat"
            var c = Nexus_V1_Cost()
            c.amount = money(cents)
            t.cost = c
            t.createdAt = ts(ago)
            t.updatedAt = ts(ago / 4)
            return t
        }
        return [
            task("tsk_fix", "Fix flaky tests in nexus", agent: "code-fixer", state: .running, node: "node_mac", cents: 81, ago: 360),
            task("tsk_fix_a", "Run test suite", agent: "code-fixer", state: .succeeded, node: "node_mac", cents: 12, ago: 300, summary: "4/5 passing", parent: "tsk_fix"),
            task("tsk_groc", "Weekly groceries", agent: "grocery", state: .waitingApproval, node: "node_mac", cents: 31, ago: 900),
            task("tsk_mail", "Triage inbox", agent: "inbox", state: .waitingInput, node: "node_srv", cents: 4, ago: 1200),
            task("tsk_res", "Compare heat pumps", agent: "research", state: .succeeded, node: "node_srv", cents: 54, ago: 86_000, summary: "Daikin Altherma 3 wins on COP; report saved to vault."),
            task("tsk_old", "Rename photos", agent: "code-fixer", state: .failed, node: "node_mbp", cents: 2, ago: 172_000, summary: "Node offline"),
        ]
    }()

    public static let approvals: [Nexus_V1_Approval] = {
        var a = Nexus_V1_Approval()
        a.id = "apr_demo_checkout"
        a.taskID = "tsk_groc"
        a.runID = "run_tsk_groc"
        a.agentID = "grocery"
        a.actionClass = .spend
        a.level = .biometric
        a.title = "Checkout €84.30 at Continente"
        a.summary = "22 items · 2 substitutions · delivery Sat 9–11"
        a.payload = (try? Google_Protobuf_Struct(jsonString: #"{"store":"Continente","items":22,"total":"84.30 EUR","slot":"Sat 09:00-11:00"}"#)) ?? Google_Protobuf_Struct()
        a.payloadHash = (try? CanonicalJSON.actionHash(of: a.payload)) ?? Data()
        a.challenge = Data(repeating: 0x01, count: 32)
        a.state = .pending
        a.createdAt = ts(120)
        a.expiresAt = ts(-3600)
        a.nodeID = "node_mac"

        var b = Nexus_V1_Approval()
        b.id = "apr_demo_push"
        b.taskID = "tsk_fix"
        b.runID = "run_tsk_fix"
        b.agentID = "code-fixer"
        b.actionClass = .codePush
        b.level = .approve
        b.title = "Push branch fix/flaky-tests"
        b.summary = "+42 −13 · 3 files → origin/fix/flaky-tests"
        b.payload = (try? Google_Protobuf_Struct(jsonString: #"{"remote":"origin","branch":"fix/flaky-tests","commit":"9f2c1ab"}"#)) ?? Google_Protobuf_Struct()
        b.payloadHash = (try? CanonicalJSON.actionHash(of: b.payload)) ?? Data()
        b.challenge = Data(repeating: 0x02, count: 32)
        b.state = .pending
        b.createdAt = ts(60)
        b.expiresAt = ts(-1800)
        b.nodeID = "node_mac"
        return [a, b]
    }()

    public static let questions: [QuestionItem] = {
        var q = Nexus_V1_Question()
        q.questionID = "q_demo_mail"
        q.text = "Reply to Ana about Friday's meeting?"
        q.choices = ["Accept", "Propose Monday", "Skip"]
        q.allowFreeText = true
        return [QuestionItem(taskID: "tsk_mail", question: q, at: Date().addingTimeInterval(-300))]
    }()

    public static let notices: [NoticeItem] = {
        var brief = Nexus_V1_Notice()
        brief.id = "ntc_brief"
        brief.kind = .brief
        brief.title = "Good morning"
        brief.body = "2 agents ran overnight. Backup OK. 1 approval waiting. Rain after 16:00."
        var result = Nexus_V1_Notice()
        result.id = "ntc_res"
        result.kind = .result
        result.title = "Heat pump comparison ready"
        result.body = "Daikin Altherma 3 wins on COP; report saved to vault."
        return [
            NoticeItem(taskID: "", notice: brief, at: Date().addingTimeInterval(-3600)),
            NoticeItem(taskID: "tsk_res", notice: result, at: Date().addingTimeInterval(-7200)),
        ]
    }()

    public static func timeline(for taskID: String) -> [TimelineItem] {
        let now = Date()
        var call = Nexus_V1_ToolCall()
        call.callID = "call_1"
        call.tool = "shell.run"
        call.input = (try? Google_Protobuf_Struct(jsonString: #"{"cmd":"go test ./..."}"#)) ?? Google_Protobuf_Struct()
        call.actionClass = .compute
        var result = Nexus_V1_ToolResult()
        result.callID = "call_1"
        result.output = (try? Google_Protobuf_Struct(jsonString: #"{"passed":4,"failed":1}"#)) ?? Google_Protobuf_Struct()
        result.durationMs = 8400
        var cost = Nexus_V1_Cost()
        cost.amount = money(31)
        cost.model = "sonnet"
        cost.inputTokens = 18_200
        cost.outputTokens = 2_100
        let run = "run_" + taskID
        return [
            TimelineItem(id: "1", at: now.addingTimeInterval(-300), runID: run, kind: .userMessage("Fix the flaky tests in nexus")),
            TimelineItem(id: "2", at: now.addingTimeInterval(-290), runID: run, kind: .status(.running, summary: "")),
            TimelineItem(id: "3", at: now.addingTimeInterval(-280), runID: run, kind: .output("Looking at the failing tests. `TestEventReplay` is timing-dependent; I'll make the clock injectable.", isFinal: false)),
            TimelineItem(id: "4", at: now.addingTimeInterval(-200), runID: run, kind: .toolCall(call, result: result)),
            TimelineItem(id: "5", at: now.addingTimeInterval(-100), runID: run, kind: .cost(cost)),
            TimelineItem(id: "6", at: now.addingTimeInterval(-60), runID: run, kind: .output("4 of 5 passing. Fixing the last one now…", isFinal: false)),
        ]
    }

    public static let devices: [Nexus_V1_Device] = {
        var d = Nexus_V1_Device()
        d.id = "dev_iphone"
        d.name = "Daniel's iPhone"
        d.kind = .ios
        d.primary = true
        d.pairedAt = ts(86_400 * 12)
        d.lastSeen = ts(10)
        var w = Nexus_V1_Device()
        w.id = "dev_web"
        w.name = "Chrome on Mac"
        w.kind = .web
        w.pairedAt = ts(86_400 * 3)
        w.lastSeen = ts(7200)
        return [d, w]
    }()

    public static let searchHits: [Nexus_V1_SearchHit] = {
        func hit(_ id: String, _ title: String, _ path: String, _ snippet: String) -> Nexus_V1_SearchHit {
            var h = Nexus_V1_SearchHit()
            var ref = Nexus_V1_NoteRef()
            ref.id = id
            ref.title = title
            ref.path = path
            h.note = ref
            h.snippet = snippet
            h.score = 0.9
            return h
        }
        return [
            hit("n1", "Heat pumps", "research/heat-pumps.md", "Daikin Altherma 3 wins on COP…"),
            hit("n2", "Groceries", "lists/groceries.md", "Weekly basics: milk, eggs, bread…"),
            hit("n3", "2026-10-11", "daily/2026-10-11.md", "Call the plumber; review PR #42"),
        ]
    }()

    public static func note(id: String) -> Nexus_V1_Note {
        var n = Nexus_V1_Note()
        var ref = Nexus_V1_NoteRef()
        ref.id = id
        ref.title = "Heat pumps"
        ref.path = "research/heat-pumps.md"
        n.ref = ref
        n.type = "research"
        n.tags = ["home", "energy"]
        n.body = """
        # Heat pumps

        **Winner:** Daikin Altherma 3 — best *seasonal COP* in our climate.

        - Mitsubishi Ecodan: quieter, ~8% pricier
        - Vaillant aroTHERM: good app, weaker support locally

        See [[Energy budget]] and [the spreadsheet](https://example.com).
        """
        n.updatedAt = ts(86_000)
        return n
    }

    public static let files: [Nexus_V1_FileEntry] = {
        func f(_ name: String, dir: Bool, size: Int64) -> Nexus_V1_FileEntry {
            var e = Nexus_V1_FileEntry()
            e.name = name
            e.path = "/Users/me/" + name
            e.isDir = dir
            e.sizeBytes = size
            e.modifiedAt = ts(3600)
            return e
        }
        return [f("Projects", dir: true, size: 0), f("Downloads", dir: true, size: 0), f("notes.txt", dir: false, size: 2_048)]
    }()
}
