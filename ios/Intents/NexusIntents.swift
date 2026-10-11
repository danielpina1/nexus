import AppIntents
import Foundation
import NexusCore
import WidgetKit

// App Intents shared by the app (Siri, Shortcuts, Action Button) and the widget extension
// (interactive widgets, Live Activity buttons, Control Center controls).
// This file is compiled into BOTH targets. Every network call goes to the Hub API only.

/// Errors surfaced to Siri / Shortcuts.
enum NexusIntentError: Error, CustomLocalizedStringResourceConvertible {
    case notPaired
    case demoMode
    case needsFaceID
    case failed(String)

    var localizedStringResource: LocalizedStringResource {
        switch self {
        case .notPaired: return "Nexus is not paired with a Hub yet. Open the app to pair."
        case .demoMode: return "Nexus is in demo mode."
        case .needsFaceID: return "This approval needs Face ID. Open Nexus to review it."
        case .failed(let message): return "Nexus: \(message)"
        }
    }
}

enum IntentSupport {
    static func actions() throws -> NexusActions {
        if AppGroup.defaults.bool(forKey: AppGroup.Key.demoMode) { throw NexusIntentError.demoMode }
        guard let actions = NexusActions.shared() else { throw NexusIntentError.notPaired }
        return actions
    }

    static func wrap<T>(_ body: () async throws -> T) async throws -> T {
        do {
            return try await body()
        } catch let error as NexusIntentError {
            throw error
        } catch NexusActions.DecideError.requiresBiometric {
            throw NexusIntentError.needsFaceID
        } catch {
            throw NexusIntentError.failed(error.localizedDescription)
        }
    }

    /// Wakes the app UI for actions that must finish there (kill switch confirmation, capture).
    static func requestInApp(_ action: PendingAction) {
        PendingAction.set(action)
        NotificationCenter.default.post(name: .nexusPendingAction, object: nil)
    }
}

extension Notification.Name {
    static let nexusPendingAction = Notification.Name("NexusPendingAction")
}

// MARK: - Siri / Shortcuts

struct AskNexusIntent: AppIntent {
    static var title: LocalizedStringResource = "Ask Nexus"
    static var description = IntentDescription("Send a request to Nexus. It is routed to the right agent or answered from your vault.")
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Request", requestValueDialog: "What should Nexus do?")
    var request: String

    init() {}

    init(request: String) {
        self.request = request
    }

    func perform() async throws -> some IntentResult & ReturnsValue<String> & ProvidesDialog {
        let actions = try IntentSupport.actions()
        let response = try await IntentSupport.wrap {
            try await actions.execute(request, source: "siri")
        }
        let reply: String
        if !response.answer.isEmpty {
            reply = response.answer
        } else if !response.question.isEmpty {
            let choices = response.choices.isEmpty ? "" : " (" + response.choices.joined(separator: ", ") + ")"
            reply = response.question + choices
        } else if response.hasTask {
            reply = "On it: \(response.task.displayTitle)."
        } else {
            reply = "Done."
        }
        return .result(value: reply, dialog: "\(reply)")
    }
}

struct RunWorkflowIntent: AppIntent {
    static var title: LocalizedStringResource = "Run Workflow"
    static var description = IntentDescription("Start a Nexus agent or workflow.")
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Agent or workflow ID")
    var targetID: String

    @Parameter(title: "Name", default: "")
    var name: String

    @Parameter(title: "Instructions", default: "")
    var prompt: String

    init() {}

    init(targetID: String, name: String, prompt: String) {
        self.targetID = targetID
        self.name = name
        self.prompt = prompt
    }

    func perform() async throws -> some IntentResult & ProvidesDialog {
        let actions = try IntentSupport.actions()
        let input = prompt.isEmpty ? "Run \(name.isEmpty ? targetID : name)" : prompt
        let response = try await IntentSupport.wrap {
            try await actions.execute(input, agentID: targetID, source: "widget")
        }
        WidgetCenter.shared.reloadAllTimelines()
        let title = response.hasTask ? response.task.displayTitle : input
        return .result(dialog: "Started \(title).")
    }
}

struct CaptureIntent: AppIntent {
    static var title: LocalizedStringResource = "Capture to Nexus"
    static var description = IntentDescription("Save a thought, link or snippet to your Nexus vault.")
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Text", requestValueDialog: "What should I capture?")
    var text: String

