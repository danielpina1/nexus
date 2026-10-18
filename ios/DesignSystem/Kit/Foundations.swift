import SwiftUI

// Foundations of the Nexus component kit (design/MASTER.md): tones, text roles, cards, focus
// ring, glass, dividers and icon tiles. Shared by the app and the widget extension, so nothing
// here may depend on app state.

// MARK: - Tone

/// Semantic tone of a badge, card border, icon or meter (MASTER.md §2).
enum NXTone: Hashable {
    case neutral, accent, ok, warn, danger, info

    /// Text / icon colour (≥ 4.5:1 on `surface` and on `soft`).
    var color: Color {
        switch self {
        case .neutral: return NXColor.fg2
        case .accent: return NXColor.accent
        case .ok: return NXColor.ok
        case .warn: return NXColor.warn
        case .danger: return NXColor.danger
        case .info: return NXColor.info
        }
    }

    /// Tinted background for badges and icon tiles.
    var soft: Color {
        switch self {
        case .neutral: return NXColor.surface2
        case .accent: return NXColor.accentSoft
        case .ok: return NXColor.okSoft
        case .warn: return NXColor.warnSoft
        case .danger: return NXColor.dangerSoft
        case .info: return NXColor.infoSoft
        }
    }

    /// Border for cards that wait on you (warn/40, danger/40, info/35); others stay quiet.
    var urgentBorder: Color {
        switch self {
        case .warn: return NXColor.warn.opacity(0.4)
        case .danger: return NXColor.danger.opacity(0.4)
        case .info: return NXColor.info.opacity(0.35)
        case .accent: return NXColor.accent.opacity(0.4)
        case .ok, .neutral: return NXColor.border
        }
    }
}

// MARK: - Text roles

extension View {
    /// Applies a type role (font with Dynamic Type, line spacing, tracking).
    func nxText(_ style: NXTextStyle) -> some View {
        font(style.font)
            .lineSpacing(style.lineSpacing)
            .tracking(style.tracking)
    }
}

extension Text {
    /// Type role on a `Text` (keeps `Text` concatenation working).
    func nx(_ style: NXTextStyle) -> Text {
        font(style.font).tracking(style.tracking)
    }
}

// MARK: - Card

/// Flat card: surface, hairline border, card shadow, radius lg (MASTER.md §5, §9).
struct NXCardModifier: ViewModifier {
    var padding: CGFloat?
    var urgency: NXTone?
    var isSelected = false

    @Environment(\.colorScheme) private var colorScheme

    func body(content: Content) -> some View {
        let shadow = colorScheme == .dark ? NXShadow.card.dark : NXShadow.card.light
        let shape = RoundedRectangle(cornerRadius: NXRadius.lg, style: .continuous)
        content
            .padding(padding ?? NXSpace.s4)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(NXColor.surface, in: shape)
            .overlay {
                shape.strokeBorder(isSelected ? NXColor.accent : (urgency?.urgentBorder ?? NXColor.border), lineWidth: isSelected ? 2 : 1)
            }
            .shadow(color: NXShadow.card.color, radius: shadow.radius, x: shadow.x, y: shadow.y)
    }
}

extension View {
    /// Wraps the view in a card. `urgency` tints the border of things waiting on you.
    func nxCard(padding: CGFloat? = nil, urgency: NXTone? = nil, isSelected: Bool = false) -> some View {
        modifier(NXCardModifier(padding: padding, urgency: urgency, isSelected: isSelected))
    }

    /// App background (bg token) behind scroll views and forms.
    func nxScreenBackground() -> some View {
        background(NXColor.bg.ignoresSafeArea())
    }
}

// MARK: - Focus ring

/// 2 pt `ring` outline with a 2 pt offset while the control has keyboard focus (hardware
/// keyboard / Full Keyboard Access). The system draws its own indicator too; this one matches
/// the web's `:focus-visible` ring.
struct NXFocusRing: ViewModifier {
    var cornerRadius: CGFloat

    @Environment(\.isFocused) private var isFocused

