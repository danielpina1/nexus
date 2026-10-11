import NexusCore
import SwiftUI
import UIKit
import UserNotifications

struct SettingsView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(AppLock.self) private var lock
    @Environment(\.dismiss) private var dismiss

    @State private var notificationStatus: UNAuthorizationStatus = .notDetermined
    @State private var confirmUnpair = false
    @State private var devToken = Keychain.string(for: .devToken) ?? ""

    var body: some View {
        @Bindable var lock = lock
        NavigationStack {
            Form {
                Section("Hub") {
                    if let configuration = store.configuration {
                        ForEach(configuration.hubURLs, id: \.self) { url in
                            Text(url.absoluteString).font(.footnote.monospaced())
                        }
                        LabeledContent("Connection", value: store.connection.label)
                        if let deviceID = configuration.deviceID {
                            LabeledContent("Device ID", value: deviceID)
                        }
                        if let paired = configuration.pairedAt {
                            LabeledContent("Paired", value: paired.formatted(date: .abbreviated, time: .shortened))
                        }
                    } else {
                        Text("Not paired").foregroundStyle(.secondary)
                    }
                    SecureField("Dev token (development only)", text: $devToken)
                        .onSubmit {
                            Keychain.setString(devToken.isEmpty ? nil : devToken, for: .devToken)
                            store.reconnect()
                        }
                    Button("Reconnect") { store.reconnect() }
                }

                Section("Devices") {
                    ForEach(store.devices, id: \.id) { device in
                        HStack {
                            VStack(alignment: .leading) {
                                Text(device.name + (device.primary ? " (primary)" : ""))
                                if let seen = device.lastSeen.optionalDate {
                                    Text("Seen \(Format.relative(seen))").font(.caption).foregroundStyle(.secondary)
                                }
                            }
                            Spacer()
                            if device.id == store.configuration?.deviceID {
                                Text("This iPhone").font(.caption).foregroundStyle(.secondary)
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

                Section {
                    Toggle("Face ID on open", isOn: $lock.isEnabled)
                    LabeledContent("Secure Enclave", value: KeyManager.shared.isHardwareBacked ? "Yes" : "No (Simulator — software keys)")
                    if let code = store.configuration?.safetyCode {
                        LabeledContent("Safety code", value: code)
                    }
                    if let keys = try? KeyManager.shared.ensureKeys() {
                        LabeledContent("Auth key", value: WireFormat.keyID(keys.auth))
                            .font(.caption.monospaced())
                        LabeledContent("Approval key", value: WireFormat.keyID(keys.approval))
                            .font(.caption.monospaced())
                        LabeledContent("Encryption key", value: WireFormat.keyID(keys.encryption))
                            .font(.caption.monospaced())
                    }
                } header: {
                    Text("Trust")
                } footer: {
                    Text("Keys never leave this iPhone. Approvals are signed with a Face ID–bound Secure Enclave key and verified by the machine that executes them.")
                }

                Section {
                    Toggle("Demo mode", isOn: Binding(
                        get: { store.isDemo },
                        set: { store.setDemoMode($0) }
                    ))
                } footer: {
                    Text("Shows sample agents, tasks and approvals without a Hub.")
                }

                Section {
                    Button(role: .destructive) {
                        router.showSettings = false
                        router.showKillSwitchConfirmation = true
                    } label: {
                        Label("Kill switch", systemImage: "stop.circle.fill")
                    }
                    Button("Unpair this iPhone", role: .destructive) {
                        confirmUnpair = true
                    }
                }
            }
            .navigationTitle("Settings")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
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
