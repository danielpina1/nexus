import SwiftUI

// Buttons (MASTER.md §9): primary, secondary, destructive, success, ghost and outline, with the
// shared states — press scale 0.98 (opacity under Reduce Motion), 50 % when disabled, a spinner
// that keeps the width while loading, the focus ring, and a 44 pt touch target.

enum NXButtonKind {
    case primary, secondary, destructive, success, ghost, outline
}

enum NXButtonSize {
    /// 36 pt visual height inside a 44 pt touch target (inline card actions).
    case small
    /// 44 pt.
    case regular
    /// 50 pt (sheet-level actions such as Approve with Face ID).
    case large

    var visualHeight: CGFloat {
        switch self {
        case .small: return NXSize.controlMd
        case .regular: return NXSize.controlLg
        case .large: return 50
        }
    }
}

struct NXButtonStyle: ButtonStyle {
    var kind: NXButtonKind = .secondary
    var size: NXButtonSize = .regular
    var isLoading = false
    var fullWidth = false

    func makeBody(configuration: Configuration) -> some View {
        NXButtonBody(configuration: configuration, kind: kind, size: size, isLoading: isLoading, fullWidth: fullWidth)
    }
}

private struct NXButtonBody: View {
    let configuration: ButtonStyleConfiguration
    let kind: NXButtonKind
    let size: NXButtonSize
    let isLoading: Bool
    let fullWidth: Bool

    @Environment(\.isEnabled) private var isEnabled
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var foreground: Color {
        switch kind {
        case .primary: return NXColor.primaryFg
        // Filled tones use the same dark-on-light rule as primary in dark mode.
        case .destructive, .success: return NXColor.primaryFg
        case .secondary, .outline: return NXColor.fg
        case .ghost: return NXColor.fg2
        }
    }

    private var fill: Color {
        switch kind {
        case .primary: return NXColor.primary
        case .destructive: return NXColor.danger
        case .success: return NXColor.ok
        case .secondary: return configuration.isPressed ? NXColor.surface2 : NXColor.surface
        case .ghost, .outline: return configuration.isPressed ? NXColor.surface2 : Color.clear
        }
    }

    private var border: Color? {
        switch kind {
        case .secondary: return NXColor.borderStrong
        case .outline: return NXColor.borderStrong
        default: return nil
        }
    }

    var body: some View {
        let pressed = configuration.isPressed
        let shape = RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
        let style = size == .large ? NXType.bodyLg.weight(.semibold) : NXType.body.weight(.semibold)
        configuration.label
            .font(style.font)
            .lineLimit(2)
            .multilineTextAlignment(.center)
            .foregroundStyle(foreground)
            .opacity(isLoading ? 0 : 1)
            .overlay {
                if isLoading {
                    ProgressView()
                        .tint(foreground)
                        .accessibilityHidden(true)
                }
            }
            .padding(.horizontal, size == .small ? NXSpace.s3 : NXSpace.s4)
            .frame(maxWidth: fullWidth ? .infinity : nil, minHeight: size.visualHeight)
            .background(fill, in: shape)
            .overlay {
                if let border {
                    shape.strokeBorder(border, lineWidth: 1)
                }
            }
            .opacity(isEnabled ? (pressed && (kind == .primary || kind == .destructive || kind == .success) ? 0.9 : 1) : 0.5)
            .scaleEffect(pressed && !reduceMotion ? 0.98 : 1)
            .animation(reduceMotion ? nil : NXMotion.out(NXMotion.instant), value: pressed)
            .nxFocusRing(cornerRadius: NXRadius.md)
            .frame(minHeight: NXSize.touchMin)
            .contentShape(Rectangle())
    }
}

/// A text (+ icon) button with the kit style. Disabled while loading; VoiceOver hears "Loading".
struct NXButton: View {
    let title: String
    var systemImage: String?
    var kind: NXButtonKind = .secondary
    var size: NXButtonSize = .regular
    var isLoading = false
    var fullWidth = false
    var role: ButtonRole?
    let action: () -> Void

    init(
        _ title: String,
        systemImage: String? = nil,
        kind: NXButtonKind = .secondary,
        size: NXButtonSize = .regular,
        isLoading: Bool = false,
        fullWidth: Bool = false,
        role: ButtonRole? = nil,
        action: @escaping () -> Void
    ) {
        self.title = title
        self.systemImage = systemImage
        self.kind = kind
        self.size = size
        self.isLoading = isLoading
        self.fullWidth = fullWidth
        self.role = role
        self.action = action
    }

