import ActivityKit
import NexusCore
import SwiftUI
import WidgetKit

/// Aggregate "Pulse" activity: one activity instead of many
/// ("3 running · 1 waiting for you · €0.42 today"). Also the StandBy mission-control view.
struct PulseLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: PulseActivityAttributes.self) { context in
            PulseLockScreenView(state: context.state, hubName: context.attributes.hubName)
                .widgetURL(DeepLink.inbox)
        } dynamicIsland: { context in
            let state = context.state
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Label("\(state.running) running", systemImage: "play.circle.fill")
                        .font(.caption)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text(PulseFormat.cost(state.costCentsToday))
                        .font(.caption.monospacedDigit())
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: 4) {
                        if state.waiting > 0 {
                            Label("\(state.waiting) waiting for you", systemImage: "hand.raised.fill")
                                .foregroundStyle(.orange)
                        }
                        if let headline = state.headline {
                            Label(headline, systemImage: state.headlineSymbol ?? "sparkles")
                                .lineLimit(1)
                        }
                    }
                    .font(.caption)
                }
            } compactLeading: {
                Label("\(state.running)", systemImage: "play.circle.fill")
                    .labelStyle(.titleAndIcon)
                    .font(.caption2)
            } compactTrailing: {
                if state.waiting > 0 {
                    Label("\(state.waiting)", systemImage: "hand.raised.fill")
                        .labelStyle(.titleAndIcon)
                        .font(.caption2)
                        .foregroundStyle(.orange)
                } else {
                    Text(PulseFormat.cost(state.costCentsToday)).font(.caption2.monospacedDigit())
                }
            } minimal: {
                Image(systemName: state.waiting > 0 ? "hand.raised.fill" : "circle.hexagongrid.fill")
                    .foregroundStyle(state.waiting > 0 ? Color.orange : Color.blue)
            }
            .widgetURL(DeepLink.inbox)
        }
    }
}

enum PulseFormat {
    static func cost(_ cents: Int) -> String {
        (Decimal(cents) / 100).formatted(.currency(code: "EUR"))
    }
}

struct PulseLockScreenView: View {
    let state: PulseActivityAttributes.ContentState
    let hubName: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "circle.hexagongrid.fill").foregroundStyle(.blue)
                Text("Nexus").font(.headline)
                Text("· \(hubName)").font(.subheadline).foregroundStyle(.secondary)
                Spacer()
                Text(PulseFormat.cost(state.costCentsToday) + " today")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            HStack(spacing: 12) {
                Label("\(state.running) running", systemImage: "play.circle.fill")
                Label("\(state.waiting) waiting for you", systemImage: "hand.raised.fill")
                    .foregroundStyle(state.waiting > 0 ? Color.orange : Color.secondary)
            }
            .font(.subheadline)
            if let headline = state.headline {
                Label(headline, systemImage: state.headlineSymbol ?? "sparkles")
                    .font(.caption)
                    .lineLimit(1)
            }
        }
        .padding()
    }
}