    func body(content: Content) -> some View {
        content.overlay {
            if isFocused {
                RoundedRectangle(cornerRadius: cornerRadius + 2, style: .continuous)
                    .strokeBorder(NXColor.ring, lineWidth: 2)
                    .padding(-4)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
        }
    }
}

extension View {
    func nxFocusRing(cornerRadius: CGFloat = NXRadius.md) -> some View {
        modifier(NXFocusRing(cornerRadius: cornerRadius))
    }
}

// MARK: - Glass (chrome only)

/// Translucent chrome for bars and sheets (MASTER.md §6). Falls back to solid `surface` under
/// Reduce Transparency. Never use it on content cards.
struct NXGlass: ViewModifier {
    var cornerRadius: CGFloat = 0
    var showsBorder = true

    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        content
            .background {
                if reduceTransparency {
                    shape.fill(NXColor.surface)
                } else {
                    shape.fill(.ultraThinMaterial)
                        .overlay(shape.fill(NXColor.glass))
                }
            }
            .overlay {
                if showsBorder {
                    shape.strokeBorder(NXColor.glassBorder, lineWidth: 1)
                }
            }
    }
}

extension View {
    func nxGlass(cornerRadius: CGFloat = 0, showsBorder: Bool = true) -> some View {
        modifier(NXGlass(cornerRadius: cornerRadius, showsBorder: showsBorder))
    }
}

/// Sheet background: glass, or solid surface under Reduce Transparency.
struct NXSheetBackground: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    func body(content: Content) -> some View {
        content.presentationBackground(reduceTransparency ? AnyShapeStyle(NXColor.surface) : AnyShapeStyle(.regularMaterial))
    }
}

extension View {
    func nxSheetBackground() -> some View {
        modifier(NXSheetBackground())
    }
}

// MARK: - Divider, icon tile, dots

/// Hairline divider in the `border` colour.
struct NXDivider: View {
    var inset: CGFloat = 0

    var body: some View {
        Rectangle()
            .fill(NXColor.border)
            .frame(height: 1)
            .padding(.leading, inset)
            .accessibilityHidden(true)
    }
}

/// Rounded tile with a tinted SF Symbol (agents, teams, empty states). Decorative.
struct NXIconTile: View {
    let systemImage: String
    var tint: Color = NXColor.muted
    var background: Color = NXColor.surface2
    var size: CGFloat = 32

    init(systemImage: String, tone: NXTone, size: CGFloat = 32) {
        self.systemImage = systemImage
        self.tint = tone.color
        self.background = tone.soft
        self.size = size
    }

    init(systemImage: String, tint: Color, background: Color, size: CGFloat = 32) {
        self.systemImage = systemImage
        self.tint = tint
        self.background = background
        self.size = size
    }

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(size: size * 0.46, weight: .semibold))
            .foregroundStyle(tint)
            .frame(width: size, height: size)
            .background(background, in: RoundedRectangle(cornerRadius: size >= 40 ? NXRadius.lg : NXRadius.md, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// Status dot; `pulse` breathes slowly (2.4 s) unless Reduce Motion is on.
struct NXDot: View {
    var color: Color
    var size: CGFloat = 8
    var pulse = false

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dimmed = false

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: size, height: size)
            .opacity(pulse && dimmed ? 0.35 : 1)
            .onAppear {
                guard pulse, !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                    dimmed = true
                }
            }
            .accessibilityHidden(true)
    }
}

// MARK: - Formatting

enum NXFormat {
    /// "4m 12s", "1h 04m", "12s" (web formatDuration).
    static func duration(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds.rounded()))
        if s >= 3600 {
            return "\(s / 3600)h \(String(format: "%02d", (s % 3600) / 60))m"
        }
        if s >= 60 {
            return "\(s / 60)m \(String(format: "%02d", s % 60))s"
        }
        return "\(s)s"
    }

    /// Spoken form of `duration` for VoiceOver.
    static func spokenDuration(_ seconds: TimeInterval) -> String {
        let formatter = DateComponentsFormatter()
        formatter.unitsStyle = .full
        formatter.allowedUnits = seconds >= 3600 ? [.hour, .minute] : [.minute, .second]
        return formatter.string(from: max(0, seconds)) ?? duration(seconds)
    }

    static func money(cents: Int, currency: String = "EUR") -> String {
        (Decimal(cents) / 100).formatted(.currency(code: currency.isEmpty ? "EUR" : currency))
    }

    static func relative(_ date: Date?) -> String {
        guard let date else { return "" }
        return date.formatted(.relative(presentation: .named))
    }
}