    var body: some View {
        Button(role: role, action: action) {
            if let systemImage {
                Label(title, systemImage: systemImage)
            } else {
                Text(title)
            }
        }
        .buttonStyle(NXButtonStyle(kind: kind, size: size, isLoading: isLoading, fullWidth: fullWidth))
        .disabled(isLoading)
        .accessibilityLabel(title)
        .accessibilityValue(isLoading ? Text("Loading") : Text(""))
    }
}

/// Icon-only button: 44 × 44, always with a VoiceOver label.
struct NXIconButton: View {
    let systemImage: String
    let label: String
    var tint: Color = NXColor.fg2
    var isLoading = false
    let action: () -> Void

    init(systemImage: String, label: String, tint: Color = NXColor.fg2, isLoading: Bool = false, action: @escaping () -> Void) {
        self.systemImage = systemImage
        self.label = label
        self.tint = tint
        self.isLoading = isLoading
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                if isLoading {
                    ProgressView()
                } else {
                    Image(systemName: systemImage)
                        .font(.system(size: 17, weight: .medium))
                        .foregroundStyle(tint)
                }
            }
            .frame(width: NXSize.touchMin, height: NXSize.touchMin)
            .contentShape(Rectangle())
        }
        .buttonStyle(NXPlainPressStyle())
        .disabled(isLoading)
        .accessibilityLabel(label)
    }
}

/// Plain style for rows and tiles that are buttons: opacity on press and the focus ring.
struct NXPlainPressStyle: ButtonStyle {
    var cornerRadius: CGFloat = NXRadius.md

    func makeBody(configuration: Configuration) -> some View {
        NXPlainPressBody(configuration: configuration, cornerRadius: cornerRadius)
    }
}

private struct NXPlainPressBody: View {
    let configuration: ButtonStyleConfiguration
    let cornerRadius: CGFloat

    @Environment(\.isEnabled) private var isEnabled

    var body: some View {
        configuration.label
            .opacity(isEnabled ? (configuration.isPressed ? 0.7 : 1) : 0.5)
            .nxFocusRing(cornerRadius: cornerRadius)
    }
}

/// Approve button of an approval card / sheet. Biometric approvals say "Approve with Face ID"
/// and are signed by the Secure Enclave after Face ID.
struct NXApproveButton: View {
    let isBiometric: Bool
    var isLoading = false
    var size: NXButtonSize = .regular
    var fullWidth = false
    let action: () -> Void

    init(isBiometric: Bool, isLoading: Bool = false, size: NXButtonSize = .regular, fullWidth: Bool = false, action: @escaping () -> Void) {
        self.isBiometric = isBiometric
        self.isLoading = isLoading
        self.size = size
        self.fullWidth = fullWidth
        self.action = action
    }

    var body: some View {
        NXButton(
            isBiometric ? "Approve with Face ID" : "Approve",
            systemImage: isBiometric ? "faceid" : "checkmark",
            kind: .success,
            size: size,
            isLoading: isLoading,
            fullWidth: fullWidth,
            action: action
        )
        .accessibilityHint(isBiometric ? "Signs this exact action with Face ID." : "Signs and approves this action.")
    }
}

/// Destructive action that needs a second, explicit tap ("Cancel run" → "Confirm cancel" for
/// 4 s), MASTER.md §9.
struct NXConfirmButton: View {
    let title: String
    let confirmTitle: String
    var systemImage: String?
    var kind: NXButtonKind = .secondary
    var size: NXButtonSize = .regular
    var isLoading = false
    let action: () -> Void

    @State private var armed = false
    @State private var disarmTask: Task<Void, Never>?

    init(_ title: String, confirmTitle: String, systemImage: String? = nil, kind: NXButtonKind = .secondary, size: NXButtonSize = .regular, isLoading: Bool = false, action: @escaping () -> Void) {
        self.title = title
        self.confirmTitle = confirmTitle
        self.systemImage = systemImage
        self.kind = kind
        self.size = size
        self.isLoading = isLoading
        self.action = action
    }

    var body: some View {
        NXButton(
            armed ? confirmTitle : title,
            systemImage: systemImage,
            kind: armed ? .destructive : kind,
            size: size,
            isLoading: isLoading
        ) {
            if armed {
                disarmTask?.cancel()
                armed = false
                action()
            } else {
                armed = true
                disarmTask?.cancel()
                disarmTask = Task {
                    try? await Task.sleep(for: .seconds(4))
                    if !Task.isCancelled { armed = false }
                }
            }
        }
        .accessibilityHint(armed ? "Tap again to confirm." : "Asks to confirm.")
        .onDisappear { disarmTask?.cancel() }
    }
}
