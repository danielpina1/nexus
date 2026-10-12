import AppIntents
import NexusCore
import SwiftUI
import WidgetKit

// Home Screen and Lock Screen widgets. They render the `WidgetSnapshot` the app writes to the
// App Group (no network from timeline providers); interactive buttons run App Intents.

struct SnapshotEntry: TimelineEntry {
    let date: Date
    let snapshot: WidgetSnapshot
}

struct SnapshotProvider: TimelineProvider {
    func placeholder(in context: Context) -> SnapshotEntry {
        SnapshotEntry(date: Date(), snapshot: .placeholder)
    }

    func getSnapshot(in context: Context, completion: @escaping (SnapshotEntry) -> Void) {
        let snapshot = context.isPreview ? WidgetSnapshot.placeholder : (WidgetSnapshot.load() ?? WidgetSnapshot())
        completion(SnapshotEntry(date: Date(), snapshot: snapshot))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<SnapshotEntry>) -> Void) {
        let entry = SnapshotEntry(date: Date(), snapshot: WidgetSnapshot.load() ?? WidgetSnapshot())
        // The app reloads timelines when the snapshot changes; this is only a safety net.
        let next = Calendar.current.date(byAdding: .minute, value: 15, to: Date()) ?? Date().addingTimeInterval(900)
        completion(Timeline(entries: [entry], policy: .after(next)))
    }
}

enum WidgetKinds {
    static let inbox = "NexusInboxWidget"
    static let machines = "NexusMachinesWidget"
    static let favourites = "NexusFavouritesWidget"
    static let lockScreen = "NexusLockScreenWidget"
}

// MARK: - Inbox

struct InboxWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: WidgetKinds.inbox, provider: SnapshotProvider()) { entry in
            InboxWidgetView(snapshot: entry.snapshot)
                .containerBackground(.fill.tertiary, for: .widget)
                .widgetURL(DeepLink.inbox)
        }
        .configurationDisplayName("Inbox")
        .description("Approvals, questions and running tasks.")
        .supportedFamilies([.systemSmall, .systemMedium])
    }
}

struct InboxWidgetView: View {
    @Environment(\.widgetFamily) private var family
    let snapshot: WidgetSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "tray.full.fill").foregroundStyle(.tint)
                Text("Nexus").font(.headline)
                Spacer()
                Text(PulseFormat.cost(snapshot.costCentsToday)).font(.caption2).foregroundStyle(.secondary)
            }
            Text("\(snapshot.inboxCount)")
                .font(.system(size: 40, weight: .bold, design: .rounded))
                .contentTransition(.numericText())
            Text(snapshot.pendingApprovals > 0 ? "\(snapshot.pendingApprovals) need approval" : "inbox items")
                .font(.caption)
                .foregroundStyle(snapshot.pendingApprovals > 0 ? Color.orange : Color.secondary)
            if family == .systemMedium {
                Divider()
                ForEach(snapshot.running.prefix(2)) { task in
                    Link(destination: DeepLink.task(task.id)) {
                        Label(task.title, systemImage: task.waitingForYou ? "hand.raised.fill" : task.agentSymbol)
                            .font(.caption)
                            .lineLimit(1)
                    }
                }
            }
            Spacer(minLength: 0)
        }
    }
}

// MARK: - Machines

struct MachinesWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: WidgetKinds.machines, provider: SnapshotProvider()) { entry in
            MachinesWidgetView(snapshot: entry.snapshot)
                .containerBackground(.fill.tertiary, for: .widget)
                .widgetURL(DeepLink.machines)
        }
        .configurationDisplayName("Machines")
        .description("Your nodes at a glance.")
        .supportedFamilies([.systemSmall, .systemMedium])
    }
}

struct MachinesWidgetView: View {
    let snapshot: WidgetSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Machines", systemImage: "desktopcomputer").font(.headline)
            if snapshot.machines.isEmpty {
                Text("No machines yet").font(.caption).foregroundStyle(.secondary)
            }
            ForEach(snapshot.machines.prefix(4)) { machine in
                HStack(spacing: 6) {
                    Circle()
                        .fill(machine.online ? Color.green : Color.gray)
                        .frame(width: 8, height: 8)
                    Text(machine.name).font(.caption).lineLimit(1)
                    Spacer()
                    if machine.online {
                        Text("\(Int(machine.cpuPercent))%").font(.caption2.monospacedDigit()).foregroundStyle(.secondary)
                        if machine.activeRuns > 0 {
                            Text("▶︎\(machine.activeRuns)").font(.caption2)
                        }
                    }
                }
            }
            Spacer(minLength: 0)
        }
    }
}

// MARK: - Favourite workflows (interactive)

struct FavouritesWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: WidgetKinds.favourites, provider: SnapshotProvider()) { entry in
            FavouritesWidgetView(snapshot: entry.snapshot)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Favourites")
        .description("Run your favourite agents and workflows with one tap.")
        .supportedFamilies([.systemMedium, .systemLarge])
    }
}

struct FavouritesWidgetView: View {
    let snapshot: WidgetSnapshot

    private let columns = [GridItem(.flexible()), GridItem(.flexible())]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Run", systemImage: "bolt.fill").font(.headline)
            if snapshot.favourites.isEmpty {
                Text("Open Nexus to load your agents.").font(.caption).foregroundStyle(.secondary)
            }
            LazyVGrid(columns: columns, spacing: 8) {
                ForEach(snapshot.favourites.prefix(4)) { workflow in
                    Button(intent: RunWorkflowIntent(targetID: workflow.id, name: workflow.name, prompt: workflow.prompt)) {
                        Label(workflow.name, systemImage: workflow.symbol)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                }
            }
            Spacer(minLength: 0)
        }
    }
}

// MARK: - Lock Screen accessories

struct LockScreenWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: WidgetKinds.lockScreen, provider: SnapshotProvider()) { entry in
            LockScreenWidgetView(snapshot: entry.snapshot)
                .containerBackground(.clear, for: .widget)
                .widgetURL(DeepLink.inbox)
        }
        .configurationDisplayName("Nexus status")
        .description("Pending approvals and running tasks.")
        .supportedFamilies([.accessoryCircular, .accessoryRectangular, .accessoryInline])
    }
}

struct LockScreenWidgetView: View {
    @Environment(\.widgetFamily) private var family
    let snapshot: WidgetSnapshot

    var body: some View {
        switch family {
        case .accessoryCircular:
            ZStack {
                AccessoryWidgetBackground()
                VStack(spacing: 0) {
                    Image(systemName: snapshot.pendingApprovals > 0 ? "hand.raised.fill" : "play.circle.fill")
                    Text("\(snapshot.pendingApprovals > 0 ? snapshot.pendingApprovals : snapshot.runningCount)")
                        .font(.headline)
                }
            }
        case .accessoryInline:
            Label("\(snapshot.runningCount) running · \(snapshot.pendingApprovals) to approve", systemImage: "circle.hexagongrid.fill")
        default:
            VStack(alignment: .leading, spacing: 2) {
                Label("Nexus", systemImage: "circle.hexagongrid.fill").font(.headline)
                Text("\(snapshot.pendingApprovals) to approve · \(snapshot.openQuestions) question\(snapshot.openQuestions == 1 ? "" : "s")")
                    .font(.caption)
                Text("\(snapshot.runningCount) running").font(.caption)
            }
            .widgetAccentable()
        }
    }
}

#Preview(as: .systemMedium) {
    FavouritesWidget()
} timeline: {
    SnapshotEntry(date: .now, snapshot: .placeholder)
}
