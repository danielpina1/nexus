import SwiftUI

// Stat tiles and meters (MASTER.md §9; web Metric, Meter and LimitMeter). Numbers use
// Montserrat with tabular figures; meters print their numbers so state never relies on colour.

/// Tone of a 0–1 fraction: accent, warn from 75 %, danger from 90 %.
func nxMeterTone(_ fraction: Double) -> NXTone {
    fraction >= 0.9 ? .danger : fraction >= 0.75 ? .warn : .accent
}

/// Thin horizontal bar for a 0–1 value.
struct NXMeter: View {
    let value: Double
    let label: String
    var tone: NXTone?
    var height: CGFloat = 6

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(value: Double, label: String, tone: NXTone? = nil, height: CGFloat = 6) {
        self.value = value
        self.label = label
        self.tone = tone
        self.height = height
    }

    private var clamped: Double { min(max(value, 0), 1) }

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                Capsule().fill(NXColor.surface3)
                Capsule()
                    .fill((tone ?? nxMeterTone(clamped)).color)
                    .frame(width: max(clamped > 0 ? height : 0, proxy.size.width * clamped))
            }
        }
        .frame(height: height)
        .animation(reduceMotion ? nil : NXMotion.out(0.7), value: clamped)
        .accessibilityElement()
        .accessibilityLabel(label)
        .accessibilityValue("\(Int((clamped * 100).rounded())) percent")
    }
}

/// Metric tile: label (with an optional tone dot), big number, one line below.
struct NXStatTile: View {
    let label: String
    let value: String
    var detail: String?
    var dot: NXTone?
    var systemImage: String?

    init(_ label: String, value: String, detail: String? = nil, dot: NXTone? = nil, systemImage: String? = nil) {
        self.label = label
        self.value = value
        self.detail = detail
        self.dot = dot
        self.systemImage = systemImage
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s1) {
            HStack(spacing: 6) {
                if let dot {
                    NXDot(color: dot.color, size: 6)
                }
                if let systemImage {
                    Image(systemName: systemImage)
                        .font(.system(size: 11, weight: .semibold))
                        .accessibilityHidden(true)
                }
                Text(label)
                    .nx(NXType.caption)
                    .lineLimit(1)
            }
            .foregroundStyle(NXColor.muted)
            Text(value)
                .nx(NXType.title2)
                .monospacedDigit()
                .foregroundStyle(NXColor.fg)
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            if let detail, !detail.isEmpty {
                Text(detail)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .nxCard(padding: NXSpace.s4)
        .accessibilityElement(children: .combine)
    }
}

/// A value against a limit (team runs: tasks, runs, cost, time). Warn at 75 %, danger at 90 %.
struct NXLimitMeter: View {
    let label: String
    var systemImage: String?
    let value: Double
    /// nil or 0 = no limit.
    var limit: Double?
    let display: String
    var limitDisplay: String?
    var hint: String?

    init(_ label: String, systemImage: String? = nil, value: Double, limit: Double?, display: String, limitDisplay: String? = nil, hint: String? = nil) {
        self.label = label
        self.systemImage = systemImage
        self.value = value
        self.limit = limit
        self.display = display
        self.limitDisplay = limitDisplay
        self.hint = hint
    }

    private var fraction: Double? {
        guard let limit, limit > 0 else { return nil }
        return min(1, value / limit)
    }

    var body: some View {
        let tone = fraction.map(nxMeterTone)
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                if let systemImage {
                    Image(systemName: systemImage)
                        .font(.system(size: 11, weight: .semibold))
                        .accessibilityHidden(true)
                }
                Text(label).nx(NXType.caption)
            }
            .foregroundStyle(NXColor.muted)
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(display)
                    .nx(NXType.title2)
                    .monospacedDigit()
                    .foregroundStyle(tone == .danger ? NXColor.danger : tone == .warn ? NXColor.warn : NXColor.fg)
                    .lineLimit(1)
                    .minimumScaleFactor(0.7)
                if let limitDisplay {
                    Text("/ \(limitDisplay)")
                        .nx(NXType.caption.weight(.regular))
                        .monospacedDigit()
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(1)
                }
            }
            NXMeter(value: fraction ?? 0, label: label, tone: tone)
                .accessibilityHidden(true)
            if let hint, !hint.isEmpty {
                Text(hint)
                    .nx(NXType.caption.weight(.regular))
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .nxCard(padding: NXSpace.s3 + 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(accessibilityValue)
    }

    private var accessibilityValue: String {
        var parts = [display]
        if let limitDisplay { parts.append("of \(limitDisplay)") }
        if let fraction { parts.append("\(Int((fraction * 100).rounded())) percent of the limit") }
        if let hint { parts.append(hint) }
        return parts.joined(separator: ", ")
    }
}

/// Labelled meter row (machine CPU / memory / disk): label, bar, percentage.
struct NXMetricRow: View {
    let label: String
    let percent: Double

    var body: some View {
        HStack(spacing: NXSpace.s2) {
            Text(label)
                .nx(NXType.caption.weight(.regular))
                .foregroundStyle(NXColor.muted)
                .frame(width: 44, alignment: .leading)
            NXMeter(value: percent / 100, label: label)
                .accessibilityHidden(true)
            Text("\(Int(percent.rounded()))%")
                .nx(NXType.caption)
                .monospacedDigit()
                .foregroundStyle(NXColor.fg2)
                .frame(width: 40, alignment: .trailing)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue("\(Int(percent.rounded())) percent")
    }
}
