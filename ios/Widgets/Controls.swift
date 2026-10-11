import AppIntents
import SwiftUI
import WidgetKit

// iOS 18 Control Center / Lock Screen / Action Button controls.

/// Kill switch: opens Nexus on a confirmation dialog before stopping everything.
struct KillSwitchControl: ControlWidget {
    static let kind = "NexusKillSwitchControl"

    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: Self.kind) {
            ControlWidgetButton(action: KillSwitchIntent()) {
                Label("Kill switch", systemImage: "stop.circle.fill")
            }
        }
        .displayName("Nexus kill switch")
        .description("Stop all agents and pause triggers (asks to confirm).")
    }
}

/// Quick capture into the vault.
struct QuickCaptureControl: ControlWidget {
    static let kind = "NexusQuickCaptureControl"

    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: Self.kind) {
            ControlWidgetButton(action: QuickCaptureIntent()) {
                Label("Capture", systemImage: "square.and.pencil")
            }
        }
        .displayName("Nexus capture")
        .description("Capture a thought into your vault.")
    }
}

/// Talk to Nexus (opens the composer).
struct TalkToNexusControl: ControlWidget {
    static let kind = "NexusTalkControl"

    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: Self.kind) {
            ControlWidgetButton(action: OpenComposerIntent()) {
                Label("Talk to Nexus", systemImage: "waveform")
            }
        }
        .displayName("Talk to Nexus")
        .description("Open Nexus ready for a command.")
    }
}
