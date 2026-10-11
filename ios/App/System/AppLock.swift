import LocalAuthentication
import NexusCore
import Observation
import SwiftUI

/// Face ID on open (configurable) with a re-lock timeout (SECURITY.md §4).
@Observable
@MainActor
final class AppLock {
    private(set) var isLocked: Bool
    private(set) var isAuthenticating = false
    var lastError: String?

    /// Re-lock after this long in the background.
    var timeout: TimeInterval = 60
    @ObservationIgnored private var backgroundedAt: Date?

    init() {
        let enabled = AppLock.storedEnabled
        let hasData = (HubConfiguration.load()?.isPaired ?? false)
            || AppGroup.defaults.bool(forKey: AppGroup.Key.demoMode)
        isEnabled = enabled
        isLocked = enabled && hasData
    }

    private static var storedEnabled: Bool {
        // Default ON once paired.
        if AppGroup.defaults.object(forKey: AppGroup.Key.appLockEnabled) == nil { return true }
        return AppGroup.defaults.bool(forKey: AppGroup.Key.appLockEnabled)
    }

    var isEnabled: Bool {
        didSet {
            AppGroup.defaults.set(isEnabled, forKey: AppGroup.Key.appLockEnabled)
            if !isEnabled { isLocked = false }
        }
    }

    func didEnterBackground() {
        backgroundedAt = Date()
    }

    func didBecomeActive() {
        guard isEnabled else {
            isLocked = false
            return
        }
        if let backgroundedAt, Date().timeIntervalSince(backgroundedAt) > timeout {
            isLocked = true
        }
        self.backgroundedAt = nil
        if isLocked && !isAuthenticating {
            Task { await unlock() }
        }
    }

    func unlock() async {
        guard isLocked, !isAuthenticating else { return }
        isAuthenticating = true
        defer { isAuthenticating = false }
        let context = LAContext()
        var error: NSError?
        guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else {
            // No passcode set on the device: nothing to lock with.
            isLocked = false
            return
        }
        do {
            let ok = try await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: "Unlock Nexus")
            if ok {
                isLocked = false
                lastError = nil
            }
        } catch {
            lastError = error.localizedDescription
        }
    }
}

/// Shown over the app while locked.
struct LockView: View {
    let lock: AppLock

    var body: some View {
        ZStack {
            Rectangle().fill(.ultraThickMaterial).ignoresSafeArea()
            VStack(spacing: 16) {
                Image(systemName: "lock.shield.fill")
                    .font(.system(size: 56))
                    .foregroundStyle(.tint)
                Text("Nexus is locked").font(.title2.bold())
                if let error = lock.lastError {
                    Text(error).font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                Button {
                    Task { await lock.unlock() }
                } label: {
                    Label("Unlock with Face ID", systemImage: "faceid")
                }
                .buttonStyle(.borderedProminent)
                .disabled(lock.isAuthenticating)
            }
            .padding()
        }
    }
}

/// Privacy cover in the app switcher.
struct PrivacyCover: View {
    var body: some View {
        ZStack {
            Rectangle().fill(.ultraThickMaterial).ignoresSafeArea()
            Image(systemName: "circle.hexagongrid.fill")
                .font(.system(size: 64))
                .foregroundStyle(.tint)
        }
    }
}
