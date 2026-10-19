import Foundation
import NexusCore

/// Demo mode for the chat: scripted runs fed through `NexusStore.applyDemo` as if they came from
/// the Hub's event stream (cost announcement with the exact model, streamed text deltas every
/// 50 ms, tool calls, a Nexus Question with options, approvals with readable payloads,
/// sub-tasks). Questions and approvals also appear in the Inbox and are answered there or inline.
@MainActor
enum ChatDemo {
    static let model = "claude-sonnet-4-5-20250929"
    private static var seq: Int64 = 9_000_000

    // MARK: Entry points

    /// Runs a reply for a message typed in demo mode.
    static func run(input: String, command: ChatCommand, replyID: String, threadKey: String, chat: ChatStore) {
        let store = chat.store
        Task { @MainActor in
            switch command {
            case .search(let query):
                try? await Task.sleep(for: .milliseconds(600))
                let hits = (try? await store.search(query)) ?? []
                var response = Nexus_V1_ExecuteResponse()
                response.route.kind = .search
                response.answer = hits.isEmpty
                    ? "Nothing in your vault matches “\(query)”."
                    : "From your vault: **\(hits[0].note.title)**. \(hits[0].snippet)"
                response.sources = hits.prefix(3).map(\.note)
                chat.apply(response, to: replyID, in: threadKey, input: input)
            case .remember(let text):
                try? await Task.sleep(for: .milliseconds(400))
                var response = Nexus_V1_ExecuteResponse()
                response.route.kind = .capture
                var note = Nexus_V1_NoteRef()
                note.id = "demo-capture"
                note.path = "inbox/" + ToolText.clip(text.lowercased().replacingOccurrences(of: " ", with: "-"), 32) + ".md"
                note.title = ToolText.clip(text, 40)
                response.sources = [note]
                response.answer = "Saved to your vault."
                chat.apply(response, to: replyID, in: threadKey, input: input)
            case .mention, .message:
                let agentID: String
                if case .mention(let target, _) = ChatCommand.parse(input) { agentID = target } else { agentID = "assistant" }
                let task = startTask(prompt: command.prompt, agentID: agentID, store: store)
                var response = Nexus_V1_ExecuteResponse()
                response.task = task
                response.route.kind = .task
                response.route.agentID = agentID
                chat.apply(response, to: replyID, in: threadKey, input: input)
                await script(for: command.prompt, taskID: task.id, store: store)
            }
        }
    }

