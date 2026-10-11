import NexusCore
import SwiftUI
import UIKit
import UserNotifications
import WidgetKit

/// Long-lived app services shared by SwiftUI and the UIKit delegate callbacks.
@MainActor
final class AppEnvironment {
    static let shared = AppEnvironment()

    let store: NexusStore
    let router = AppRouter()
    let lock = AppLock()
    let liveActivities = LiveActivityManager()

    private init() {
        store = NexusStore()
        store.onSnapshot = {
            WidgetCenter.shared.reloadAllTimelines()
        }
        store.onTaskChanged = { [weak self] task in
            guard let self else { return }
            let approval = self.store.pendingApprovals.first { $0.taskID == task.id }
            self.liveActivities.update(task: task, approval: approval)
        }
        store.onApprovalChanged = { [weak self] approval in
            guard let self, let task = self.store.tasks[approval.taskID] else { return }
            self.liveActivities.update(task: task, approval: approval.state == .pending ? approval : nil)
        }
    }
}

@main
struct NexusApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @Environment(\.scenePhase) private var scenePhase

    private let env = AppEnvironment.shared

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(env.store)
                .environment(env.router)
                .environment(env.lock)
                .onOpenURL { url in
                    env.router.handle(url: url)
                }
                .onReceive(NotificationCenter.default.publisher(for: .nexusPendingAction)) { _ in
                    env.router.consumePendingAction()
                }
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active:
                if env.store.isPaired || env.store.isDemo {
                    env.lock.didBecomeActive()
                }
                env.router.consumePendingAction()
                env.store.foreground()
                Task { await env.store.refresh() }
            case .background:
                env.lock.didEnterBackground()
                env.store.writeSnapshot()
            default:
                break
            }
        }
    }
}

/// UIKit hooks: push registration, notification delegate, Live Activity token observation.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        NotificationCategories.register()
        MainActor.assumeIsolated {
            let env = AppEnvironment.shared
            if env.store.isPaired || env.store.isDemo {
                env.store.start()
            }
            if env.store.isPaired {
                env.liveActivities.start()
                Task { await NotificationCategories.requestAuthorizationAndRegister() }
            }
        }
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Task {
            try? await NexusActions.shared()?.registerPushToken(deviceToken, environment: PushEnvironment.current)
        }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        // Simulator / missing entitlement: notifications simply stay local.
    }

    /// Silent pushes (`content-available`) refresh the store and widgets.
    func application(_ application: UIApplication, didReceiveRemoteNotification userInfo: [AnyHashable: Any]) async -> UIBackgroundFetchResult {
        await AppEnvironment.shared.store.refresh()
        await MainActor.run { AppEnvironment.shared.store.writeSnapshot() }
        return .newData
    }

    // MARK: UNUserNotificationCenterDelegate

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        await NotificationResponseHandler.handle(
            response,
            store: AppEnvironment.shared.store,
            router: AppEnvironment.shared.router
        )
    }
}
