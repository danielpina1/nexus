import NexusCore
import Observation
import SwiftUI
import UIKit

/// Light / System / Dark override (MASTER.md §2). Stored under the same key as the web
/// (`nexus.settings.theme`); System follows the iOS appearance.
enum AppAppearance: String, CaseIterable, Identifiable {
    case light, system, dark

    static let storageKey = "nexus.settings.theme"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .light: return "Light"
        case .system: return "System"
        case .dark: return "Dark"
        }
    }

    var systemImage: String {
        switch self {
        case .light: return "sun.max"
        case .system: return "circle.lefthalf.filled"
        case .dark: return "moon"
        }
    }

    /// nil = follow the system.
    var colorScheme: ColorScheme? {
        switch self {
        case .light: return .light
        case .system: return nil
        case .dark: return .dark
        }
    }
}

/// Light / System / Dark segmented control (Settings → Appearance, More).
struct AppearancePicker: View {
    @AppStorage(AppAppearance.storageKey) private var appearance: AppAppearance = .system

    var body: some View {
        Picker("Appearance", selection: $appearance) {
            ForEach(AppAppearance.allCases) { option in
                Text(option.title).tag(option)
            }
        }
        .pickerStyle(.segmented)
        .frame(minHeight: NXSize.touchMin)
        .accessibilityLabel("Appearance")
    }
}

/// UIKit chrome (navigation bars, tab bar) in the design system: Montserrat titles, glass bars
/// (system materials, which turn opaque under Reduce Transparency), `bg` behind large titles.
enum ChromeAppearance {
    @MainActor
    static func apply() {
        let titleFont = UIFontMetrics(forTextStyle: .headline)
            .scaledFont(for: UIFont(name: NXFontFace.name(.heading, .semibold), size: 17) ?? .preferredFont(forTextStyle: .headline))
        let largeTitleFont = UIFontMetrics(forTextStyle: .largeTitle)
            .scaledFont(for: UIFont(name: NXFontFace.name(.heading, .semibold), size: 30) ?? .preferredFont(forTextStyle: .largeTitle))
        let bodyFont = UIFontMetrics(forTextStyle: .body)
            .scaledFont(for: UIFont(name: NXFontFace.name(.sans, .medium), size: 16) ?? .preferredFont(forTextStyle: .body))

        let glass = UINavigationBarAppearance()
        glass.configureWithDefaultBackground()
        glass.backgroundEffect = UIBlurEffect(style: .systemUltraThinMaterial)
        glass.backgroundColor = NXUIColor.glass
        glass.shadowColor = NXUIColor.glassBorder
        glass.titleTextAttributes = [.font: titleFont, .foregroundColor: NXUIColor.fg]
        glass.largeTitleTextAttributes = [.font: largeTitleFont, .foregroundColor: NXUIColor.fg]
        glass.buttonAppearance.normal.titleTextAttributes = [.font: bodyFont]

        let edge = UINavigationBarAppearance()
        edge.configureWithTransparentBackground()
        edge.backgroundColor = NXUIColor.bg
        edge.titleTextAttributes = glass.titleTextAttributes
        edge.largeTitleTextAttributes = glass.largeTitleTextAttributes
        edge.buttonAppearance.normal.titleTextAttributes = [.font: bodyFont]

        let navigationBar = UINavigationBar.appearance()
        navigationBar.standardAppearance = glass
        navigationBar.compactAppearance = glass
        navigationBar.scrollEdgeAppearance = edge
        navigationBar.tintColor = NXUIColor.accent

        let tabBar = UITabBarAppearance()
        tabBar.configureWithDefaultBackground()
        tabBar.backgroundEffect = UIBlurEffect(style: .systemUltraThinMaterial)
        tabBar.backgroundColor = NXUIColor.glass
        tabBar.shadowColor = NXUIColor.glassBorder
        let tabFont = UIFont(name: NXFontFace.name(.sans, .medium), size: 10.5) ?? .systemFont(ofSize: 10.5, weight: .medium)
        for item in [tabBar.stackedLayoutAppearance, tabBar.inlineLayoutAppearance, tabBar.compactInlineLayoutAppearance] {
            item.normal.iconColor = NXUIColor.muted
            item.normal.titleTextAttributes = [.font: tabFont, .foregroundColor: NXUIColor.muted]
            item.selected.iconColor = NXUIColor.accent
            item.selected.titleTextAttributes = [.font: tabFont, .foregroundColor: NXUIColor.fg]
        }
        UITabBar.appearance().standardAppearance = tabBar
        UITabBar.appearance().scrollEdgeAppearance = tabBar

        UISegmentedControl.appearance().selectedSegmentTintColor = NXUIColor.surface
        UISegmentedControl.appearance().backgroundColor = NXUIColor.surface2
        let segmentFont = UIFontMetrics(forTextStyle: .subheadline)
            .scaledFont(for: UIFont(name: NXFontFace.name(.sans, .medium), size: 13) ?? .preferredFont(forTextStyle: .subheadline))
        UISegmentedControl.appearance().setTitleTextAttributes([.font: segmentFont, .foregroundColor: NXUIColor.fg2], for: .normal)
        UISegmentedControl.appearance().setTitleTextAttributes([.font: segmentFont, .foregroundColor: NXUIColor.fg], for: .selected)
    }
}

/// App-wide toasts (MASTER.md §9): one at a time, error toasts last 8 s.
@Observable
@MainActor
final class ToastCenter {
    private(set) var current: NXToastModel?
    @ObservationIgnored private var action: (() -> Void)?
    @ObservationIgnored private var hideTask: Task<Void, Never>?

    func success(_ title: String, _ message: String? = nil) {
        show(NXToastModel(kind: .success, title: title, message: message))
    }

    func info(_ title: String, _ message: String? = nil) {
        show(NXToastModel(kind: .info, title: title, message: message))
    }

    func error(_ title: String, _ error: Error, retry: (() -> Void)? = nil) {
        self.error(title, message: error.localizedDescription, retry: retry)
    }

    func error(_ title: String, message: String, retry: (() -> Void)? = nil) {
        show(NXToastModel(kind: .error, title: title, message: message, actionTitle: retry == nil ? nil : "Retry"), action: retry)
    }

    func show(_ toast: NXToastModel, action: (() -> Void)? = nil) {
        current = toast
        self.action = action
        hideTask?.cancel()
        let id = toast.id
        hideTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(toast.duration))
            guard !Task.isCancelled, let self, self.current?.id == id else { return }
            self.dismiss()
        }
    }

    func performAction() {
        let action = self.action
        dismiss()
        action?()
    }

    func dismiss() {
        hideTask?.cancel()
        current = nil
        action = nil
    }
}

/// Bottom toast host (above the tab bar).
struct ToastHost: ViewModifier {
    @Environment(ToastCenter.self) private var toasts
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        content.overlay(alignment: .bottom) {
            if let toast = toasts.current {
                NXToastView(
                    toast: toast,
                    onAction: toast.actionTitle == nil ? nil : { toasts.performAction() },
                    onDismiss: { toasts.dismiss() }
                )
                .padding(.horizontal, NXSpace.s4)
                .padding(.bottom, 64)
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                .id(toast.id)
            }
        }
        .animation(reduceMotion ? nil : NXMotion.out(NXMotion.base), value: toasts.current)
    }
}
