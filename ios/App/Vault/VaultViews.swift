import NexusCore
import SwiftUI

struct NoteRoute: Hashable {
    let id: String
    let path: String
    let title: String
}

struct VaultView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    @State private var query = ""
    @State private var hits: [Nexus_V1_SearchHit] = []
    @State private var isSearching = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            List {
                if let errorMessage {
                    ErrorBanner(message: errorMessage)
                }
                if hits.isEmpty && !isSearching {
                    Text(query.isEmpty ? "Search your notes, people, projects and facts." : "No results.")
                        .foregroundStyle(.secondary)
                }
                ForEach(hits, id: \.note.id) { hit in
                    NavigationLink(value: NoteRoute(id: hit.note.id, path: hit.note.path, title: hit.note.title)) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(hit.note.title.isEmpty ? hit.note.path : hit.note.title).font(.subheadline.weight(.semibold))
                            Text(hit.snippet).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                            Text(hit.note.path).font(.caption2.monospaced()).foregroundStyle(.tertiary)
                        }
                    }
                }
            }
            .overlay {
                if isSearching { ProgressView() }
            }
            .navigationTitle("Vault")
            .searchable(text: $query, prompt: "Search the vault")
            .task(id: query) {
                // Debounce typing.
                try? await Task.sleep(for: .milliseconds(300))
                guard !Task.isCancelled else { return }
                await search()
            }
            .toolbar {
                CommonToolbar()
                ToolbarItem(placement: .bottomBar) {
                    Button {
                        router.showCapture = true
                    } label: {
                        Label("Quick capture", systemImage: "square.and.pencil")
                    }
                }
            }
            .navigationDestination(for: NoteRoute.self) { route in
                NoteView(route: route)
            }
        }
    }

    private func search() async {
        isSearching = true
        defer { isSearching = false }
        do {
            hits = try await store.search(query)
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

struct NoteView: View {
    let route: NoteRoute

    @Environment(NexusStore.self) private var store
    @State private var note: Nexus_V1_Note?
    @State private var errorMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if let note {
                    if !note.tags.isEmpty {
                        Text(note.tags.map { "#" + $0 }.joined(separator: " "))
                            .font(.caption)
                            .foregroundStyle(.tint)
                    }
                    MarkdownText(markdown: note.body)
                        .textSelection(.enabled)
                    if !note.backlinks.isEmpty {
                        Divider()
                        Text("Backlinks").font(.headline)
                        ForEach(note.backlinks, id: \.id) { link in
                            NavigationLink(value: NoteRoute(id: link.id, path: link.path, title: link.title)) {
                                Label(link.title.isEmpty ? link.path : link.title, systemImage: "link")
                            }
                        }
                    }
                    if let updated = note.updatedAt.optionalDate {
                        Text("Updated \(Format.relative(updated))").font(.caption2).foregroundStyle(.secondary)
                    }
                } else if let errorMessage {
                    ErrorBanner(message: errorMessage)
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .padding()
        }
        .navigationTitle(note?.ref.title ?? route.title)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            do {
                note = try await store.note(id: route.id, path: route.path)
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}

/// Quick capture sheet (also opened by the Control Center control / `nexus://capture`).
struct CaptureView: View {
    @Environment(NexusStore.self) private var store
    @Environment(\.dismiss) private var dismiss

    @State private var text = ""
    @State private var url = ""
    @State private var isSaving = false
    @State private var errorMessage: String?
    @FocusState private var focused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("What's on your mind?", text: $text, axis: .vertical)
                        .lineLimit(4...12)
                        .focused($focused)
                    TextField("Link (optional)", text: $url)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
                if let errorMessage {
                    Section { ErrorBanner(message: errorMessage) }
                }
            }
            .navigationTitle("Capture")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(text.isEmpty || isSaving)
                }
            }
            .onAppear { focused = true }
        }
    }

    private func save() async {
        isSaving = true
        defer { isSaving = false }
        do {
            try await store.capture(text, url: url.isEmpty ? nil : url)
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
