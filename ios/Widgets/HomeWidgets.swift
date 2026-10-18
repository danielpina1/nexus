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
                .containerBackground(NXColor.surface, for: .widget)
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
        VStack(alignment: .leading, spacing: NXSpace.s1) {
            WidgetHeader(title: "Needs you", systemImage: "tray", trailing: family == .systemMedium ? PulseFormat.cost(snapshot.costCentsToday) + " today" : nil)
            Text("\(snapshot.inboxCount)")
                .font(NXTextStyle(family: .heading, weight: .semibold, size: 40, relativeTo: .largeTitle, lineSpacing: 0, tracking: 0).font)
                .monospacedDigit()
                .foregroundStyle(NXColor.fg)
                .contentTransition(.numericText())
                .accessibilityLabel("\(snapshot.inboxCount) need you")
            if snapshot.pendingApprovals > 0 {
                NXBadge("\(snapshot.pendingApprovals) approval\(snapshot.pendingApprovals == 1 ? "" : "s")", systemImage: "hand.raised", tone: .warn)
            } else {
                Text(snapshot.inboxCount == 0 ? "Inbox zero" : "inbox items")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            if family == .systemMedium {
                NXDivider().padding(.vertical, 2)
                if snapshot.running.isEmpty {
                    Text("Nothing running")
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
                ForEach(snapshot.running.prefix(2)) { task in
                    Link(destination: DeepLink.task(task.id)) {
                        HStack(spacing: 6) {
                            Image(systemName: task.waitingForYou ? "hand.raised" : task.agentSymbol)
                                .foregroundStyle(task.waitingForYou ? NXColor.warn : NXColor.info)
                                .accessibilityHidden(true)
                            Text(task.title)
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.fg)
                                .lineLimit(1)
                        }
                    }
                    .accessibilityLabel(task.title + (task.waitingForYou ? ", waiting for you" : ", running"))
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
                .containerBackground(NXColor.surface, for: .widget)
                .widgetURL(DeepLink.machines)
        }
        .configurationDisplayName("Machines")
        .description("Your nodes at a glance.")
        .supportedFamilies([.systemSmall, .systemMedium])
    }
}

struct MachinesWidgetView: View {
    @Environment(\.widgetFamily) private var family
    let snapshot: WidgetSnapshot

    var body: some View {
        let online = snapshot.machines.filter(\.online).count
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            WidgetHeader(title: "Machines", systemImage: "cpu", trailing: snapshot.machines.isEmpty ? nil : "\(online)/\(snapshot.machines.count)")
            if snapshot.machines.isEmpty {
                Text("No machines yet")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            ForEach(snapshot.machines.prefix(family == .systemSmall ? 3 : 4)) { machine in
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Circle()
                            .fill(machine.online ? NXColor.ok : NXColor.muted)
                            .frame(width: 7, height: 7)
                            .accessibilityHidden(true)
                        Text(machine.name)
                            .nx(NXType.footnote.weight(.medium))
                            .foregroundStyle(NXColor.fg)
                            .lineLimit(1)
                        Spacer(minLength: 0)
                        if machine.online {
                            if machine.activeRuns > 0 {
                                Text("\(machine.activeRuns) running")
                                    .nx(NXType.caption.weight(.regular))
                                    .foregroundStyle(NXColor.info)
                            }
                            Text("\(Int(machine.cpuPercent))%")
                                .nx(NXType.caption)
                                .monospacedDigit()
                                .foregroundStyle(NXColor.fg2)
                        } else {
                            Text("offline")
                                .nx(NXType.caption.weight(.regular))
                                .foregroundStyle(NXColor.muted)
                        }
                    }
                    if machine.online && family == .systemMedium {
                        NXMeter(value: machine.cpuPercent / 100, label: "CPU", height: 4)
                    }
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(machine.name), " + (machine.online ? "online, CPU \(Int(machine.cpuPercent)) percent, \(machine.activeRuns) running" : "offline"))
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
                .containerBackground(NXColor.surface, for: .widget)
        }
        .configurationDisplayName("Favourites")
        .description("Run your favourite agents and workflows with one tap.")
        .supportedFamilies([.systemMedium, .systemLarge])
    }
}

struct FavouritesWidgetView: View {
    let snapshot: WidgetSnapshot

    private let columns = [GridItem(.flexible(), spacing: NXSpace.s2), GridItem(.flexible(), spacing: NXSpace.s2)]

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            WidgetHeader(title: "Run", systemImage: "bolt", trailing: nil)
            if snapshot.favourites.isEmpty {
                Text("Open Nexus to load your agents.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            LazyVGrid(columns: columns, spacing: NXSpace.s2) {
                ForEach(snapshot.favourites.prefix(4)) { workflow in
                    Button(intent: RunWorkflowIntent(targetID: workflow.id, name: workflow.name, prompt: workflow.prompt)) {
                        WidgetPill(title: workflow.name, systemImage: workflow.symbol)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Run \(workflow.name)")
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

/// Accessory widgets are tinted by the system (vibrant), so they use type and symbols only.
struct LockScreenWidgetView: View {
    @Environment(\.widgetFamily) private var family
    let snapshot: WidgetSnapshot

    var body: some View {
        switch family {
        case .accessoryCircular:
            ZStack {
                AccessoryWidgetBackground()
                VStack(spacing: 0) {
                    Image(systemName: snapshot.pendingApprovals > 0 ? "hand.raised" : "arrow.triangle.2.circlepath")
                        .accessibilityHidden(true)
                    Text("\(snapshot.pendingApprovals > 0 ? snapshot.pendingApprovals : snapshot.runningCount)")
                        .nx(NXType.title3)
                        .monospacedDigit()
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(snapshot.pendingApprovals > 0 ? "\(snapshot.pendingApprovals) to approve" : "\(snapshot.runningCount) running")
        case .accessoryInline:
            Label("\(snapshot.runningCount) running · \(snapshot.pendingApprovals) to approve", systemImage: "circle.hexagongrid.fill")
        default:
            VStack(alignment: .leading, spacing: 2) {
                Label("Nexus", systemImage: "circle.hexagongrid.fill")
                    .nxText(NXType.title3)
                Text("\(snapshot.pendingApprovals) to approve · \(snapshot.openQuestions) question\(snapshot.openQuestions == 1 ? "" : "s")")
                    .nx(NXType.footnote)
                Text("\(snapshot.runningCount) running")
                    .nx(NXType.footnote)
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