    /// A conversation that already shows a finished answer, an open question and a pending
    /// approval. The question and approval runs continue once answered (inline or in the Inbox).
    static func seed(chat: ChatStore) -> [ChatStore.Message] {
        let store = chat.store
        var messages: [ChatStore.Message] = []

        func pair(_ text: String, taskID: String, ago: TimeInterval, firstToken: Int?) {
            let at = Date().addingTimeInterval(-ago)
            messages.append(.init(id: ChatStore.newID(), role: .user, text: text, at: at))
            var reply = ChatStore.Message(id: ChatStore.newID(), role: .nexus, text: "", at: at)
            reply.taskID = taskID
            reply.prompt = text
            reply.firstTokenMs = firstToken
            messages.append(reply)
        }

        // 1. A finished answer: tool chip, Markdown, exact model.
        let t1 = startTask(prompt: "What can you do?", agentID: "assistant", store: store, ago: 420)
        tool(t1.id, "mcp__nexus__nexus_status", input: [:], output: #"{"agents":6,"running":2}"#, store: store)
        output(t1.id, capabilities, final: true, store: store)
        finish(t1.id, summary: "Explained what Nexus can do.", cents: 3, store: store)
        pair("What can you do?", taskID: t1.id, ago: 420, firstToken: 1180)

        // 2. Waiting for an answer (multi-select question with descriptions).
        let t2 = startTask(prompt: "Plan a weekend away for the two of us", agentID: "assistant", store: store, ago: 240)
        output(t2.id, "Happy to plan it. One quick question first so I don't book the wrong thing:", final: false, store: store)
        pair("Plan a weekend away for the two of us", taskID: t2.id, ago: 240, firstToken: 950)
        let q2 = askQuestion(t2.id, header: "Trip", text: "What should the weekend include?", options: [
            ("Hotels", "Two nights, near the centre"),
            ("Restaurants", "Dinner reservations both nights"),
            ("Museums", "Tickets for the top two"),
            ("Day trip", "A half-day trip out of town"),
        ], multiSelect: true, store: store)
        Task { @MainActor in
            guard let answer = await waitForAnswer(q2, taskID: t2.id, store: store) else { return }
            setState(t2.id, .running, store: store)
            await say(t2.id, "\n\nGreat — **\(answer)** it is. I checked your calendar and the 14th–16th are free.\n\n- **Porto**: river walk, Serralves, dinner at Cantinho do Avillez\n- Hotels from €120/night near Ribeira\n\nWant me to book it?", store: store)
            finish(t2.id, summary: "Proposed a Porto weekend.", cents: 4, store: store)
        }

        // 3. Waiting for an approval (an edit with a line diff, then a push).
        let t3 = startTask(prompt: "Fix the typo in the README and push it", agentID: "assistant", store: store, ago: 60)
        tool(t3.id, "Read", input: ["file_path": "/Users/me/src/nexus/README.md"], output: #"{"content":"lines 1-40"}"#, store: store)
        output(t3.id, "Found it: “recieve” on line 12. I'll fix it and push to `main`.", final: false, store: store)
        pair("Fix the typo in the README and push it", taskID: t3.id, ago: 60, firstToken: 1420)
        let a3 = requestApproval(
            t3.id,
            title: "Athena · Assistant wants to use Edit",
            actionClass: .compute,
            level: .approve,
            payload: [
                "tool": "Edit",
                "cwd": "/Users/me/src/nexus",
                "input": [
                    "file_path": "/Users/me/src/nexus/README.md",
                    "old_string": "Nexus lets you recieve results\non your phone.",
                    "new_string": "Nexus lets you receive results\non your phone.",
                ],
            ],
            store: store
        )
        Task { @MainActor in
            await continueCodeFix(t3.id, editApproval: a3, store: store)
        }
        return messages
    }

    // MARK: Scripts

    private static func script(for prompt: String, taskID: String, store: NexusStore) async {
        let p = prompt.lowercased()
        try? await Task.sleep(for: .milliseconds(500))
        announce(taskID, store: store)
        if ["buy", "order", "grocer", "pay", "checkout"].contains(where: { p.contains($0) }) {
            await shopping(taskID, store: store)
        } else if ["fix", "push", "commit", "deploy", "edit"].contains(where: { p.contains($0) }) {
            tool(taskID, "Read", input: ["file_path": "/Users/me/src/nexus/README.md"], output: #"{"content":"lines 1-40"}"#, store: store)
            await say(taskID, "Found it: “recieve” on line 12. I'll fix it and push to `main`.", store: store)
            let id = requestApproval(
                taskID,
                title: "Athena · Assistant wants to use Edit",
                actionClass: .compute,
                level: .approve,
                payload: [
                    "tool": "Edit",
                    "cwd": "/Users/me/src/nexus",
                    "input": [
                        "file_path": "/Users/me/src/nexus/README.md",
                        "old_string": "Nexus lets you recieve results\non your phone.",
                        "new_string": "Nexus lets you receive results\non your phone.",
                    ],
                ],
                store: store
            )
            await continueCodeFix(taskID, editApproval: id, store: store)
        } else if ["trip", "weekend", "plan a", "choose", "pick", "question"].contains(where: { p.contains($0) }) {
            await say(taskID, "Sure. Which city should I plan for?", store: store)
            let q = askQuestion(taskID, header: "City", text: "Where should I look?", options: [
                ("Porto", "3 h by train, great food"),
                ("Lisbon", "Museums and nightlife"),
                ("Algarve", "Beaches, quieter in autumn"),
            ], multiSelect: false, store: store)
            guard let answer = await waitForAnswer(q, taskID: taskID, store: store) else { return }
            setState(taskID, .running, store: store)
            await say(taskID, "\n\n**\(answer)** — good choice. Here's a first draft:\n\n1. Friday: arrive, dinner by the river\n2. Saturday: old town walk, museum, wine tasting\n3. Sunday: brunch, back by 18:00", store: store)
            finish(taskID, summary: "Drafted a weekend in \(answer).", cents: 3, store: store)
        } else if p.contains("research") || p.contains("compare") {
            await research(taskID, store: store)
        } else if p.contains("background") {
            tool(taskID, "mcp__nexus__memory_search", input: ["query": "deals"], output: #"{"notes":[]}"#, store: store)
            await say(taskID, "I've started a price check. The agent is running in the background — I'll report back with a deals table once it finishes.", store: store)
            finish(taskID, summary: "Started a price check.", cents: 2, store: store)
        } else {
            tool(taskID, "mcp__nexus__memory_search", input: ["query": ToolText.clip(prompt, 40)], output: #"{"notes":["daily/2026-10-18.md"]}"#, store: store)
            await say(taskID, "Here's what I found.\n\n### Summary\nYour vault has **2 notes** about this. The most recent is from yesterday:\n\n- You asked the plumber to come on *Tuesday*\n- The quote was **€180**, paid on completion\n\nWant me to add a reminder for Monday evening?", store: store)
            finish(taskID, summary: "Answered from the vault.", cents: 2, store: store)
        }
    }

    private static func continueCodeFix(_ taskID: String, editApproval: String, store: NexusStore) async {
        guard let approved = await waitForDecision(editApproval, taskID: taskID, store: store) else { return }
        setState(taskID, .running, store: store)
        guard approved else {
            await say(taskID, "\n\nOkay, I left the README unchanged.", store: store)
            finish(taskID, summary: "Edit denied; nothing changed.", cents: 1, store: store)
            return
        }
        tool(taskID, "Edit", input: ["file_path": "/Users/me/src/nexus/README.md"], output: #"{"ok":true}"#, store: store)
        let push = requestApproval(
            taskID,
            title: "Athena · Assistant wants to use Bash",
            actionClass: .codePush,
            level: .approve,
            payload: [
                "tool": "Bash",
                "cwd": "/Users/me/src/nexus",
                "input": ["command": "git commit -am 'Fix typo in README' && git push origin main", "description": "Commit and push the fix"],
            ],
            store: store
        )
        guard let pushed = await waitForDecision(push, taskID: taskID, store: store) else { return }
        setState(taskID, .running, store: store)
        if pushed {
            tool(taskID, "Bash", input: ["command": "git push origin main"], output: #"{"content":"main -> main"}"#, store: store)
            await say(taskID, "\n\nDone: fixed “recieve” → “receive” and pushed `9f2c1ab` to `main`.", store: store)
            finish(taskID, summary: "Fixed the README typo and pushed it.", cents: 6, store: store)
        } else {
            await say(taskID, "\n\nThe fix is committed locally; I didn't push it.", store: store)
            finish(taskID, summary: "Fixed the typo locally (not pushed).", cents: 5, store: store)
        }
    }

    private static func shopping(_ taskID: String, store: NexusStore) async {
        tool(taskID, "mcp__nexus__browser_open", input: ["url": "https://www.continente.pt"], output: #"{"ok":true}"#, store: store)
        await say(taskID, "Your usual basket is ready: 9 items, one substitution (oat milk → soy). Checking out needs your Face ID.", store: store)
        let id = requestApproval(
            taskID,
            title: "Checkout €42.90 at Continente",
            actionClass: .spend,
            level: .biometric,
            payload: [
                "tool": "mcp__nexus__browser_checkout",
                "input": ["merchant": "Continente", "amount": 42.9, "currency": "EUR", "items": 9, "slot": "Sat 09:00–11:00"] as [String: Any],
            ],
            store: store
        )
        guard let approved = await waitForDecision(id, taskID: taskID, store: store) else { return }
        setState(taskID, .running, store: store)
        if approved {
            await say(taskID, "\n\nOrdered. Delivery **Saturday 09:00–11:00**, €42.90.", store: store)
            finish(taskID, summary: "Ordered groceries (€42.90).", cents: 4, store: store)
        } else {
            await say(taskID, "\n\nNo problem — the cart is saved, nothing was paid.", store: store)
            finish(taskID, summary: "Checkout cancelled.", cents: 3, store: store)
        }
    }

    private static func research(_ taskID: String, store: NexusStore) async {
        tool(taskID, "mcp__nexus__spawn_agent", input: ["agent_id": "researcher", "prompt": "Compare the options"], output: #"{"task_id":"sub"}"#, store: store)
        var child = Nexus_V1_Task()
        child.id = taskID + "_sub"
        child.parentID = taskID
        child.rootID = taskID
        child.title = "Compare the options"
        child.prompt = child.title
        child.agentID = "researcher"
        child.state = .running
        child.source = "spawn"
        child.createdAt = Google_Protobuf_Timestamp(date: Date())
        child.updatedAt = child.createdAt
        emit(child.id, store) { $0.taskUpdated.task = child }
        await say(taskID, "I asked the Researcher to compare them; I'll wait for its result.", store: store)
        var parent = store.tasks[taskID] ?? Nexus_V1_Task()
        parent.phase = "waiting_children"
        emit(taskID, store) { $0.taskUpdated.task = parent }
        try? await Task.sleep(for: .seconds(6))
        guard !cancelled(taskID, store: store) else { return }
        child.state = .succeeded
        child.summary = "Daikin Altherma 3 has the best seasonal COP."
        emit(child.id, store) { $0.taskUpdated.task = child }
        parent = store.tasks[taskID] ?? parent
        parent.phase = ""
        emit(taskID, store) { $0.taskUpdated.task = parent }
        await say(taskID, "\n\nThe Researcher is done: **Daikin Altherma 3** wins on seasonal COP; Mitsubishi Ecodan is quieter but ~8% pricier.", store: store)
        finish(taskID, summary: "Compared the options.", cents: 9, store: store)
    }

    static let capabilities = """
    I'm your Nexus assistant. I can:

    - **Answer** from your vault and the web
    - **Hand work to specialists** — write `@researcher` or `@code-fixer` before a message
    - **Search** with `/search heat pumps`, **save notes** with `remember …`
    - **Ask you** before anything risky: edits, pushes and payments need your approval (Face ID for spending)

    Right now 2 tasks are running on *mac-studio*.
    """

    // MARK: Event helpers

    private static func emit(_ taskID: String, _ store: NexusStore, _ fill: (inout Nexus_V1_Event) -> Void) {
        seq += 1
        var event = Nexus_V1_Event()
        event.seq = seq
        event.at = Google_Protobuf_Timestamp(date: Date())
        event.taskID = taskID
        event.runID = "run_" + taskID
        fill(&event)
        store.applyDemo(event)
    }

    @discardableResult
    private static func startTask(prompt: String, agentID: String, store: NexusStore, ago: TimeInterval = 0) -> Nexus_V1_Task {
        var task = Nexus_V1_Task()
        task.id = "tsk_chat_" + ChatStore.newID()
        task.title = ToolText.clip(prompt, 60)
        task.prompt = prompt
        task.agentID = agentID
        task.state = .running
        task.source = "chat"
        task.nodeID = "node_mac"
        task.currentRunID = "run_" + task.id
        task.rootID = task.id
        task.createdAt = Google_Protobuf_Timestamp(date: Date().addingTimeInterval(-ago))
        task.updatedAt = task.createdAt
        emit(task.id, store) { $0.taskUpdated.task = task }
        if ago > 0 { announce(task.id, store: store) }
        return task
    }

    /// The zero cost event that names the engine and exact model at the start of a run.
    private static func announce(_ taskID: String, store: NexusStore) {
        emit(taskID, store) { event in
            var cost = Nexus_V1_Cost()
            cost.runtime = "claude-code"
            cost.model = model
            event.cost.cost = cost
        }
    }

    private static func setState(_ taskID: String, _ state: Nexus_V1_TaskState, store: NexusStore) {
        guard var task = store.tasks[taskID], task.state != state else { return }
        task.state = state
        task.updatedAt = Google_Protobuf_Timestamp(date: Date())
        emit(taskID, store) { $0.taskUpdated.task = task }
    }

    private static func output(_ taskID: String, _ text: String, final: Bool, store: NexusStore) {
        emit(taskID, store) { event in
            event.runOutput.text = text
            event.runOutput.isFinal = final
        }
    }

    private static func cancelled(_ taskID: String, store: NexusStore) -> Bool {
        store.tasks[taskID]?.state.isTerminal ?? true
    }

    /// Streams `text` in small deltas every 50 ms, like the claude-code runtime.
    private static func say(_ taskID: String, _ text: String, store: NexusStore) async {
        var chunk = ""
        var words = 0
        for character in text {
            chunk.append(character)
            if character == " " || character == "\n" { words += 1 }
            if words >= 3 {
                guard !cancelled(taskID, store: store) else { return }
                output(taskID, chunk, final: false, store: store)
                chunk = ""
                words = 0
                try? await Task.sleep(for: .milliseconds(50))
            }
        }
        if !chunk.isEmpty, !cancelled(taskID, store: store) { output(taskID, chunk, final: false, store: store) }
    }

    private static func tool(_ taskID: String, _ name: String, input: [String: Any], output: String, store: NexusStore) {
        let callID = "call_" + ChatStore.newID()
        var call = Nexus_V1_ToolCall()
        call.callID = callID
        call.tool = name
        call.input = structFrom(input)
        call.actionClass = .read
        emit(taskID, store) { $0.toolCall = call }
        var result = Nexus_V1_ToolResult()
        result.callID = callID
        result.output = (try? Google_Protobuf_Struct(jsonString: output)) ?? Google_Protobuf_Struct()
        result.durationMs = 640
        emit(taskID, store) { $0.toolResult = result }
    }

    private static func askQuestion(_ taskID: String, header: String, text: String, options: [(String, String)], multiSelect: Bool, store: NexusStore) -> String {
        setState(taskID, .waitingInput, store: store)
        var question = Nexus_V1_Question()
        question.questionID = "q_" + ChatStore.newID()
        question.header = header
        question.text = text
        question.choices = options.map(\.0)
        question.options = options.map { label, description in
            var option = Nexus_V1_QuestionOption()
            option.label = label
            option.description_p = description
            return option
        }
        question.multiSelect = multiSelect
        question.allowFreeText = true
        emit(taskID, store) { $0.question = question }
        return question.questionID
    }

    private static func requestApproval(
        _ taskID: String,
        title: String,
        actionClass: Nexus_V1_ActionClass,
        level: Nexus_V1_TrustLevel,
        payload: [String: Any],
        store: NexusStore
    ) -> String {
        setState(taskID, .waitingApproval, store: store)
        var approval = Nexus_V1_Approval()
        approval.id = "apr_chat_" + ChatStore.newID()
        approval.taskID = taskID
        approval.runID = "run_" + taskID
        approval.agentID = "assistant"
        approval.actionClass = actionClass
        approval.level = level
        approval.title = title
        approval.payload = structFrom(payload)
        approval.payloadHash = (try? CanonicalJSON.actionHash(of: approval.payload)) ?? Data()
        approval.challenge = Data(repeating: 0x03, count: 32)
        approval.state = .pending
        approval.createdAt = Google_Protobuf_Timestamp(date: Date())
        approval.expiresAt = Google_Protobuf_Timestamp(date: Date().addingTimeInterval(3600))
        approval.nodeID = "node_mac"
        emit(taskID, store) { $0.approval = approval }
        return approval.id
    }

    private static func finish(_ taskID: String, summary: String, cents: Int64, store: NexusStore) {
        output(taskID, "", final: true, store: store)
        var cost = Nexus_V1_Cost()
        cost.runtime = "claude-code"
        cost.model = model
        cost.inputTokens = 12_400
        cost.outputTokens = 860
        var money = Nexus_V1_Money()
        money.currency = "EUR"
        money.minorUnits = cents
        cost.amount = money
        emit(taskID, store) { $0.cost.cost = cost }
        guard var task = store.tasks[taskID], !task.state.isTerminal else { return }
        task.state = .succeeded
        task.summary = summary
        task.cost = cost
        task.updatedAt = Google_Protobuf_Timestamp(date: Date())
        emit(taskID, store) { $0.taskUpdated.task = task }
    }

    /// Waits until the question is answered (inline, in the Inbox or from a notification).
    private static func waitForAnswer(_ questionID: String, taskID: String, store: NexusStore) async -> String? {
        while !Task.isCancelled {
            if cancelled(taskID, store: store) { return nil }
            if store.questions[questionID] == nil {
                let answer = (store.timelines[taskID] ?? []).reversed().lazy.compactMap { entry -> String? in
                    if case .userMessage(let text) = entry.kind { return text }
                    return nil
                }.first
                return answer ?? "your choice"
            }
            try? await Task.sleep(for: .milliseconds(250))
        }
        return nil
    }

    /// Waits for the decision on an approval (true = approved).
    private static func waitForDecision(_ approvalID: String, taskID: String, store: NexusStore) async -> Bool? {
        while !Task.isCancelled {
            if cancelled(taskID, store: store) { return nil }
            for entry in (store.timelines[taskID] ?? []).reversed() {
                if case .approval(let approval) = entry.kind, approval.id == approvalID, approval.state != .pending {
                    return approval.state == .approved
                }
            }
            try? await Task.sleep(for: .milliseconds(250))
        }
        return nil
    }

    private static func structFrom(_ object: [String: Any]) -> Google_Protobuf_Struct {
        guard JSONSerialization.isValidJSONObject(object),
              let data = try? JSONSerialization.data(withJSONObject: object),
              let json = String(data: data, encoding: .utf8),
              let value = try? Google_Protobuf_Struct(jsonString: json) else { return Google_Protobuf_Struct() }
        return value
    }
}
