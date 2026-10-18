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
                .activityBackgroundTint(NXColor.surface)
                .activitySystemActionForegroundColor(NXColor.fg)
                .widgetURL(DeepLink.inbox)
        } dynamicIsland: { context in
            let state = context.state
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Label("\(state.running) running", systemImage: "arrow.triangle.2.circlepath")
                        .nxText(NXType.caption)
                        .foregroundStyle(NXColor.info)
                        .environment(\.colorScheme, .dark)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text(PulseFormat.cost(state.costCentsToday))
                        .nx(NXType.caption)
                        .monospacedDigit()
                        .foregroundStyle(NXColor.fg2)
                        .environment(\.colorScheme, .dark)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: 6) {
                        if state.waiting > 0 {
                            NXBadge("\(state.waiting) waiting for you", systemImage: "hand.raised", tone: .warn)
                        }
                        if let headline = state.headline {
                            Label(headline, systemImage: state.headlineSymbol ?? "sparkles")
                                .nxText(NXType.footnote)
                                .foregroundStyle(NXColor.fg)
                                .lineLimit(1)
                        }
                    }
                    .environment(\.colorScheme, .dark)
                }
            } compactLeading: {
                Label("\(state.running)", systemImage: "arrow.triangle.2.circlepath")
                    .labelStyle(.titleAndIcon)
                    .nxText(NXType.caption)
                    .foregroundStyle(NXColor.info)
                    .environment(\.colorScheme, .dark)
                    .accessibilityLabel("\(state.running) running")
            } compactTrailing: {
                Group {
                    if state.waiting > 0 {
                        Label("\(state.waiting)", systemImage: "hand.raised")
                            .labelStyle(.titleAndIcon)
                            .nxText(NXType.caption)
                            .foregroundStyle(NXColor.warn)
                            .accessibilityLabel("\(state.waiting) waiting for you")
                    } else {
                        Text(PulseFormat.cost(state.costCentsToday))
                            .nx(NXType.caption)
                            .monospacedDigit()
                    }
                }
                .environment(\.colorScheme, .dark)
            } minimal: {
                Image(systemName: state.waiting > 0 ? "hand.raised" : "circle.hexagongrid.fill")
                    .foregroundStyle(state.waiting > 0 ? NXColor.warn : NXColor.accent)
                    .environment(\.colorScheme, .dark)
                    .accessibilityLabel(state.waiting > 0 ? "\(state.waiting) waiting for you" : "Nexus")
            }
            .widgetURL(DeepLink.inbox)
            .keylineTint(state.waiting > 0 ? NXColor.warn : NXColor.accent)
        }
    }
}

enum PulseFormat {
    static func cost(_ cents: Int) -> String {
        NXFormat.money(cents: cents)
    }
}

struct PulseLockScreenView: View {
    let state: PulseActivityAttributes.ContentState
    let hubName: String

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            WidgetHeader(title: "Nexus · \(hubName)", systemImage: "circle.hexagongrid.fill", trailing: PulseFormat.cost(state.costCentsToday) + " today")
            HStack(spacing: NXSpace.s4) {
                stat("\(state.running)", label: "running", tone: state.running > 0 ? .info : .neutral)
                stat("\(state.waiting)", label: "waiting for you", tone: state.waiting > 0 ? .warn : .neutral)
                Spacer(minLength: 0)
            }
            if let headline = state.headline {
                Label(headline, systemImage: state.headlineSymbol ?? "sparkles")
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.fg2)
                    .lineLimit(1)
            }
        }
        .padding(NXSpace.s4)
    }

    private func stat(_ value: String, label: String, tone: NXTone) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text(value)
                .nx(NXType.title2)
                .monospacedDigit()
                .foregroundStyle(tone == .neutral ? NXColor.fg : tone.color)
            Text(label)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
        }
        .accessibilityElement(children: .combine)
    }
}
