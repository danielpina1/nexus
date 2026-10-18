import NexusCore
import SwiftUI

// Badges and status pills (MASTER.md §9): `-soft` tint plus tone text, radius sm, icon plus label
// (state is never shown by colour alone).

struct NXBadge: View {
    let text: String
    var systemImage: String?
    var tone: NXTone = .neutral
    /// A small spinner instead of the icon (running states). Off in widgets.
    var isSpinning = false
    /// A leading dot, optionally pulsing (live).
    var dot = false
    var pulse = false

    init(_ text: String, systemImage: String? = nil, tone: NXTone = .neutral, isSpinning: Bool = false, dot: Bool = false, pulse: Bool = false) {
        self.text = text
        self.systemImage = systemImage
        self.tone = tone
        self.isSpinning = isSpinning
        self.dot = dot
        self.pulse = pulse
    }

    var body: some View {
        HStack(spacing: 4) {
            if dot {
                NXDot(color: tone.color, size: 6, pulse: pulse)
            }
            if isSpinning {
                ProgressView()
                    .controlSize(.mini)
                    .tint(tone.color)
                    .frame(width: 11, height: 11)
                    .accessibilityHidden(true)
            } else if let systemImage {
                Image(systemName: systemImage)
                    .font(.system(size: 10, weight: .semibold))
                    .accessibilityHidden(true)
            }
            Text(text)
                .nx(NXType.caption)
                .lineLimit(1)
        }
        .foregroundStyle(tone.color)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .frame(minHeight: 20)
        .background(tone.soft, in: RoundedRectangle(cornerRadius: NXRadius.sm, style: .continuous))
        .overlay {
            if tone == .neutral {
                RoundedRectangle(cornerRadius: NXRadius.sm, style: .continuous)
                    .strokeBorder(NXColor.border, lineWidth: 1)
            }
        }
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(text)
    }
}

// MARK: - Task state

extension Nexus_V1_TaskState {
    /// web/src/lib/format.ts taskStateTone.
    var tone: NXTone {
        switch self {
        case .running, .assigned, .routing, .escalating: return .info
        case .waitingApproval, .waitingInput, .needsInput: return .warn
        case .succeeded: return .ok
        case .failed: return .danger
        default: return .neutral
        }
    }

    /// SF Symbols per MASTER.md §11.
    var badgeSymbol: String {
        switch self {
        case .running, .routing, .assigned: return "arrow.triangle.2.circlepath"
        case .waitingApproval: return "hand.raised"
        case .waitingInput, .needsInput: return "questionmark.bubble"
        case .escalating: return "arrow.up.circle"
        case .succeeded: return "checkmark.circle"
        case .failed: return "xmark.circle"
        case .cancelled: return "stop.circle"
        case .queued: return "clock"
        default: return "circle.dashed"
        }
    }

    var isWorking: Bool {
        self == .running || self == .routing || self == .assigned
    }
}

/// Task state pill: spinner for running states (pass `animated: false` in widgets).
struct NXTaskStateBadge: View {
    let state: Nexus_V1_TaskState
    var animated = true

    var body: some View {
        NXBadge(
            state.label,
            systemImage: state.badgeSymbol,
            tone: state.tone,
            isSpinning: animated && state.isWorking
        )
        .accessibilityLabel("Status: \(state.label)")
    }
}

// MARK: - Approval class

/// "Face ID approval" (danger) or "Approval" (warn) plus the action class, as on the web card.
struct NXApprovalClassBadge: View {
    let isBiometric: Bool

    var body: some View {
        NXBadge(
            isBiometric ? "Face ID approval" : "Approval",
            systemImage: isBiometric ? "faceid" : "checkmark.shield",
            tone: isBiometric ? .danger : .warn
        )
    }
}

extension Nexus_V1_ApprovalState {
    var badgeLabel: String {
        switch self {
        case .approved: return "approved"
        case .denied: return "denied"
        case .expired: return "expired"
        default: return "pending"
        }
    }

    var tone: NXTone {
        switch self {
        case .approved: return .ok
        case .pending: return .warn
        default: return .neutral
        }
    }
}

/// Count badge on a section title or tab ("3 need you"): announced with its meaning.
struct NXCountBadge: View {
    let count: Int
    var tone: NXTone = .neutral
    var spokenNoun = "items"

    var body: some View {
        Text("\(count)")
            .nx(NXType.caption)
            .monospacedDigit()
            .foregroundStyle(tone == .neutral ? NXColor.muted : tone.color)
            .padding(.horizontal, 6)
            .frame(minWidth: 20, minHeight: 20)
            .background(tone == .neutral ? NXColor.surface2 : tone.soft, in: Capsule())
            .accessibilityLabel("\(count) \(spokenNoun)")
    }
}
