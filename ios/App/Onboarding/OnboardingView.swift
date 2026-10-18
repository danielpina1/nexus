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
                VStack(spacing: NXSpace.s6) {
                    VStack(spacing: NXSpace.s3) {
                        NXIconTile(systemImage: "circle.hexagongrid.fill", tone: .accent, size: 72)
                            .padding(.top, NXSpace.s10)
                        Text("Nexus")
                            .font(NXTextStyle(family: .heading, weight: .bold, size: 34, relativeTo: .largeTitle, lineSpacing: 0, tracking: -0.34).font)
                            .foregroundStyle(NXColor.fg)
                            .accessibilityAddTraits(.isHeader)
                        Text("Your agents, machines and vault — controlled from your phone, approved with Face ID.")
                            .nx(NXType.bodyLg)
                            .multilineTextAlignment(.center)
                            .foregroundStyle(NXColor.fg2)
                    }

                    VStack(spacing: NXSpace.s3) {
                        NXButton("Scan pairing code", systemImage: "qrcode.viewfinder", kind: .primary, size: .large, fullWidth: true) {
                            showScanner = true
                        }

                        Text("Run `nexusd pair --device` on your Hub, or open Security → Pair device on the web dashboard.")
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.muted)
                            .multilineTextAlignment(.center)

                        PairAtHomeHint()
                    }

                    VStack(alignment: .leading, spacing: NXSpace.s3) {
                        Text("Or paste the pairing link")
                            .nx(NXType.body.weight(.semibold))
                            .foregroundStyle(NXColor.fg)
                        NXTextField(placeholder: "nexus://pair?…", text: $pasteText, axis: .vertical, mono: true)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        NXButton("Continue", kind: .secondary) { handle(code: pasteText) }
                            .disabled(pasteText.isEmpty)
                            .frame(maxWidth: .infinity, alignment: .trailing)
                    }
                    .nxCard()

                    if let parseError {
                        ErrorBanner(message: parseError) { self.parseError = nil }
                    }

                    VStack(spacing: NXSpace.s1) {
                        NavigationLink(value: OnboardingStep.devToken) {
                            Label("Connect with a dev token", systemImage: "hammer")
                        }
                        .buttonStyle(NXButtonStyle(kind: .ghost))
                        NXButton("Explore demo mode", systemImage: "play.rectangle", kind: .ghost) {
                            store.setDemoMode(true)
                        }
                    }
                }
                .padding(NXSpace.s4)
            }
            .nxScreenBackground()
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
                        .nxText(NXType.mono)
                }
                if let ca = payload.caFingerprint {
                    LabeledContent("CA fingerprint") {
                        Text(String(ca.prefix(16)) + "…").nx(NXType.mono)
                    }
                } else {
                    Label("No CA fingerprint — development Hub", systemImage: "exclamationmark.triangle")
                        .foregroundStyle(NXColor.warn)
                        .nxText(NXType.footnote)
                }
            }
            .listRowBackground(NXColor.surface)

            if !KeyManager.shared.isHardwareBacked {
                Section {
                    Label("Secure Enclave unavailable (Simulator): keys are software keys. Do not use this device for real approvals.", systemImage: "exclamationmark.shield")
                        .foregroundStyle(NXColor.warn)
                        .nxText(NXType.footnote)
                }
                .listRowBackground(NXColor.surface)
            }

            if let result {
                Section("Safety code") {
                    Text(result.safetyCode)
                        .font(.system(size: 40))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, NXSpace.s2)
                        .accessibilityLabel("Safety code: \(result.safetyCode)")
                    Text("Check that your Hub shows exactly the same emoji, in the same order. If not, cancel — someone may be intercepting the pairing.")
                        .nxText(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                }
                .listRowBackground(NXColor.surface)
                Section {
                    NXButton("They match — finish pairing", systemImage: "checkmark.seal.fill", kind: .success, fullWidth: true) {
                        confirm(result)
                    }
                    NXButton("They don't match", kind: .secondary, fullWidth: true, role: .destructive) {
                        KeyManager.shared.resetAll()
                        self.result = nil
                        dismiss()
                    }
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            } else {
                Section("This iPhone") {
                    TextField("Device name", text: $deviceName)
                }
                .listRowBackground(NXColor.surface)
                Section {
                    NXButton("Pair with Hub", systemImage: "link", kind: .primary, isLoading: isPairing, fullWidth: true) {
                        Task { await pair() }
                    }
                } footer: {
                    Text("Creates keys in the Secure Enclave and proves to the Hub that you scanned its code. Pair at home on Wi-Fi — away from home, your UniFi VPN takes over afterwards.")
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            }

            if let errorMessage {
                Section {
                    ErrorBanner(message: errorMessage)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            }
        }
        .nxForm()
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
            .listRowBackground(NXColor.surface)
            if let errorMessage {
                Section { ErrorBanner(message: errorMessage) }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
            }
            Section {
                NXButton("Connect", kind: .primary, fullWidth: true) { connect() }
                    .disabled(hubURL.isEmpty || token.isEmpty)
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
        }
        .nxForm()
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

/// "Pair at home on Wi-Fi" (docs/NETWORK.md §3): the Hub is only reachable at home or through
/// the UniFi VPN, and the first contact should happen on the trusted home network.
struct PairAtHomeHint: View {
    var body: some View {
        HStack(alignment: .top, spacing: NXSpace.s3) {
            NXIconTile(systemImage: "house", tone: .accent, size: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text("Pair at home on Wi-Fi")
                    .nx(NXType.body.weight(.semibold))
                    .foregroundStyle(NXColor.fg)
                Text("Afterwards the same Hub address works everywhere: directly at home, and through your UniFi VPN (WireGuard or Teleport) when you're out.")
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
            Spacer(minLength: 0)
        }
        .nxCard()
        .accessibilityElement(children: .combine)
    }
}
