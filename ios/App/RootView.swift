import NexusCore
import SwiftUI

struct RootView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(AppLock.self) private var lock
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage(AppAppearance.storageKey) private var appearance: AppAppearance = .system

    var body: some View {
        @Bindable var router = router
        Group {
            if store.isPaired || store.isDemo {
                MainTabView()
            } else {
                OnboardingView()
            }
        }
        .tint(NXColor.accent)
        .modifier(ToastHost())
        .sheet(item: $router.approval) { route in
            ApprovalSheet(approvalID: route.id)
        }
        .sheet(isPresented: $router.showCapture) {
            CaptureView()
        }
        .sheet(isPresented: $router.showSettings) {
            SettingsView()
        }
        .sheet(item: $router.pendingPairing.asIdentifiable) { item in
            NavigationStack {
                PairingView(payload: item.payload)
            }
        }
        .modifier(KillSwitchConfirmation(isPresented: $router.showKillSwitchConfirmation))
        .overlay {
            if (store.isPaired || store.isDemo) && lock.isLocked {
                LockView(lock: lock)
            } else if scenePhase != .active {
                PrivacyCover()
            }
        }
        // Light / System / Dark override; applies to sheets and alerts of this window too.
        .preferredColorScheme(appearance.colorScheme)
    }
}

/// Phone tab bar like the web (Home, Inbox, Chat, Work, More). Bars are glass (Chrome
/// appearance); badges announce what waits.
struct MainTabView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        @Bindable var router = router
        TabView(selection: $router.tab) {
            Tab("Home", systemImage: "house", value: AppRouter.Tab.home) {
                NavigationStack(path: $router.homePath) {
                    HomeView()
                        .nexusDestinations()
                }
            }
            Tab("Inbox", systemImage: "tray", value: AppRouter.Tab.inbox) {
                NavigationStack(path: $router.inboxPath) {
                    InboxView()
                        .nexusDestinations()
                }
            }
            .badge(store.inbox.count)
            Tab("Chat", systemImage: "bubble.left", value: AppRouter.Tab.chat) {
                ChatListView()
            }
            Tab("Work", systemImage: "square.stack", value: AppRouter.Tab.work) {
                NavigationStack(path: $router.workPath) {
                    WorkView()
                        .nexusDestinations()
                }
            }
            Tab("More", systemImage: "ellipsis", value: AppRouter.Tab.more) {
                NavigationStack(path: $router.morePath) {
                    MoreView()
                        .nexusDestinations()
                }
            }
        }
    }
}

/// Kill switch confirmation dialog (used from Settings, the Control Center control and Siri).
struct KillSwitchConfirmation: ViewModifier {
    @Environment(NexusStore.self) private var store
    @Binding var isPresented: Bool
    @State private var result: String?

    func body(content: Content) -> some View {
        content
            .confirmationDialog("Stop everything?", isPresented: $isPresented, titleVisibility: .visible) {
                Button("Stop all agents and pause triggers", role: .destructive) {
                    Task {
                        do {
                            let count = try await store.stopAll()
                            result = "Stopped \(count) task\(count == 1 ? "" : "s"). Triggers paused."
                        } catch {
                            result = error.localizedDescription
                        }
                    }
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Cancels every running task, pauses all triggers and revokes run tokens.")
            }
            .alert("Kill switch", isPresented: Binding(get: { result != nil }, set: { if !$0 { result = nil } })) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(result ?? "")
            }
    }
}

/// Lets `.sheet(item:)` present a non-Identifiable optional payload.
struct IdentifiedPairing: Identifiable {
    let payload: PairingPayload
    var id: String { payload.token }
}

extension Binding where Value == PairingPayload? {
    var asIdentifiable: Binding<IdentifiedPairing?> {
        Binding<IdentifiedPairing?>(
            get: { wrappedValue.map { IdentifiedPairing(payload: $0) } },
            set: { newValue in if newValue == nil { wrappedValue = nil } }
        )
    }
}
