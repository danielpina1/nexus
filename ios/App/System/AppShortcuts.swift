import AppIntents

/// Siri / Shortcuts / Spotlight phrases. Lives only in the app target (the provider must be
/// in the main bundle); the intents themselves are in Intents/ and shared with widgets.
struct NexusShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: AskNexusIntent(),
            phrases: [
                "Ask \(.applicationName)",
                "Tell \(.applicationName) to do something",
            ],
            shortTitle: "Ask Nexus",
            systemImageName: "sparkles"
        )
        AppShortcut(
            intent: CaptureIntent(),
            phrases: [
                "Capture to \(.applicationName)",
                "Remember this in \(.applicationName)",
            ],
            shortTitle: "Capture",
            systemImageName: "square.and.pencil"
        )
        AppShortcut(
            intent: OpenComposerIntent(),
            phrases: ["Talk to \(.applicationName)"],
            shortTitle: "Talk to Nexus",
            systemImageName: "waveform"
        )
        AppShortcut(
            intent: KillSwitchIntent(),
            phrases: [
                "Stop all \(.applicationName) agents",
                "\(.applicationName) kill switch",
            ],
            shortTitle: "Kill switch",
            systemImageName: "stop.circle.fill"
        )
    }
}
