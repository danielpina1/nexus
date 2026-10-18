// GENERATED from design/tokens.json by scripts/gen-ios-tokens.mjs — do not edit.
// Regenerate with `make ios-tokens`. Guide: design/MASTER.md.
//
// Compiled into the app and the widget extension (Live Activities, widgets). Colours resolve
// per trait collection, so they follow the system appearance and the in-app Light / System /
// Dark override (`.preferredColorScheme`). Fonts scale with Dynamic Type (`relativeTo:`).

import SwiftUI
import UIKit

// MARK: - Colours

/// Semantic colours as dynamic `UIColor`s (UIKit appearance APIs, tab bars, …).
enum NXUIColor {
    /// `bg`: light #F8FAFC, dark #0A0A0B
    static let bg = dynamic(rgb(0xF8FAFC, 1.000), rgb(0x0A0A0B, 1.000))
    /// `surface`: light #FFFFFF, dark #111113
    static let surface = dynamic(rgb(0xFFFFFF, 1.000), rgb(0x111113, 1.000))
    /// `surface-2`: light #F1F5FD, dark #18181B
    static let surface2 = dynamic(rgb(0xF1F5FD, 1.000), rgb(0x18181B, 1.000))
    /// `surface-3`: light #E2E8F0, dark #232327
    static let surface3 = dynamic(rgb(0xE2E8F0, 1.000), rgb(0x232327, 1.000))
    /// `border`: light #E4ECFC, dark #222226
    static let border = dynamic(rgb(0xE4ECFC, 1.000), rgb(0x222226, 1.000))
    /// `border-strong`: light #CBD5E1, dark #3F3F46
    static let borderStrong = dynamic(rgb(0xCBD5E1, 1.000), rgb(0x3F3F46, 1.000))
    /// `fg`: light #0F172A, dark #F4F4F5
    static let fg = dynamic(rgb(0x0F172A, 1.000), rgb(0xF4F4F5, 1.000))
    /// `fg-2`: light #334155, dark #D4D4D8
    static let fg2 = dynamic(rgb(0x334155, 1.000), rgb(0xD4D4D8, 1.000))
    /// `muted`: light #475569, dark #A1A1AA
    static let muted = dynamic(rgb(0x475569, 1.000), rgb(0xA1A1AA, 1.000))
    /// `primary`: light #2563EB, dark #60A5FA
    static let primary = dynamic(rgb(0x2563EB, 1.000), rgb(0x60A5FA, 1.000))
    /// `primary-fg`: light #FFFFFF, dark #0A0A0B
    static let primaryFg = dynamic(rgb(0xFFFFFF, 1.000), rgb(0x0A0A0B, 1.000))
    /// `accent`: light #2563EB, dark #60A5FA
    static let accent = dynamic(rgb(0x2563EB, 1.000), rgb(0x60A5FA, 1.000))
    /// `accent-fg`: light #FFFFFF, dark #0A0A0B
    static let accentFg = dynamic(rgb(0xFFFFFF, 1.000), rgb(0x0A0A0B, 1.000))
    /// `accent-soft`: light #EEF3FE, dark #0F1D33
    static let accentSoft = dynamic(rgb(0xEEF3FE, 1.000), rgb(0x0F1D33, 1.000))
    /// `ok`: light #15803D, dark #4ADE80
    static let ok = dynamic(rgb(0x15803D, 1.000), rgb(0x4ADE80, 1.000))
    /// `ok-soft`: light #EEF8F1, dark #0B2316
    static let okSoft = dynamic(rgb(0xEEF8F1, 1.000), rgb(0x0B2316, 1.000))
    /// `warn`: light #B45309, dark #FBBF24
    static let warn = dynamic(rgb(0xB45309, 1.000), rgb(0xFBBF24, 1.000))
    /// `warn-soft`: light #FDF3E3, dark #241B07
    static let warnSoft = dynamic(rgb(0xFDF3E3, 1.000), rgb(0x241B07, 1.000))
    /// `danger`: light #C81E1E, dark #F87171
    static let danger = dynamic(rgb(0xC81E1E, 1.000), rgb(0xF87171, 1.000))
    /// `danger-soft`: light #FDF0F0, dark #2A1112
    static let dangerSoft = dynamic(rgb(0xFDF0F0, 1.000), rgb(0x2A1112, 1.000))
    /// `info`: light #0E7490, dark #22D3EE
    static let info = dynamic(rgb(0x0E7490, 1.000), rgb(0x22D3EE, 1.000))
    /// `info-soft`: light #E8F7FA, dark #0A2329
    static let infoSoft = dynamic(rgb(0xE8F7FA, 1.000), rgb(0x0A2329, 1.000))
    /// `ring`: light #2563EB, dark #60A5FA
    static let ring = dynamic(rgb(0x2563EB, 1.000), rgb(0x60A5FA, 1.000))
    /// `overlay`: light #0F172A80, dark #000000B3
    static let overlay = dynamic(rgb(0x0F172A, 0.502), rgb(0x000000, 0.702))
    /// `series-1`: light #2a78d6, dark #3987e5
    static let series1 = dynamic(rgb(0x2A78D6, 1.000), rgb(0x3987E5, 1.000))
    /// `series-2`: light #eb6834, dark #d95926
    static let series2 = dynamic(rgb(0xEB6834, 1.000), rgb(0xD95926, 1.000))
    /// `series-3`: light #1baf7a, dark #199e70
    static let series3 = dynamic(rgb(0x1BAF7A, 1.000), rgb(0x199E70, 1.000))
    /// `series-4`: light #eda100, dark #c98500
    static let series4 = dynamic(rgb(0xEDA100, 1.000), rgb(0xC98500, 1.000))
    /// `series-5`: light #e87ba4, dark #d55181
    static let series5 = dynamic(rgb(0xE87BA4, 1.000), rgb(0xD55181, 1.000))
    /// `series-6`: light #008300, dark #008300
    static let series6 = dynamic(rgb(0x008300, 1.000), rgb(0x008300, 1.000))
    /// `series-7`: light #6250d6, dark #9085e9
    static let series7 = dynamic(rgb(0x6250D6, 1.000), rgb(0x9085E9, 1.000))
    /// `series-8`: light #e34948, dark #e66767
    static let series8 = dynamic(rgb(0xE34948, 1.000), rgb(0xE66767, 1.000))
    /// `border-input`: light #7C8BA6, dark #6E6E78
    static let borderInput = dynamic(rgb(0x7C8BA6, 1.000), rgb(0x6E6E78, 1.000))
    /// `glass`: light #FFFFFFB8, dark #111113B8
    static let glass = dynamic(rgb(0xFFFFFF, 0.722), rgb(0x111113, 0.722))
    /// `glass-border`: light #E4ECFC, dark #FFFFFF14
    static let glassBorder = dynamic(rgb(0xE4ECFC, 1.000), rgb(0xFFFFFF, 0.078))

