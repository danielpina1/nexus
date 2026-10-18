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
    /// The decision being signed / sent (spinner on that button).
    @State private var deciding: Bool?
    @State private var errorMessage: String?
    @State private var comment = ""
    @State private var remember = false
    @State private var showRawPayload = false
    /// Set when the signed decision was queued because the Hub is unreachable.
    @State private var queued: (approve: Bool, expires: Date?)?

    var body: some View {
        NavigationStack {
            Group {
                if let queued {
                    queuedView(queued)
                } else if let approval {
                    content(approval)
                } else if isLoading {
                    NXLoadingState(label: "Loading approval…")
                } else if store.reachability.isUnavailable {
                    NXEmptyState(
                        systemImage: "house.and.flag",
                        title: "Can't reach your Nexus at home",
                        message: "Turn on your home VPN (WireGuard / WiFiman Teleport). This approval opens as soon as your Nexus answers.",
                        actionTitle: "Retry"
                    ) {
                        store.retryConnection()
                    }
                } else {
                    NXEmptyState(
                        systemImage: "checkmark.seal",
                        title: "Approval not found",
                        message: "It may have been decided on another device or expired."
                    )
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .nxScreenBackground()
            .navigationTitle("Approval")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                        .disabled(deciding != nil)
                }
            }
        }
        // Runs on appear and again when the Hub becomes reachable (e.g. the on-demand VPN came
        // up after tapping the notification). Cached approvals load even while unreachable.
        .task(id: store.reachability.phase) {
            if approval == nil {
                approval = await store.approval(id: approvalID)
            }
            isLoading = false
        }
        .interactiveDismissDisabled(deciding != nil)
    }

    @ViewBuilder
    private func content(_ approval: Nexus_V1_Approval) -> some View {
        let agent = store.agent(approval.agentID)
        let expires = approval.expiresDate
        let isExpired = expires.map { $0 < Date() } ?? false
        let hashVerified = (try? ApprovalSigner.verifiedActionHash(for: approval)) != nil

        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s5) {
                HStack(spacing: NXSpace.s3) {
                    AgentIconView(agent: agent, agentID: approval.agentID, size: 44)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(agent?.name ?? approval.agentID)
                            .nx(NXType.title3)
                            .foregroundStyle(NXColor.fg)
                        Text(store.node(approval.nodeID)?.name ?? approval.nodeID)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.muted)
                    }
                    Spacer()
                }

                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    HStack(spacing: 6) {
                        NXApprovalClassBadge(isBiometric: approval.isBiometric)
                        NXBadge(approval.actionClass.label.lowercased())
                    }
                    Text(approval.title)
                        .nx(NXType.title1)
                        .foregroundStyle(NXColor.fg)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                    if !approval.summary.isEmpty {
                        Text(approval.summary)
                            .nx(NXType.bodyLg)
                            .foregroundStyle(NXColor.fg2)
                    }
                    if let expires {
                        if isExpired {
                            NXBadge("Expired", systemImage: "clock", tone: .danger)
                        } else {
                            NXExpiryLabel(expiresAt: expires)
                        }
                    }
                }

                previewSection(approval)

                ForEach(approval.previews, id: \.id) { attachment in
                    if let url = URL(string: attachment.url), attachment.mimeType.hasPrefix("image/") {
                        HubPreviewImage(url: url)
                            .frame(maxHeight: 260)
                            .clipShape(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
                            .accessibilityLabel("Preview: \(attachment.name)")
                    } else if !attachment.data.isEmpty, let image = UIImage(data: attachment.data) {
                        Image(uiImage: image)
                            .resizable()
                            .scaledToFit()
                            .frame(maxHeight: 260)
                            .clipShape(RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous))
                            .accessibilityLabel("Preview: \(attachment.name)")
                    }
                }

                VStack(alignment: .leading, spacing: NXSpace.s2) {
                    if hashVerified {
                        Label("Hash recomputed on this iPhone from the action above", systemImage: "checkmark.shield.fill")
                            .nxText(NXType.footnote.weight(.medium))
                            .foregroundStyle(NXColor.ok)
                    } else {
                        Label("Action hash does not match the preview — do not approve", systemImage: "exclamationmark.shield.fill")
                            .nxText(NXType.footnote.weight(.semibold))
                            .foregroundStyle(NXColor.danger)
                    }
                    NXOverline("Action hash")
                    Text(WireFormat.hex(approval.payloadHash))
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg2)
                        .textSelection(.enabled)
                        .accessibilityLabel("Action hash")
                        .accessibilityValue(String(WireFormat.hex(approval.payloadHash).prefix(16)) + "…")
                    Text("Your signature covers exactly this action. If a single item or cent changes, the machine rejects it.")
                        .nx(NXType.caption.weight(.regular))
                        .foregroundStyle(NXColor.muted)
                }

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXTextField(placeholder: "Comment (optional)", text: $comment, axis: .vertical)
                    Toggle(isOn: $remember) {
                        Text("Remember for this agent and action")
                            .nx(NXType.body)
                            .foregroundStyle(NXColor.fg)
                    }
                    .tint(NXColor.accent)
                    .frame(minHeight: NXSize.touchMin)
                }

                if let errorMessage {
                    NXInlineError(errorMessage, retry: nil) { self.errorMessage = nil }
                }

                if store.reachability.isUnavailable && !isExpired {
                    Label {
                        Text(Self.queueNote(expires: expires))
                    } icon: {
                        Image(systemName: "tray.and.arrow.up")
                    }
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
                }

                VStack(spacing: NXSpace.s3) {
                    NXApproveButton(isBiometric: approval.isBiometric, isLoading: deciding == true, size: .large, fullWidth: true) {
                        Task { await decide(approval, approve: true) }
                    }
                    NXButton("Deny", systemImage: "xmark", kind: .secondary, size: .large, isLoading: deciding == false, fullWidth: true) {
                        Task { await decide(approval, approve: false) }
                    }
                }
                .disabled(deciding != nil || isExpired)
            }
            .padding(NXSpace.s4)
            .padding(.bottom, NXSpace.s6)
        }
    }

    static func queueNote(expires: Date?) -> String {
        var note = "You're away from your Nexus. Your decision is signed now and sent automatically when it's reachable"
        if let expires {
            note += " — if that's before " + expires.formatted(date: .omitted, time: .shortened)
        }
        return note + "."
    }

    /// Shown after a decision was signed but could not be sent yet.
    private func queuedView(_ queued: (approve: Bool, expires: Date?)) -> some View {
        VStack(spacing: NXSpace.s4) {
            NXIconTile(systemImage: "checkmark.seal.fill", tone: queued.approve ? .ok : .neutral, size: 56)
            Text(queued.approve ? "Approved — waiting to send" : "Denied — waiting to send")
                .nx(NXType.title2)
                .foregroundStyle(NXColor.fg)
                .multilineTextAlignment(.center)
            Text("Your Face ID–signed decision is stored on this iPhone and will be sent as soon as your Nexus at home is reachable. Turn on your home VPN to send it now.")
                .nx(NXType.body)
                .multilineTextAlignment(.center)
                .foregroundStyle(NXColor.fg2)
            if let expires = queued.expires {
                Label("Valid until \(expires.formatted(date: .omitted, time: .shortened)). If it can't be delivered by then, it is dropped and nothing happens.", systemImage: "timer")
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            NXButton("Done", kind: .primary, fullWidth: true) { dismiss() }
        }
        .padding(NXSpace.s6)
    }

    @ViewBuilder
    private func previewSection(_ approval: Nexus_V1_Approval) -> some View {
        let preview = approval.payloadPreview
        if !preview.isEmpty {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                HStack {
                    Text("Exact action")
                        .nx(NXType.title3)
                        .foregroundStyle(NXColor.fg)
                        .accessibilityAddTraits(.isHeader)
                    Spacer()
                    Button(showRawPayload ? "Fields" : "Raw") { showRawPayload.toggle() }
                        .font(NXType.footnote.weight(.semibold).font)
                        .foregroundStyle(NXColor.accent)
                        .frame(minWidth: NXSize.touchMin, minHeight: NXSize.touchMin)
                        .nxFocusRing()
                        .accessibilityLabel(showRawPayload ? "Show fields" : "Show raw JSON")
                }
                if showRawPayload {
                    Text(preview)
                        .nx(NXType.mono)
                        .foregroundStyle(NXColor.fg2)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    let keys = approval.payload.fields.keys.sorted()
                    ForEach(Array(keys.enumerated()), id: \.element) { index, key in
                        HStack(alignment: .firstTextBaseline) {
                            Text(key)
                                .nx(NXType.body)
                                .foregroundStyle(NXColor.muted)
                            Spacer()
                            Text(Self.display(approval.payload.fields[key]))
                                .nx(NXType.body.weight(.medium))
                                .monospacedDigit()
                                .foregroundStyle(NXColor.fg)
                                .multilineTextAlignment(.trailing)
                        }
                        .frame(minHeight: 36)
                        .accessibilityElement(children: .combine)
                        if index < keys.count - 1 { NXDivider() }
                    }
                }
            }
            .nxCard()
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
        deciding = approve
        errorMessage = nil
        defer { deciding = nil }
        do {
            let delivery = try await store.decide(approval, approve: approve, comment: comment, remember: remember)
            if delivery.isQueued {
                queued = (approve, approval.expiresDate)
            } else {
                dismiss()
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

/// Preview image from the Hub. `AsyncImage` uses `URLSession.shared`, which rejects the Hub's
/// private CA, so Hub URLs load through `HubDownloads` (CA pinned with `HubTrust`, bearer
/// token); any other URL keeps `AsyncImage`.
private struct HubPreviewImage: View {
    let url: URL

    @State private var image: UIImage?
    @State private var failed = false

    var body: some View {
        if HubDownloads.isHubURL(url) {
            Group {
                if let image {
                    Image(uiImage: image).resizable().scaledToFit()
                } else if failed {
                    Image(systemName: "photo")
                        .font(.largeTitle)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, minHeight: 120)
                } else {
                    NXSkeleton(height: 180, cornerRadius: NXRadius.lg)
                }
            }
            .task(id: url) {
                image = nil
                failed = false
                do {
                    let (data, _) = try await HubDownloads.data(from: url)
                    if let loaded = UIImage(data: data) {
                        image = loaded
                    } else {
                        failed = true
                    }
                } catch {
                    failed = true
                }
            }
        } else {
            AsyncImage(url: url) { image in
                image.resizable().scaledToFit()
            } placeholder: {
                NXSkeleton(height: 180, cornerRadius: NXRadius.lg)
            }
        }
    }
}

#Preview {
    ApprovalSheet(approvalID: "apr_demo_checkout")
        .environment(NexusStore.preview())
}
