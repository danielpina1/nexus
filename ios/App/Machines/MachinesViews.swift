import NexusCore
import SwiftUI

struct MachinesView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        @Bindable var router = router
        NavigationStack(path: $router.machinesPath) {
            List {
                if store.nodes.isEmpty {
                    Text("No machines paired yet. Run `nexus-node pair` on a computer.")
                        .foregroundStyle(.secondary)
                }
                ForEach(store.nodes, id: \.id) { node in
                    NavigationLink(value: node.id) {
                        MachineRow(node: node, status: store.status(ofNode: node.id))
                    }
                }
                Section {
                    Button(role: .destructive) {
                        router.showKillSwitchConfirmation = true
                    } label: {
                        Label("Kill switch — stop everything", systemImage: "stop.circle.fill")
                    }
                }
            }
            .navigationTitle("Machines")
            .toolbar { CommonToolbar() }
            .refreshable { await store.refresh() }
            .navigationDestination(for: String.self) { nodeID in
                MachineDetailView(nodeID: nodeID)
            }
        }
    }
}

struct MachineRow: View {
    let node: Nexus_V1_Node
    let status: Nexus_V1_NodeStatus?

    private var offlineText: String {
        guard let seen = status?.lastSeen.optionalDate else { return "Offline" }
        return "Offline · seen " + Format.relative(seen)
    }

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: node.os == "macos" ? "desktopcomputer" : "server.rack")
                .font(.title3)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Circle().fill(status?.online == true ? Color.green : Color.gray).frame(width: 8, height: 8)
                    Text(node.name).font(.headline)
                }
                if let status, status.online {
                    Text("CPU \(Int(status.cpuPercent))% · RAM \(Int(status.memoryPercent))% · disk \(Int(status.diskPercent))%")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Text(offlineText)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            if let runs = status?.activeRuns, runs > 0 {
                Label("\(runs)", systemImage: "play.circle.fill")
                    .font(.caption)
                    .foregroundStyle(.blue)
            }
        }
    }
}

struct MachineDetailView: View {
    let nodeID: String

    @Environment(NexusStore.self) private var store
    @State private var message: String?

    var body: some View {
        let node = store.node(nodeID)
        let status = store.status(ofNode: nodeID)
        List {
            if let node {
                Section("Status") {
                    LabeledContent("Online", value: status?.online == true ? "Yes" : "No")
                    if let status {
                        LabeledContent("CPU", value: "\(Int(status.cpuPercent))%")
                        LabeledContent("Memory", value: "\(Int(status.memoryPercent))%")
                        LabeledContent("Disk", value: "\(Int(status.diskPercent))%")
                        if status.batteryPercent > 0 {
                            LabeledContent("Battery", value: "\(Int(status.batteryPercent))%")
                        }
                        LabeledContent("Active runs", value: "\(status.activeRuns)")
                    }
                    LabeledContent("System", value: "\(node.os) / \(node.arch)")
                    if !node.tags.isEmpty {
                        LabeledContent("Tags", value: node.tags.joined(separator: ", "))
                    }
                }
                Section("Runtimes") {
                    ForEach(node.runtimes, id: \.name) { runtime in
                        HStack {
                            Text(runtime.name)
                            Spacer()
                            Text(runtime.version).foregroundStyle(.secondary)
                            Image(systemName: runtime.available ? "checkmark.circle.fill" : "xmark.circle")
                                .foregroundStyle(runtime.available ? Color.green : Color.secondary)
                        }
                    }
                }
                Section("Running here") {
                    let tasks = store.runningTasks.filter { $0.nodeID == nodeID }
                    if tasks.isEmpty {
                        Text("Nothing running.").foregroundStyle(.secondary)
                    }
                    ForEach(tasks, id: \.id) { task in
                        TaskRow(task: task)
                    }
                }
                Section {
                    NavigationLink {
                        FilesView(nodeID: nodeID, path: "")
                    } label: {
                        Label("Files", systemImage: "folder")
                    }
                    if status?.online != true {
                        Button {
                            Task {
                                do {
                                    try await store.wake(nodeID: nodeID)
                                    message = "Wake-on-LAN sent."
                                } catch {
                                    message = error.localizedDescription
                                }
                            }
                        } label: {
                            Label("Wake", systemImage: "power")
                        }
                    }
                    if let message {
                        Text(message).font(.caption).foregroundStyle(.secondary)
                    }
                }
            } else {
                Text("Unknown machine.")
            }
        }
        .navigationTitle(node?.name ?? "Machine")
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
        List {
            if let errorMessage {
                ErrorBanner(message: errorMessage)
            }
            ForEach(entries, id: \.path) { entry in
                if entry.isDir {
                    NavigationLink {
                        FilesView(nodeID: nodeID, path: entry.path)
                    } label: {
                        Label(entry.name, systemImage: "folder.fill")
                    }
                } else {
                    HStack {
                        Label(entry.name, systemImage: "doc")
                        Spacer()
                        Text(Format.bytes(entry.sizeBytes)).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .overlay {
            if isLoading { ProgressView() }
        }
        .navigationTitle(path.isEmpty ? "Files" : (path as NSString).lastPathComponent)
        .task {
            do {
                entries = try await store.files(nodeID: nodeID, path: path)
                    .sorted { ($0.isDir ? 0 : 1, $0.name.lowercased()) < ($1.isDir ? 0 : 1, $1.name.lowercased()) }
            } catch {
                errorMessage = error.localizedDescription
            }
            isLoading = false
        }
    }
}
