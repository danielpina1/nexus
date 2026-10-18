import NexusCore
import SwiftUI

/// Machines (web Machines): nodes with live CPU / memory / disk, what runs on them, and the
/// kill switch.
struct MachinesView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        let online = store.nodes.filter { store.status(ofNode: $0.id)?.online == true }.count
        let running = store.runningTasks.filter { !$0.nodeID.isEmpty }
        ScrollView {
            LazyVStack(alignment: .leading, spacing: NXSpace.s6) {
                NXPageHeader("Machines", subtitle: store.nodes.isEmpty ? "Nodes paired with your Hub" : "\(online) of \(store.nodes.count) nodes online · telemetry via the Hub")
                if store.nodes.isEmpty {
                    NXEmptyState(systemImage: "cpu", title: "No machines yet", message: "Install nexus-node on a machine and pair it with the Hub (`nexus-node pair`).", compact: true)
                        .nxCard(padding: 0)
                } else {
                    ForEach(store.nodes, id: \.id) { node in
                        NavigationLink(value: MachineRoute(id: node.id)) {
                            MachineCard(node: node, status: store.status(ofNode: node.id))
                        }
                        .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                    }
                }
                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Running on your machines", count: running.count, live: !running.isEmpty)
                    if running.isEmpty {
                        NXEmptyState(systemImage: "checkmark.circle", title: "Nothing running", compact: true)
                            .nxCard(padding: 0)
                    } else {
                        TaskList(tasks: running)
                    }
                }
                NXButton("Kill switch — stop everything", systemImage: "stop.circle", kind: .destructive, fullWidth: true) {
                    router.showKillSwitchConfirmation = true
                }
                .accessibilityHint("Asks to confirm, then cancels every running task and pauses triggers.")
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle("Machines")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await store.refresh() }
    }
}

/// Node card with live stats.
struct MachineCard: View {
    let node: Nexus_V1_Node
    let status: Nexus_V1_NodeStatus?

    private var offlineText: String {
        guard let seen = status?.lastSeen.optionalDate else { return "Offline" }
        return "Offline · seen " + Format.relative(seen)
    }

    var body: some View {
        let online = status?.online == true
        VStack(alignment: .leading, spacing: NXSpace.s3) {
            HStack(spacing: NXSpace.s3) {
                NXIconTile(systemImage: node.os == "macos" ? "desktopcomputer" : "server.rack", tone: online ? .accent : .neutral, size: 36)
                VStack(alignment: .leading, spacing: 2) {
                    Text(node.name)
                        .nx(NXType.title3)
                        .foregroundStyle(NXColor.fg)
                    Text("\(node.os) · \(node.arch)")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if online {
                    NXBadge("online", tone: .ok, dot: true, pulse: true)
                } else {
                    NXBadge("offline")
                }
                NXChevron()
            }
            if let status, online {
                NXMetricRow(label: "CPU", percent: status.cpuPercent)
                NXMetricRow(label: "RAM", percent: status.memoryPercent)
                NXMetricRow(label: "Disk", percent: status.diskPercent)
                HStack(spacing: NXSpace.s3) {
                    if status.activeRuns > 0 {
                        Label("\(status.activeRuns) running", systemImage: "play.circle")
                            .foregroundStyle(NXColor.info)
                    }
                    if status.batteryPercent > 0 {
                        Label("\(Int(status.batteryPercent))%", systemImage: "battery.75")
                    }
                    Spacer()
                    if let seen = status.lastSeen.optionalDate {
                        TimelineView(.periodic(from: .now, by: 5)) { _ in
                            Text("updated \(Format.relative(seen))")
                        }
                    }
                }
                .nxText(NXType.caption.weight(.regular))
                .foregroundStyle(NXColor.muted)
            } else {
                Text(offlineText)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
        }
        .nxCard()
        .accessibilityElement(children: .combine)
        .accessibilityHint("Opens the machine.")
    }
}

struct MachineDetailView: View {
    let nodeID: String

    @Environment(NexusStore.self) private var store
    @Environment(ToastCenter.self) private var toasts
    @State private var isWaking = false

    var body: some View {
        let node = store.node(nodeID)
        let status = store.status(ofNode: nodeID)
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s6) {
                if let node {
                    let online = status?.online == true
                    HStack(alignment: .top, spacing: NXSpace.s3) {
                        NXIconTile(systemImage: node.os == "macos" ? "desktopcomputer" : "server.rack", tone: online ? .accent : .neutral, size: 48)
                        VStack(alignment: .leading, spacing: NXSpace.s1) {
                            Text(node.name)
                                .nx(NXType.title1)
                                .foregroundStyle(NXColor.fg)
                                .accessibilityAddTraits(.isHeader)
                            Text("\(node.os) / \(node.arch)" + (node.tags.isEmpty ? "" : " · " + node.tags.joined(separator: ", ")))
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.muted)
                            NXBadge(online ? "online" : "offline", tone: online ? .ok : .neutral, dot: online, pulse: online)
                        }
                    }

                    if let status, online {
                        LazyVGrid(columns: [GridItem(.flexible(), spacing: NXSpace.s3), GridItem(.flexible(), spacing: NXSpace.s3)], spacing: NXSpace.s3) {
                            statTile("CPU", status.cpuPercent)
                            statTile("Memory", status.memoryPercent)
                            statTile("Disk", status.diskPercent)
                            if status.batteryPercent > 0 {
                                statTile("Battery", status.batteryPercent, inverted: true)
                            } else {
                                NXStatTile("Active runs", value: "\(status.activeRuns)", dot: status.activeRuns > 0 ? .info : nil)
                            }
                        }
                    } else {
                        VStack(alignment: .leading, spacing: NXSpace.s3) {
                            Text(status?.lastSeen.optionalDate.map { "Offline · seen " + Format.relative($0) } ?? "Offline")
                                .nx(NXType.body)
                                .foregroundStyle(NXColor.fg2)
                            NXButton("Wake", systemImage: "power", kind: .primary, isLoading: isWaking) {
                                wake()
                            }
                            .accessibilityHint("Sends Wake-on-LAN through the Hub.")
                        }
                        .nxCard()
                    }

                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        NXSectionHeader("Runtimes", count: node.runtimes.count)
                        Text("Installed agent runtimes reported by the node")
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.muted)
                        if node.runtimes.isEmpty {
                            NXEmptyState(systemImage: "shippingbox", title: "No runtimes reported", compact: true)
                                .nxCard(padding: 0)
                        } else {
                            VStack(spacing: 0) {
                                ForEach(Array(node.runtimes.enumerated()), id: \.element.name) { index, runtime in
                                    NXRow(runtime.name, subtitle: runtime.version, leading: {
                                        Image(systemName: runtime.available ? "checkmark.circle.fill" : "xmark.circle")
                                            .foregroundStyle(runtime.available ? NXColor.ok : NXColor.muted)
                                            .accessibilityLabel(runtime.available ? "Available" : "Unavailable")
                                    })
                                    if index < node.runtimes.count - 1 { NXDivider() }
                                }
                            }
                            .nxCard(padding: 0)
                        }
                    }

                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        let tasks = store.runningTasks.filter { $0.nodeID == nodeID }
                        NXSectionHeader("Running here", count: tasks.count, live: !tasks.isEmpty)
                        if tasks.isEmpty {
                            NXEmptyState(systemImage: "checkmark.circle", title: "No runs on this machine", compact: true)
                                .nxCard(padding: 0)
                        } else {
                            TaskList(tasks: tasks)
                        }
                    }