    static func rgb(_ hex: UInt32, _ alpha: CGFloat = 1) -> UIColor {
        UIColor(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: alpha
        )
    }

    static func dynamic(_ light: UIColor, _ dark: UIColor) -> UIColor {
        UIColor { traits in traits.userInterfaceStyle == .dark ? dark : light }
    }
}

/// Semantic colours for SwiftUI. Light = Minimalist SaaS, dark = OLED (MASTER.md §2).
enum NXColor {
    static let bg = Color(uiColor: NXUIColor.bg)
    static let surface = Color(uiColor: NXUIColor.surface)
    static let surface2 = Color(uiColor: NXUIColor.surface2)
    static let surface3 = Color(uiColor: NXUIColor.surface3)
    static let border = Color(uiColor: NXUIColor.border)
    static let borderStrong = Color(uiColor: NXUIColor.borderStrong)
    static let fg = Color(uiColor: NXUIColor.fg)
    static let fg2 = Color(uiColor: NXUIColor.fg2)
    static let muted = Color(uiColor: NXUIColor.muted)
    static let primary = Color(uiColor: NXUIColor.primary)
    static let primaryFg = Color(uiColor: NXUIColor.primaryFg)
    static let accent = Color(uiColor: NXUIColor.accent)
    static let accentFg = Color(uiColor: NXUIColor.accentFg)
    static let accentSoft = Color(uiColor: NXUIColor.accentSoft)
    static let ok = Color(uiColor: NXUIColor.ok)
    static let okSoft = Color(uiColor: NXUIColor.okSoft)
    static let warn = Color(uiColor: NXUIColor.warn)
    static let warnSoft = Color(uiColor: NXUIColor.warnSoft)
    static let danger = Color(uiColor: NXUIColor.danger)
    static let dangerSoft = Color(uiColor: NXUIColor.dangerSoft)
    static let info = Color(uiColor: NXUIColor.info)
    static let infoSoft = Color(uiColor: NXUIColor.infoSoft)
    static let ring = Color(uiColor: NXUIColor.ring)
    static let overlay = Color(uiColor: NXUIColor.overlay)
    static let series1 = Color(uiColor: NXUIColor.series1)
    static let series2 = Color(uiColor: NXUIColor.series2)
    static let series3 = Color(uiColor: NXUIColor.series3)
    static let series4 = Color(uiColor: NXUIColor.series4)
    static let series5 = Color(uiColor: NXUIColor.series5)
    static let series6 = Color(uiColor: NXUIColor.series6)
    static let series7 = Color(uiColor: NXUIColor.series7)
    static let series8 = Color(uiColor: NXUIColor.series8)
    static let borderInput = Color(uiColor: NXUIColor.borderInput)
    static let glass = Color(uiColor: NXUIColor.glass)
    static let glassBorder = Color(uiColor: NXUIColor.glassBorder)

