import NexusCore
import SwiftUI

struct RootView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router
    @Environment(AppLock.self) private var lock
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        @Bindable var router = router
        Group {
            if store.isPaired || store.isDemo {
                MainTabView()
            } else {
                OnboardingView()
            }
        }
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
    }
}

struct MainTabView: View {
    @Environment(AppRouter.self) private var router

    var body: some View {
        @Bindable var router = router
        TabView(selection: $router.tab) {
            Tab("Home", systemImage: "house.fill", value: AppRouter.Tab.home) {
                HomeView()
            }
            Tab("Chat", systemImage: "bubble.left.and.bubble.right.fill", value: AppRouter.Tab.chat) {
                ChatListView()
            }
            Tab("Tasks", systemImage: "list.bullet.rectangle.portrait.fill", value: AppRouter.Tab.tasks) {
                TasksView()
            }
            Tab("Vault", systemImage: "books.vertical.fill", value: AppRouter.Tab.vault) {
                VaultView()
            }
            Tab("Machines", systemImage: "desktopcomputer", value: AppRouter.Tab.machines) {
                MachinesView()
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
