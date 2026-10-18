import NexusCore
import SwiftUI

/// Knowledge (web Vault): search notes, people, projects and facts; quick capture.
struct KnowledgeView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    @State private var query = ""
    @State private var hits: [Nexus_V1_SearchHit] = []
    @State private var isSearching = false
    @State private var errorMessage: String?

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: NXSpace.s4) {
                NXButton("Quick capture", systemImage: "square.and.pencil", kind: .secondary, fullWidth: true) {
                    router.showCapture = true
                }
                if let errorMessage {
                    NXErrorState(title: "Search failed", message: errorMessage, compact: true) {
                        Task { await search() }
                    }
                    .nxCard(padding: 0)
                } else if isSearching && hits.isEmpty {
                    NXSkeletonRows(rows: 4)
                } else if hits.isEmpty {
                    NXEmptyState(
                        systemImage: query.isEmpty ? "books.vertical" : "magnifyingglass",
                        title: query.isEmpty ? "Search your knowledge" : "No results",
                        message: query.isEmpty ? "Search your notes, people, projects and facts." : "Try other words, or capture this as a new note.",
                        compact: true
                    )
                    .nxCard(padding: 0)
                } else {
                    VStack(spacing: 0) {
                        ForEach(Array(hits.enumerated()), id: \.element.note.id) { index, hit in
                            NavigationLink(value: NoteRoute(id: hit.note.id, path: hit.note.path, title: hit.note.title)) {
                                VStack(alignment: .leading, spacing: NXSpace.s1) {
                                    Text(hit.note.title.isEmpty ? hit.note.path : hit.note.title)
                                        .nx(NXType.body.weight(.semibold))
                                        .foregroundStyle(NXColor.fg)
                                    if !hit.snippet.isEmpty {
                                        Text(hit.snippet)
                                            .nx(NXType.footnote)
                                            .foregroundStyle(NXColor.fg2)
                                            .lineLimit(2)
                                    }
                                    Text(hit.note.path)
                                        .nx(NXType.mono)
                                        .foregroundStyle(NXColor.muted)
                                        .lineLimit(1)
                                }
                                .padding(.horizontal, NXSpace.s4)
                                .padding(.vertical, NXSpace.s3)
                                .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
                                .contentShape(Rectangle())
                                .accessibilityElement(children: .combine)
                            }
                            .buttonStyle(NXPlainPressStyle())
                            if index < hits.count - 1 { NXDivider() }
                        }
                    }
                    .nxCard(padding: 0)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle("Knowledge")
        .searchable(text: $query, prompt: "Search the vault")
        .task(id: query) {
            // Debounce typing.
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            await search()
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
            VStack(alignment: .leading, spacing: NXSpace.s4) {
                if let note {
                    if !note.tags.isEmpty {
                        FlowBadges {
                            ForEach(note.tags, id: \.self) { tag in
                                NXBadge("#" + tag, tone: .accent)
                            }
                        }
                    }
                    MarkdownText(markdown: note.body)
                        .nxText(NXType.bodyLg)
                        .foregroundStyle(NXColor.fg)
                        .textSelection(.enabled)
                    if !note.backlinks.isEmpty {
                        VStack(alignment: .leading, spacing: NXSpace.s3) {
                            NXSectionHeader("Backlinks", count: note.backlinks.count)
                            VStack(spacing: 0) {
                                ForEach(Array(note.backlinks.enumerated()), id: \.element.id) { index, link in
                                    NavigationLink(value: NoteRoute(id: link.id, path: link.path, title: link.title)) {
                                        NXRow(link.title.isEmpty ? link.path : link.title, leading: {
                                            Image(systemName: "link").foregroundStyle(NXColor.muted).accessibilityHidden(true)
                                        }, trailing: { NXChevron() })
                                    }
                                    .buttonStyle(NXPlainPressStyle())
                                    if index < note.backlinks.count - 1 { NXDivider() }
                                }
                            }
                            .nxCard(padding: 0)
                        }
                    }
                    if let updated = note.updatedAt.optionalDate {
                        Text("Updated \(Format.relative(updated))")
                            .nx(NXType.caption.weight(.regular))
                            .foregroundStyle(NXColor.muted)
                    }
                } else if let errorMessage {
                    NXErrorState(message: errorMessage) { Task { await load() } }
                } else {
                    NXSkeleton(height: 20, width: 220)
                    NXSkeleton(height: 14)
                    NXSkeleton(height: 14)
                    NXSkeleton(height: 14, width: 180)
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle(note?.ref.title ?? route.title)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        do {
            note = try await store.note(id: route.id, path: route.path)
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
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
                        .nxText(NXType.bodyLg)
                        .focused($focused)
                    TextField("Link (optional)", text: $url)
                        .nxText(NXType.bodyLg)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
                .listRowBackground(NXColor.surface)
                if let errorMessage {
                    Section { NXInlineError(errorMessage) { self.errorMessage = nil } }
                        .listRowBackground(Color.clear)
                }
            }
            .scrollContentBackground(.hidden)
            .navigationTitle("Capture")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if isSaving {
                        ProgressView()
                    } else {
                        Button("Save") { Task { await save() } }
                            .disabled(text.isEmpty)
                    }
                }
            }
            .onAppear { focused = true }
        }
        .presentationDetents([.medium, .large])
        .nxSheetBackground()
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