                    NavigationLink(value: FilesRoute(nodeID: nodeID, path: "")) {
                        NXRow("Files", subtitle: "Browse the node's workspace", leading: {
                            NXIconTile(systemImage: "folder", tone: .neutral, size: 32)
                        }, trailing: { NXChevron() })
                        .nxCard(padding: 0)
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
                } else {
                    NXEmptyState(systemImage: "questionmark.circle", title: "Unknown machine", message: "It may have been removed from the Hub.")
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(node?.name ?? "Machine")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await store.refresh() }
    }

    private func statTile(_ label: String, _ percent: Double, inverted: Bool = false) -> some View {
        let fraction = percent / 100
        let tone = inverted ? (fraction <= 0.1 ? NXTone.danger : (fraction <= 0.25 ? .warn : .ok)) : nxMeterTone(fraction)
        return VStack(alignment: .leading, spacing: NXSpace.s2) {
            Text(label)
                .nx(NXType.caption)
                .foregroundStyle(NXColor.muted)
            Text("\(Int(percent.rounded()))%")
                .nx(NXType.title2)
                .monospacedDigit()
                .foregroundStyle(tone == .danger ? NXColor.danger : (tone == .warn ? NXColor.warn : NXColor.fg))
            NXMeter(value: fraction, label: label, tone: tone)
                .accessibilityHidden(true)
        }
        .nxCard(padding: NXSpace.s3 + 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue("\(Int(percent.rounded())) percent")
    }

    private func wake() {
        isWaking = true
        Task {
            defer { isWaking = false }
            do {
                try await store.wake(nodeID: nodeID)
                toasts.success("Wake-on-LAN sent")
            } catch {
                toasts.error("Couldn't wake the machine", error) { wake() }
            }
        }
    }
}

struct FilesView: View {
    let nodeID: String
    let path: String

    @Environment(NexusStore.self) private var store
    @State private var entries: [Nexus_V1_FileEntry] = []
    @State private var isLoading = true
    @State private var errorMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s4) {
                Text(path.isEmpty ? "/" : path)
                    .nx(NXType.mono)
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
                    .truncationMode(.head)
                if isLoading {
                    NXSkeletonRows(rows: 5)
                } else if let errorMessage {
                    NXErrorState(message: errorMessage, compact: true) { Task { await load() } }
                        .nxCard(padding: 0)
                } else if entries.isEmpty {
                    NXEmptyState(systemImage: "folder", title: "Empty folder", compact: true)
                        .nxCard(padding: 0)
                } else {
                    VStack(spacing: 0) {
                        ForEach(Array(entries.enumerated()), id: \.element.path) { index, entry in
                            if entry.isDir {
                                NavigationLink(value: FilesRoute(nodeID: nodeID, path: entry.path)) {
                                    NXRow(entry.name, leading: {
                                        Image(systemName: "folder.fill").foregroundStyle(NXColor.accent).accessibilityHidden(true)
                                    }, trailing: { NXChevron() })
                                }
                                .buttonStyle(NXPlainPressStyle())
                                .accessibilityLabel("Folder \(entry.name)")
                            } else {
                                NXRow(entry.name, leading: {
                                    Image(systemName: "doc").foregroundStyle(NXColor.muted).accessibilityHidden(true)
                                }, trailing: {
                                    Text(Format.bytes(entry.sizeBytes))
                                        .nx(NXType.caption.weight(.regular))
                                        .monospacedDigit()
                                        .foregroundStyle(NXColor.muted)
                                })
                                .accessibilityElement(children: .combine)
                            }
                            if index < entries.count - 1 { NXDivider(inset: 44) }
                        }
                    }
                    .nxCard(padding: 0)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(path.isEmpty ? "Files" : (path as NSString).lastPathComponent)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            entries = try await store.files(nodeID: nodeID, path: path)
                .sorted { ($0.isDir ? 0 : 1, $0.name.lowercased()) < ($1.isDir ? 0 : 1, $1.name.lowercased()) }
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
