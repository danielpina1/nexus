import NexusCore
import SwiftUI
import UIKit
import UserNotifications

/// Settings presented as a sheet (gear button). The same form is pushed from More.
struct SettingsView: View {
    var body: some View {
        NavigationStack {
            SettingsForm(isSheet: true)
        }
    }
}

/// Settings (web Settings + Security): remote access, Hub, appearance, devices,
/// notifications, trust, demo mode and the kill switch.
struct SettingsForm: View {
    let isSheet: Bool

    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(AppLock.self) private var lock
    @Environment(\.dismiss) private var dismiss

    @State private var notificationStatus: UNAuthorizationStatus = .notDetermined
    @State private var confirmUnpair = false
    @State private var devToken = Keychain.string(for: .devToken) ?? ""

    var body: some View {
        @Bindable var lock = lock
        Form {
            RemoteAccessSection()

            Section {
                AppearancePicker()
            } header: {
                Text("Appearance")
            } footer: {
                Text("System follows the iOS setting. Dark is OLED black.")
            }
            .listRowBackground(NXColor.surface)

            Section("Hub") {
                if let configuration = store.configuration {
                    LabeledContent("Event stream", value: store.connection.label)
                    if let deviceID = configuration.deviceID {
                        LabeledContent("Device ID", value: deviceID)
                    }
                    if let paired = configuration.pairedAt {
                        LabeledContent("Paired", value: paired.formatted(date: .abbreviated, time: .shortened))
                    }
                } else {
                    Text("Not paired").foregroundStyle(NXColor.muted)
                }
                SecureField("Dev token (development only)", text: $devToken)
                    .onSubmit {
                        Keychain.setString(devToken.isEmpty ? nil : devToken, for: .devToken)
                        store.reconnect()
                    }
                Button("Reconnect") { store.reconnect() }
            }
            .listRowBackground(NXColor.surface)

            Section("Devices") {
                ForEach(store.devices, id: \.id) { device in
                    HStack {
                        VStack(alignment: .leading) {
                            Text(device.name + (device.primary ? " (primary)" : ""))
                            if let seen = device.lastSeen.optionalDate {
                                Text("Seen \(Format.relative(seen))").nxText(NXType.caption.weight(.regular)).foregroundStyle(NXColor.muted)
                            }
                        }
                        Spacer()
                        if device.id == store.configuration?.deviceID {
                            Text("This iPhone").nxText(NXType.caption.weight(.regular)).foregroundStyle(NXColor.muted)
                        }
                    }
                    .swipeActions {
                        if device.id != store.configuration?.deviceID {
                            Button("Revoke", role: .destructive) {
                                Task { try? await store.revoke(deviceID: device.id) }
                            }
                        }
                    }
                }
            }
            .listRowBackground(NXColor.surface)

            Section("Notifications") {
                LabeledContent("Permission", value: Self.label(notificationStatus))
                if notificationStatus == .notDetermined {
                    Button("Enable notifications") {
                        Task {
                            await NotificationCategories.requestAuthorizationAndRegister()
                            await refreshNotificationStatus()
                        }
                    }
                } else {
                    Button("Open iOS notification settings") {
                        if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
                            UIApplication.shared.open(url)
                        }
                    }
                }
                LabeledContent("Live Activities", value: AppEnvironment.shared.liveActivities.areActivitiesEnabled ? "On" : "Off")
            }
            .listRowBackground(NXColor.surface)

            Section {
                Toggle("Face ID on open", isOn: $lock.isEnabled)
                LabeledContent("Secure Enclave", value: KeyManager.shared.isHardwareBacked ? "Yes" : "No (Simulator — software keys)")
                if let code = store.configuration?.safetyCode {
                    LabeledContent("Safety code", value: code)
                }
                if let keys = try? KeyManager.shared.ensureKeys() {
                    LabeledContent("Auth key", value: WireFormat.keyID(keys.auth))
                        .nxText(NXType.mono)
                    LabeledContent("Approval key", value: WireFormat.keyID(keys.approval))
                        .nxText(NXType.mono)
                    LabeledContent("Encryption key", value: WireFormat.keyID(keys.encryption))
                        .nxText(NXType.mono)
                }
            } header: {
                Text("Trust")
            } footer: {
                Text("Keys never leave this iPhone. Approvals are signed with a Face ID–bound Secure Enclave key and verified by the machine that executes them.")
            }
            .listRowBackground(NXColor.surface)

            Section {
                Toggle("Demo mode", isOn: Binding(
                    get: { store.isDemo },
                    set: { store.setDemoMode($0) }
                ))
            } footer: {
                Text("Shows sample agents, tasks and approvals without a Hub.")
            }
            .listRowBackground(NXColor.surface)

            Section {
                Button(role: .destructive) {
                    router.showSettings = false
                    router.showKillSwitchConfirmation = true
                } label: {
                    Label("Kill switch", systemImage: "stop.circle.fill")
                        .foregroundStyle(NXColor.danger)
                }
                Button("Unpair this iPhone", role: .destructive) {
                    confirmUnpair = true
                }
                .foregroundStyle(NXColor.danger)
            }
            .listRowBackground(NXColor.surface)
        }
        .nxForm()
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(isSheet ? .inline : .large)
        .toolbar {
            if isSheet {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .confirmationDialog("Unpair this iPhone?", isPresented: $confirmUnpair, titleVisibility: .visible) {
            Button("Unpair and delete keys", role: .destructive) {
                AppEnvironment.shared.liveActivities.endAll()
                store.unpair()
                dismiss()
            }
        } message: {
            Text("Deletes this device's keys. You will need a new pairing code from the Hub.")
        }
        .task {
            await store.loadDevices()
            await refreshNotificationStatus()
        }
    }

    private func refreshNotificationStatus() async {
        notificationStatus = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }

    static func label(_ status: UNAuthorizationStatus) -> String {
        switch status {
        case .authorized: return "Allowed"
        case .denied: return "Denied"
        case .provisional: return "Provisional"
        case .ephemeral: return "Ephemeral"
        case .notDetermined: return "Not asked"
        @unknown default: return "Unknown"
        }
    }
}

// MARK: - Remote access (docs/NETWORK.md)

/// Hub URL(s), current route (home Wi-Fi / VPN / unreachable), last contact, the outbox, and
/// a short offline how-to for the UniFi VPN.
struct RemoteAccessSection: View {
    @Environment(NexusStore.self) private var store

