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

/// Shown over the app while locked. Glass over the content (solid under Reduce Transparency).
struct LockView: View {
    let lock: AppLock

    var body: some View {
        ZStack {
            Rectangle()
                .fill(.clear)
                .nxGlass(showsBorder: false)
                .ignoresSafeArea()
            VStack(spacing: NXSpace.s4) {
                NXIconTile(systemImage: "lock.shield", tone: .accent, size: 64)
                Text("Nexus is locked")
                    .nx(NXType.title1)
                    .foregroundStyle(NXColor.fg)
                    .accessibilityAddTraits(.isHeader)
                if let error = lock.lastError {
                    Text(error)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                        .multilineTextAlignment(.center)
                }
                NXButton("Unlock with Face ID", systemImage: "faceid", kind: .primary, size: .large, isLoading: lock.isAuthenticating) {
                    Task { await lock.unlock() }
                }
            }
            .padding(NXSpace.s6)
        }
    }
}

/// Privacy cover in the app switcher.
struct PrivacyCover: View {
    var body: some View {
        ZStack {
            NXColor.bg.ignoresSafeArea()
            NXIconTile(systemImage: "circle.hexagongrid.fill", tone: .accent, size: 72)
        }
        .accessibilityHidden(true)
    }
}