    /// Categorical colours for charts and agent tiles (`series-1…8`).
    static let series: [Color] = [series1, series2, series3, series4, series5, series6, series7, series8]
}

// MARK: - Typography

/// A bundled font family (MASTER.md §3): Montserrat for headings and numbers, Inter for body
/// and UI, JetBrains Mono for code, ids and tool output.
enum NXFontFamily: Sendable {
    case sans, heading, mono
}

enum NXFontWeight: Sendable {
    case regular, medium, semibold, bold
}

enum NXFontFace {
    /// PostScript names of the bundled faces (files: ios/DesignSystem/Fonts/<name>.ttf).
    static let all: [String] = ["Inter-Regular", "Inter-Medium", "Inter-SemiBold", "Inter-Bold", "Montserrat-SemiBold", "Montserrat-Bold", "JetBrainsMono-Regular", "JetBrainsMono-Medium", "JetBrainsMono-SemiBold"]

    /// The bundled face closest to `weight` in `family`.
    static func name(_ family: NXFontFamily, _ weight: NXFontWeight) -> String {
        switch family {
        case .sans:
            switch weight {
            case .regular: return "Inter-Regular"
            case .medium: return "Inter-Medium"
            case .semibold: return "Inter-SemiBold"
            case .bold: return "Inter-Bold"
            }
        case .heading:
            switch weight {
            case .regular: return "Montserrat-SemiBold"
            case .medium: return "Montserrat-SemiBold"
            case .semibold: return "Montserrat-SemiBold"
            case .bold: return "Montserrat-Bold"
            }
        case .mono:
            switch weight {
            case .regular: return "JetBrainsMono-Regular"
            case .medium: return "JetBrainsMono-Medium"
            case .semibold: return "JetBrainsMono-SemiBold"
            case .bold: return "JetBrainsMono-SemiBold"
            }
        }
    }
}

/// A type role: font, Dynamic Type text style and line spacing (line height − size).
struct NXTextStyle {
    let family: NXFontFamily
    let weight: NXFontWeight
    let size: CGFloat
    let relativeTo: Font.TextStyle
    let lineSpacing: CGFloat
    /// Letter spacing in points at the base size.
    let tracking: CGFloat

    /// The font, scaled with Dynamic Type relative to `relativeTo`.
    var font: Font {
        Font.custom(NXFontFace.name(family, weight), size: size, relativeTo: relativeTo)
    }

    /// The same role with another weight (e.g. a semibold body label).
    func weight(_ weight: NXFontWeight) -> NXTextStyle {
        NXTextStyle(family: family, weight: weight, size: size, relativeTo: relativeTo, lineSpacing: lineSpacing, tracking: tracking)
    }
}

