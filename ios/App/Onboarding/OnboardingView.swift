import NexusCore
import SwiftUI

/// First run: pair with a Hub via QR (secure path), connect with a dev token (local dev), or
/// explore demo mode.
struct OnboardingView: View {
    @Environment(NexusStore.self) private var store

    @State private var path: [OnboardingStep] = []
    @State private var showScanner = false
    @State private var pasteText = ""
    @State private var parseError: String?

    enum OnboardingStep: Hashable {
        case pair(PairingPayloadBox)
        case devToken
    }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                VStack(spacing: 24) {
                    Image(systemName: "circle.hexagongrid.fill")
                        .font(.system(size: 72))
                        .foregroundStyle(.tint)
                        .padding(.top, 40)
                    Text("Nexus").font(.largeTitle.bold())
                    Text("Your agents, machines and vault — controlled from your phone, approved with Face ID.")
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)

                    VStack(spacing: 12) {
                        Button {
                            showScanner = true
                        } label: {
                            Label("Scan pairing code", systemImage: "qrcode.viewfinder")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.large)

                        Text("Run `nexusd pair --device` on your Hub, or open Security → Pair device on the web dashboard.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                    }

                    GroupBox("Or paste the pairing link") {
                        TextField("nexus://pair?…", text: $pasteText, axis: .vertical)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .font(.footnote.monospaced())
                        Button("Continue") { handle(code: pasteText) }
                            .disabled(pasteText.isEmpty)
                            .frame(maxWidth: .infinity, alignment: .trailing)
                    }

                    if let parseError {
                        ErrorBanner(message: parseError) { self.parseError = nil }
                    }

                    VStack(spacing: 8) {
                        NavigationLink(value: OnboardingStep.devToken) {
                            Label("Connect with a dev token", systemImage: "hammer")
                        }
                        Button {
                            store.setDemoMode(true)
                        } label: {
                            Label("Explore demo mode", systemImage: "play.rectangle")
                        }
                    }
                    .font(.subheadline)
                }
                .padding()
            }
            .navigationDestination(for: OnboardingStep.self) { step in
                switch step {
                case .pair(let box):
                    PairingView(payload: box.payload)
                case .devToken:
                    DevTokenView()
                }
            }
            .sheet(isPresented: $showScanner) {
                NavigationStack {
                    QRScannerView { code in
                        showScanner = false
                        handle(code: code)
                    }
                    .ignoresSafeArea()
                    .navigationTitle("Scan pairing code")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Cancel") { showScanner = false }
                        }
                    }
                }
            }
        }
    }

    private func handle(code: String) {
        do {
            let payload = try PairingPayload(string: code)
            parseError = nil
            path.append(.pair(PairingPayloadBox(payload: payload)))
        } catch {
            parseError = error.localizedDescription
        }
    }
}

/// Hashable box so the payload can travel through a NavigationStack path.
struct PairingPayloadBox: Hashable {
    let payload: PairingPayload

    static func == (lhs: Self, rhs: Self) -> Bool { lhs.payload == rhs.payload }
    func hash(into hasher: inout Hasher) {
        hasher.combine(payload.token)
        hasher.combine(payload.secret)
    }
}

/// Pairs with the Hub, then shows the emoji safety code to compare with the Hub's screen.
struct PairingView: View {
    let payload: PairingPayload

    @Environment(NexusStore.self) private var store
    @Environment(\.dismiss) private var dismiss

    @State private var deviceName = UIDevice.current.name
    @State private var result: PairingResult?
    @State private var isPairing = false
    @State private var errorMessage: String?

    var body: some View {
        Form {
            Section("Hub") {
                ForEach(payload.hubURLs, id: \.self) { url in
                    Label(url.absoluteString, systemImage: "server.rack")
                        .font(.footnote.monospaced())
                }
                if let ca = payload.caFingerprint {
                    LabeledContent("CA fingerprint") {
                        Text(String(ca.prefix(16)) + "…").font(.caption.monospaced())
                    }
                } else {
                    Label("No CA fingerprint — development Hub", systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.orange)
                        .font(.footnote)
                }
            }

            if !KeyManager.shared.isHardwareBacked {
                Section {
                    Label("Secure Enclave unavailable (Simulator): keys are software keys. Do not use this device for real approvals.", systemImage: "exclamationmark.shield")
                        .foregroundStyle(.orange)
                        .font(.footnote)
                }
            }

            if let result {
                Section("Safety code") {
                    Text(result.safetyCode)
                        .font(.system(size: 40))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 8)
                    Text("Check that your Hub shows exactly the same emoji, in the same order. If not, cancel — someone may be intercepting the pairing.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
                Section {
                    Button {
                        confirm(result)
                    } label: {
                        Label("They match — finish pairing", systemImage: "checkmark.seal.fill")
                    }
                    Button("They don't match", role: .destructive) {
                        KeyManager.shared.resetAll()
                        self.result = nil
                        dismiss()
                    }
                }
            } else {
                Section("This iPhone") {
                    TextField("Device name", text: $deviceName)
                }
                Section {
                    Button {
                        Task { await pair() }
                    } label: {
                        HStack {
                            Label("Pair with Hub", systemImage: "link")
                            if isPairing { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(isPairing)
                } footer: {
                    Text("Creates keys in the Secure Enclave and proves to the Hub that you scanned its code.")
                }
            }

            if let errorMessage {
                Section {
                    ErrorBanner(message: errorMessage)
                }
            }
        }
        .navigationTitle("Pair")
    }

    private func pair() async {
        isPairing = true
        errorMessage = nil
        defer { isPairing = false }
        do {
            result = try await PairingService().pair(payload: payload, deviceName: deviceName)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func confirm(_ result: PairingResult) {
        PairingService().confirm(result)
        Keychain.delete(.devToken)
        if store.isDemo { store.setDemoMode(false) }
        store.reconnect()
        AppEnvironment.shared.liveActivities.start()
        Task { await NotificationCategories.requestAuthorizationAndRegister() }
        dismiss()
    }
}

/// Local development: hub URL + `security.dev_token` from nexus.yaml.
struct DevTokenView: View {
    @Environment(NexusStore.self) private var store

    @State private var hubURL = "http://192.168.1.10:7700"
    @State private var token = ""
    @State private var useJSON = false
    @State private var errorMessage: String?

    var body: some View {
        Form {
            Section {
                TextField("Hub URL", text: $hubURL)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                SecureField("Dev token", text: $token)
                Toggle("JSON codec (debugging)", isOn: $useJSON)
            } footer: {
                Text("Development only: uses `security.dev_token` from nexus.yaml. Approvals still need device keys, so pair with a QR code for real use.")
            }
            if let errorMessage {
                Section { ErrorBanner(message: errorMessage) }
            }
            Section {
                Button("Connect") { connect() }
                    .disabled(hubURL.isEmpty || token.isEmpty)
            }
        }
        .navigationTitle("Dev token")
    }

    private func connect() {
        guard let url = URL(string: hubURL.trimmingCharacters(in: .whitespaces)), url.scheme != nil else {
            errorMessage = "Enter a full URL, e.g. http://192.168.1.10:7700"
            return
        }
        try? KeyManager.shared.ensureKeys()
        Keychain.setString(token, for: .devToken)
        HubConfiguration(hubURLs: [url], deviceName: UIDevice.current.name, useJSONCodec: useJSON).save()
        if store.isDemo { store.setDemoMode(false) }
        store.reconnect()
    }
}