    var body: some View {
        Section {
            if let configuration = store.configuration {
                ForEach(configuration.hubURLs, id: \.self) { url in
                    HStack {
                        Text(url.absoluteString)
                            .nxText(NXType.mono)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer()
                        if store.reachability.isConnected && url == store.currentHubURL {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundStyle(NXColor.ok)
                                .accessibilityLabel("In use")
                        }
                    }
                }
            }
            LabeledContent("Route") {
                Label(store.routeLabel, systemImage: routeSymbol)
                    .foregroundStyle(routeColor)
            }
            LabeledContent("Network", value: store.networkPath?.label ?? "—")
            LabeledContent("Last contact", value: store.lastContact.map { Format.relative($0) } ?? "Never")
            if case .unreachable(let reason) = store.reachability {
                Text(reason)
                    .nxText(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            if !store.outbox.isEmpty {
                DisclosureGroup("Waiting to send (\(store.outbox.count))") {
                    ForEach(store.outbox) { item in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.summary).lineLimit(2)
                            Text("Queued \(Format.relative(item.createdAt))" + (item.attempts > 0 ? " · \(item.attempts) tries" : ""))
                                .nxText(NXType.caption.weight(.regular))
                                .foregroundStyle(NXColor.muted)
                        }
                        .swipeActions {
                            Button("Discard", role: .destructive) {
                                store.discardOutboxItem(item.id)
                            }
                        }
                    }
                }
            }
            Button {
                store.retryConnection()
            } label: {
                HStack {
                    Label("Test connection", systemImage: "antenna.radiowaves.left.and.right")
                    if store.isProbing {
                        Spacer()
                        ProgressView()
                    }
                }
            }
            .disabled(store.isDemo || store.isProbing)
            NavigationLink {
                RemoteAccessHelpView()
            } label: {
                Label("How to connect away from home", systemImage: "questionmark.circle")
            }
        } header: {
            Text("Remote access")
        } footer: {
            Text("Your Nexus is never exposed to the internet. Away from home the app reaches it through your UniFi VPN. Notifications and Live Activities arrive even with the VPN off.")
        }
        .listRowBackground(NXColor.surface)
    }