/// The type scale (MASTER.md §3).
enum NXType {
    /// `caption`: 12/16 500, Inter, scales with `caption1`.
    static let caption = NXTextStyle(family: .sans, weight: .medium, size: 12, relativeTo: .caption, lineSpacing: 4, tracking: 0)
    /// `footnote`: 13/18 400, Inter, scales with `footnote`.
    static let footnote = NXTextStyle(family: .sans, weight: .regular, size: 13, relativeTo: .footnote, lineSpacing: 5, tracking: 0)
    /// `body`: 14/21 400, Inter, scales with `subheadline`.
    static let body = NXTextStyle(family: .sans, weight: .regular, size: 14, relativeTo: .subheadline, lineSpacing: 7, tracking: 0)
    /// `body-lg`: 16/24 400, Inter, scales with `body`.
    static let bodyLg = NXTextStyle(family: .sans, weight: .regular, size: 16, relativeTo: .body, lineSpacing: 8, tracking: 0)
    /// `title-3`: 16/22 600, Montserrat, scales with `headline`.
    static let title3 = NXTextStyle(family: .heading, weight: .semibold, size: 16, relativeTo: .headline, lineSpacing: 6, tracking: 0)
    /// `title-2`: 20/26 600, Montserrat, scales with `title3`.
    static let title2 = NXTextStyle(family: .heading, weight: .semibold, size: 20, relativeTo: .title3, lineSpacing: 6, tracking: 0)
    /// `title-1`: 24/30 600, Montserrat, scales with `title2`.
    static let title1 = NXTextStyle(family: .heading, weight: .semibold, size: 24, relativeTo: .title2, lineSpacing: 6, tracking: -0.24)
    /// `mono`: 12.5/19 400, JetBrains Mono, scales with `footnote.monospaced`.
    static let mono = NXTextStyle(family: .mono, weight: .regular, size: 12.5, relativeTo: .footnote, lineSpacing: 6.5, tracking: 0)
}

// MARK: - Spacing, radius, sizes

/// 4 pt spacing scale (`space.*`).
enum NXSpace {
    static let s0: CGFloat = 0
    static let s1: CGFloat = 4
    static let s2: CGFloat = 8
    static let s3: CGFloat = 12
    static let s4: CGFloat = 16
    static let s5: CGFloat = 20
    static let s6: CGFloat = 24
    static let s8: CGFloat = 32
    static let s10: CGFloat = 40
    static let s12: CGFloat = 48
}

enum NXRadius {
    static let sm: CGFloat = 6
    static let md: CGFloat = 8
    static let lg: CGFloat = 12
    static let xl: CGFloat = 16
    static let full: CGFloat = 9999
}

enum NXSize {
    static let touchMin: CGFloat = 44
    static let controlSm: CGFloat = 32
    static let controlMd: CGFloat = 36
    static let controlLg: CGFloat = 44
    static let sidebar: CGFloat = 232
    static let tabbar: CGFloat = 56
    static let contentMax: CGFloat = 1200
}

enum NXZIndex {
    static let sticky: Double = 10
    static let nav: Double = 20
    static let sheet: Double = 30
    static let dialog: Double = 50
    static let toast: Double = 60
}

// MARK: - Motion

/// Durations in seconds and easing curves (MASTER.md §7). Callers honour Reduce Motion.
enum NXMotion {
    static let instant: Double = 0.100
    static let fast: Double = 0.150
    static let base: Double = 0.200
    static let slow: Double = 0.250

    /// `out`: cubic-bezier(0.22, 1, 0.36, 1)
    static func out(_ duration: Double) -> Animation {
        .timingCurve(0.22, 1, 0.36, 1, duration: duration)
    }

    /// `standard`: cubic-bezier(0.4, 0, 0.2, 1)
    static func standard(_ duration: Double) -> Animation {
        .timingCurve(0.4, 0, 0.2, 1, duration: duration)
    }
}

// MARK: - Elevation

struct NXShadowStyle {
    struct Offset {
        let x: CGFloat
        let y: CGFloat
        let radius: CGFloat
    }

    let color: Color
    let light: Offset
    let dark: Offset
}

/// Two levels only (MASTER.md §5): a card hairline shadow and an overlay shadow.
enum NXShadow {
    static let card = NXShadowStyle(
        color: Color(uiColor: NXUIColor.dynamic(NXUIColor.rgb(0x0F172A, 0.040), NXUIColor.rgb(0x000000, 0.000))),
        light: .init(x: 0, y: 1, radius: 1),
        dark: .init(x: 0, y: 0, radius: 0)
    )
    static let overlay = NXShadowStyle(
        color: Color(uiColor: NXUIColor.dynamic(NXUIColor.rgb(0x0F172A, 0.250), NXUIColor.rgb(0x000000, 0.700))),
        light: .init(x: 0, y: 16, radius: 24),
        dark: .init(x: 0, y: 24, radius: 30)
    )
}