    init() {}

    func perform() async throws -> some IntentResult & ProvidesDialog {
        let actions = try IntentSupport.actions()
        let note = try await IntentSupport.wrap {
            try await actions.capture(text, source: "siri")
        }
        let title = note.title.isEmpty ? "your vault" : note.title
        return .result(dialog: "Saved to \(title).")
    }
}

// MARK: - Live Activity / widget buttons

/// Normal-level (`approve`) approvals from Live Activity, widget or notification buttons.
/// Biometric approvals never go through here — they deep-link to the in-app sheet.
struct ApproveIntent: LiveActivityIntent {
    static var title: LocalizedStringResource = "Approve"
    static var description = IntentDescription("Approve or deny a pending Nexus action.")
    static var isDiscoverable: Bool = false

    @Parameter(title: "Approval ID")
    var approvalID: String

    @Parameter(title: "Approve", default: true)
    var approve: Bool

    init() {}

    init(approvalID: String, approve: Bool) {
        self.approvalID = approvalID
        self.approve = approve
    }

    func perform() async throws -> some IntentResult {
        let actions = try IntentSupport.actions()
        _ = try await IntentSupport.wrap {
            try await actions.decide(approvalID: approvalID, approve: approve, allowBiometric: false)
        }
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}

struct StopTaskIntent: LiveActivityIntent {
    static var title: LocalizedStringResource = "Stop Task"
    static var isDiscoverable: Bool = false

    @Parameter(title: "Task ID")
    var taskID: String

    init() {}

    init(taskID: String) {
        self.taskID = taskID
    }

    func perform() async throws -> some IntentResult {
        let actions = try IntentSupport.actions()
        try await IntentSupport.wrap {
            try await actions.cancel(taskID: taskID)
        }
        return .result()
    }
}

struct TryHarderIntent: LiveActivityIntent {
    static var title: LocalizedStringResource = "Try Harder"
    static var isDiscoverable: Bool = false

    @Parameter(title: "Task ID")
    var taskID: String

    init() {}

    init(taskID: String) {
        self.taskID = taskID
    }

    func perform() async throws -> some IntentResult {
        let actions = try IntentSupport.actions()
        _ = try await IntentSupport.wrap {
            try await actions.escalate(taskID: taskID)
        }
        return .result()
    }
}

/// Answers a simple question from a Live Activity choice button.
struct AnswerChoiceIntent: LiveActivityIntent {
    static var title: LocalizedStringResource = "Answer"
    static var isDiscoverable: Bool = false

    @Parameter(title: "Task ID")
    var taskID: String

    @Parameter(title: "Choice")
    var choice: String

    init() {}

    init(taskID: String, choice: String) {
        self.taskID = taskID
        self.choice = choice
    }

    func perform() async throws -> some IntentResult {
        let actions = try IntentSupport.actions()
        try await IntentSupport.wrap {
            try await actions.sendMessage(taskID: taskID, text: choice, choice: choice)
        }
        return .result()
    }
}

// MARK: - Kill switch / controls

/// Kill switch: opens Nexus on a confirmation dialog (a destructive, global action should
/// never fire from a single accidental tap).
struct KillSwitchIntent: AppIntent {
    static var title: LocalizedStringResource = "Nexus Kill Switch"
    static var description = IntentDescription("Stop all agents and pause all triggers (asks for confirmation).")
    static var openAppWhenRun: Bool = true

    init() {}

    @MainActor
    func perform() async throws -> some IntentResult {
        IntentSupport.requestInApp(.killSwitch)
        return .result()
    }
}

/// Opens the quick-capture sheet (Control Center / Action Button).
struct QuickCaptureIntent: AppIntent {
    static var title: LocalizedStringResource = "Quick Capture"
    static var description = IntentDescription("Open Nexus quick capture.")
    static var openAppWhenRun: Bool = true

    init() {}

    @MainActor
    func perform() async throws -> some IntentResult {
        IntentSupport.requestInApp(.capture)
        return .result()
    }
}

/// Opens Nexus on the command composer ("Talk to Nexus").
struct OpenComposerIntent: AppIntent {
    static var title: LocalizedStringResource = "Talk to Nexus"
    static var openAppWhenRun: Bool = true

    init() {}

    @MainActor
    func perform() async throws -> some IntentResult {
        IntentSupport.requestInApp(.askNexus)
        return .result()
    }
}
