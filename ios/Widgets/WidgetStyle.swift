import NexusCore
import SwiftUI
import WidgetKit

// Design-system helpers for widgets, Live Activities and the Dynamic Island (MASTER.md: same
// cards, states and words on every surface). Interactive buttons use `.plain` with a drawn pill:
// widgets render without live button-style state, so the pill carries the kit look.

enum WidgetPillKind {
    case primary, success, secondary, destructive
}

/// Button / link label in kit colours. `minHeight` 44 on the Lock Screen, smaller in the island.
struct WidgetPill: View {
    let title: String
    var systemImage: String?
    var kind: WidgetPillKind = .secondary
    var minHeight: CGFloat = NXSize.touchMin

    var body: some View {
        let fill: Color
        let foreground: Color
        switch kind {
        case .primary:
            fill = NXColor.primary
            foreground = NXColor.primaryFg
        case .success:
            fill = NXColor.ok
            foreground = NXColor.primaryFg
        case .destructive:
            fill = NXColor.dangerSoft
            foreground = NXColor.danger
        case .secondary:
            fill = NXColor.surface2
            foreground = NXColor.fg
        }
        return HStack(spacing: 4) {
            if let systemImage {
                Image(systemName: systemImage)
                    .font(.system(size: 12, weight: .semibold))
                    .accessibilityHidden(true)
            }
            Text(title)
                .nx(NXType.footnote.weight(.semibold))
                .lineLimit(1)
                .minimumScaleFactor(0.8)
        }
        .foregroundStyle(foreground)
        .padding(.horizontal, NXSpace.s2)
        .frame(maxWidth: .infinity, minHeight: minHeight)
        .background(fill, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
    }
}

extension RunActivityAttributes.ContentState.Phase {
    var tone: NXTone {
        switch self {
        case .running: return .info
        case .waitingApproval, .waitingInput: return .warn
        case .done: return .ok
        case .failed: return .danger
        }
    }

    /// Same words as the task state badge.
    var label: String {
        switch self {
        case .running: return "Running"
        case .waitingApproval: return "Waiting approval"
        case .waitingInput: return "Waiting input"
        case .done: return "Succeeded"
        case .failed: return "Failed"
        }
    }

    var symbol: String {
        switch self {
        case .running: return "arrow.triangle.2.circlepath"
        case .waitingApproval: return "hand.raised"
        case .waitingInput: return "questionmark.bubble"
        case .done: return "checkmark.circle"
        case .failed: return "xmark.circle"
        }
    }
}

/// Header line used by every widget: tile, title, trailing text.
struct WidgetHeader: View {
    let title: String
    let systemImage: String
    var trailing: String?

    var body: some View {
        HStack(spacing: NXSpace.s2) {
            NXIconTile(systemImage: systemImage, tone: .accent, size: 22)
            Text(title)
                .nx(NXType.title3)
                .foregroundStyle(NXColor.fg)
                .lineLimit(1)
            Spacer(minLength: 0)
            if let trailing {
                Text(trailing)
                    .nx(NXType.caption.weight(.regular))
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
                    .lineLimit(1)
            }
        }
    }
}