    private var routeSymbol: String {
        switch store.reachability {
        case .connected(_, let viaVPN):
            return viaVPN == true ? "lock.shield.fill" : "house.fill"
        case .unreachable: return "house.and.flag"
        case .offline: return "wifi.slash"
        case .checking: return "arrow.triangle.2.circlepath"
        case .unknown: return "questionmark.circle"
        }
    }

    private var routeColor: Color {
        switch store.reachability {
        case .connected: return NXColor.ok
        case .unreachable: return NXColor.warn
        default: return NXColor.muted
        }
    }
}

/// Concise, offline how-to (links to the docs would not open while away from home).
struct RemoteAccessHelpView: View {
    var body: some View {
        List {
            Section {
                Text("Your Nexus lives at home behind your UniFi gateway. On home Wi-Fi the app talks to it directly. Away from home, a VPN to your gateway makes it work exactly the same — no ports are opened to the internet.")
                    .nxText(NXType.body)
            }
            .listRowBackground(NXColor.surface)

            Section {
                HelpStep(1, "In the UniFi Network app or console: **Settings → VPN → VPN Server → Create → WireGuard**.")
                HelpStep(2, "Add a client called **iPhone** and show its QR code.")
                HelpStep(3, "Install the **WireGuard** app on this iPhone → **+** → **Create from QR code** → scan it.")
                HelpStep(4, "Edit the tunnel → **On-Demand**: turn on **Cellular** and **Wi-Fi**, and choose **Except these SSIDs** → your home Wi-Fi.")
                HelpStep(5, "Use your gateway as the tunnel's DNS so **nexus.home.arpa** resolves when you're away.")
            } header: {
                Text("WireGuard — recommended")
            } footer: {
                Text("With On-Demand the tunnel comes up by itself whenever you leave home, including when you tap an approval notification.")
            }
            .listRowBackground(NXColor.surface)

            Section {
                HelpStep(1, "In UniFi: **Settings → VPN → Teleport** → enable it and create an invite link.")
                HelpStep(2, "Install **WiFiman** on this iPhone and open the invite.")
                HelpStep(3, "When you're away, open WiFiman and tap **Connect** before using Nexus.")
            } header: {
                Text("Teleport — simplest")
            } footer: {
                Text("Teleport has no per-network rules, so it doesn't connect automatically.")
            }
            .listRowBackground(NXColor.surface)

            Section("Check it works") {
                HelpStep(1, "Turn Wi-Fi off (cellular only) with the VPN on.")
                HelpStep(2, "Settings → Remote access → **Test connection**. Route should say **VPN**.")
            }
            .listRowBackground(NXColor.surface)

            Section("Good to know") {
                Label("Pair at home on Wi-Fi. After that the same Hub address works everywhere.", systemImage: "house.fill")
                Label("Notifications come from Apple, so they arrive even with the VPN off.", systemImage: "bell.badge")
                Label("Commands, replies and Face ID–signed approvals made while your Nexus is unreachable are queued and sent when it's back. An approval that expires first is dropped — nothing happens.", systemImage: "tray.and.arrow.up")
                Label("Lost your phone? Revoke its WireGuard client in UniFi and this device in Nexus.", systemImage: "iphone.slash")
            }
            .listRowBackground(NXColor.surface)
            .nxText(NXType.body)
        }
        .nxForm()
        .navigationTitle("Remote access")
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct HelpStep: View {
    let number: Int
    let text: LocalizedStringKey

    init(_ number: Int, _ text: LocalizedStringKey) {
        self.number = number
        self.text = text
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text("\(number)")
                .font(NXType.caption.weight(.bold).font)
                .monospacedDigit()
                .foregroundStyle(NXColor.primaryFg)
                .frame(width: 20, height: 20)
                .background(Circle().fill(NXColor.primary))
                .accessibilityLabel("Step \(number)")
            Text(text)
                .nxText(NXType.body)
        }
    }
}

/// Grouped forms in the design system: `bg` behind, `surface` rows, Inter text.
struct NXFormStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .scrollContentBackground(.hidden)
            .nxScreenBackground()
            .nxText(NXType.bodyLg)
            .foregroundStyle(NXColor.fg)
            .tint(NXColor.accent)
    }
}

extension View {
    func nxForm() -> some View {
        modifier(NXFormStyle())
    }
}
