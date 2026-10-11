import NexusCore
import SwiftUI

/// "What you see is what you sign" (SECURITY.md §5): shows the exact action preview and
/// signs the decision — biometric approvals unlock the Secure Enclave approval key with Face ID.
/// Presented from the inbox, notifications and Live Activity deep links (`nexus://approval/<id>`).
struct ApprovalSheet: View {
    let approvalID: String

    @Environment(NexusStore.self) private var store
    @Environment(\.dismiss) private var dismiss

    @State private var approval: Nexus_V1_Approval?
    @State private var isLoading = true
    @State private var isSubmitting = false
    @State private var errorMessage: String?
    @State private var comment = ""
    @State private var remember = false
    @State private var showRawPayload = false

    var body: some View {
        NavigationStack {
            Group {
                if let approval {
                    content(approval)
                } else if isLoading {
                    ProgressView("Loading approval…")
                } else {
                    ContentUnavailableView(
                        "Approval not found",
                        systemImage: "checkmark.seal",
                        description: Text("It may have been decided on another device or expired.")
                    )
                }
            }
            .navigationTitle("Approval")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                }
            }
        }
        .task {
            approval = await store.approval(id: approvalID)
            isLoading = false
        }
        .interactiveDismissDisabled(isSubmitting)
    }

    @ViewBuilder
    private func content(_ approval: Nexus_V1_Approval) -> some View {
        let agent = store.agent(approval.agentID)
        let expires = approval.expiresDate
        let isExpired = expires.map { $0 < Date() } ?? false

        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(spacing: 12) {
                    AgentIconView(agent: agent, size: 44)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(agent?.name ?? approval.agentID).font(.headline)
                        Text("\(approval.actionClass.label) · \(store.node(approval.nodeID)?.name ?? approval.nodeID)")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    if approval.isBiometric {
                        Image(systemName: "faceid").font(.title2).foregroundStyle(.orange)
                    }
                }

                Text(approval.title).font(.title2.bold())
                if !approval.summary.isEmpty {
                    Text(approval.summary).font(.body)
                }

                if let expires {
                    Label(
                        isExpired ? "Expired" : "Expires \(Format.relative(expires))",
                        systemImage: "timer"
                    )
                    .font(.footnote)
                    .foregroundStyle(isExpired ? Color.red : Color.secondary)
                }

                previewSection(approval)

                ForEach(approval.previews, id: \.id) { attachment in
                    if let url = URL(string: attachment.url), attachment.mimeType.hasPrefix("image/") {
                        AsyncImage(url: url) { image in
                            image.resizable().scaledToFit()
                        } placeholder: {
                            ProgressView()
                        }
                        .frame(maxHeight: 260)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                    } else if !attachment.data.isEmpty, let image = UIImage(data: attachment.data) {
                        Image(uiImage: image)
                            .resizable()
                            .scaledToFit()
                            .frame(maxHeight: 260)
                            .clipShape(RoundedRectangle(cornerRadius: 12))
                    }
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text("Action hash").font(.caption.weight(.semibold))
                    Text(WireFormat.hex(approval.payloadHash))
                        .font(.caption2.monospaced())
                        .textSelection(.enabled)
                        .foregroundStyle(.secondary)
                    Text("Your signature covers exactly this action. If a single item or cent changes, the machine rejects it.")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }

                TextField("Comment (optional)", text: $comment, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                Toggle("Remember for this agent and action", isOn: $remember)
                    .font(.subheadline)

                if let errorMessage {
                    ErrorBanner(message: errorMessage) { self.errorMessage = nil }
                }

                VStack(spacing: 10) {
                    Button {
                        Task { await decide(approval, approve: true) }
                    } label: {
                        Label(
                            approval.isBiometric ? "Approve with Face ID" : "Approve",
                            systemImage: approval.isBiometric ? "faceid" : "checkmark"
                        )
                        .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .tint(.green)

                    Button(role: .destructive) {
                        Task { await decide(approval, approve: false) }
                    } label: {
                        Label("Deny", systemImage: "xmark").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.large)
                }
                .disabled(isSubmitting || isExpired)
                .overlay {
                    if isSubmitting { ProgressView() }
                }
            }
            .padding()
        }
    }

    @ViewBuilder
    private func previewSection(_ approval: Nexus_V1_Approval) -> some View {
        let preview = approval.payloadPreview
        if !preview.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Exact action").font(.headline)
                    Spacer()
                    Button(showRawPayload ? "Fields" : "Raw") { showRawPayload.toggle() }
                        .font(.caption)
                }
                if showRawPayload {
                    Text(preview)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    ForEach(approval.payload.fields.keys.sorted(), id: \.self) { key in
                        HStack(alignment: .firstTextBaseline) {
                            Text(key).font(.subheadline).foregroundStyle(.secondary)
                            Spacer()
                            Text(Self.display(approval.payload.fields[key]))
                                .font(.subheadline.monospacedDigit())
                                .multilineTextAlignment(.trailing)
                        }
                        Divider()
                    }
                }
            }
            .padding(12)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
        }
    }

    private static func display(_ value: Google_Protobuf_Value?) -> String {
        guard let value else { return "" }
        switch value.kind {
        case .stringValue(let s): return s
        case .numberValue(let n): return n.formatted()
        case .boolValue(let b): return b ? "yes" : "no"
        case .nullValue: return "—"
        case .listValue(let list): return "\(list.values.count) items"
        case .structValue(let s): return "\(s.fields.count) fields"
        case .none: return ""
        }
    }

    private func decide(_ approval: Nexus_V1_Approval, approve: Bool) async {
        isSubmitting = true
        errorMessage = nil
        defer { isSubmitting = false }
        do {
            try await store.decide(approval, approve: approve, comment: comment, remember: remember)
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

#Preview {
    ApprovalSheet(approvalID: "apr_demo_checkout")
        .environment(NexusStore.preview())
}
