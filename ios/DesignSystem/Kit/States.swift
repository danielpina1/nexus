import SwiftUI
import UIKit

// Empty, error and loading states (MASTER.md §9): every list has all three.

/// Icon tile, one-line title, one sentence and an optional action.
struct NXEmptyState: View {
    let systemImage: String
    let title: String
    var message: String?
    var actionTitle: String?
    var action: (() -> Void)?
    var compact = false

    init(systemImage: String, title: String, message: String? = nil, compact: Bool = false, actionTitle: String? = nil, action: (() -> Void)? = nil) {
        self.systemImage = systemImage
        self.title = title
        self.message = message
        self.compact = compact
        self.actionTitle = actionTitle
        self.action = action
    }

    var body: some View {
        VStack(spacing: NXSpace.s2) {
            Image(systemName: systemImage)
                .font(.system(size: 17, weight: .medium))
                .foregroundStyle(NXColor.muted)
                .frame(width: 36, height: 36)
                .background(NXColor.surface2, in: Circle())
                .padding(.bottom, NXSpace.s1)
                .accessibilityHidden(true)
            Text(title)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .multilineTextAlignment(.center)
            if let message, !message.isEmpty {
                Text(message)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 320)
            }
            if let actionTitle, let action {
                NXButton(actionTitle, kind: .secondary, size: .small, action: action)
                    .padding(.top, NXSpace.s2)
            }
        }
        .padding(.horizontal, NXSpace.s6)
        .padding(.vertical, compact ? NXSpace.s8 : NXSpace.s12)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
    }
}

/// "Couldn't reach the Hub" with the error and Retry. Announced to VoiceOver when it appears.
struct NXErrorState: View {
    var title = "Couldn't reach the Hub"
    let message: String
    var compact = false
    var retry: (() -> Void)?

    init(title: String = "Couldn't reach the Hub", message: String, compact: Bool = false, retry: (() -> Void)? = nil) {
        self.title = title
        self.message = message
        self.compact = compact
        self.retry = retry
    }

    var body: some View {
        VStack(spacing: NXSpace.s2) {
            Image(systemName: "icloud.slash")
                .font(.system(size: 17, weight: .medium))
                .foregroundStyle(NXColor.danger)
                .frame(width: 36, height: 36)
                .background(NXColor.dangerSoft, in: Circle())
                .padding(.bottom, NXSpace.s1)
                .accessibilityHidden(true)
            Text(title)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .multilineTextAlignment(.center)
            Text(message)
                .nx(NXType.mono)
                .foregroundStyle(NXColor.muted)
                .multilineTextAlignment(.center)
                .textSelection(.enabled)
            if let retry {
                NXButton("Retry", systemImage: "arrow.clockwise", kind: .secondary, size: .small, action: retry)
                    .padding(.top, NXSpace.s2)
            }
        }
        .padding(.horizontal, NXSpace.s6)
        .padding(.vertical, compact ? NXSpace.s8 : NXSpace.s12)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .contain)
        .onAppear {
            UIAccessibility.post(notification: .announcement, argument: "\(title). \(message)")
        }
    }
}

/// Inline error message (danger text) for a failed action, with optional Retry and dismiss.
struct NXInlineError: View {
    let message: String
    var retry: (() -> Void)?
    var dismiss: (() -> Void)?

    init(_ message: String, retry: (() -> Void)? = nil, dismiss: (() -> Void)? = nil) {
        self.message = message
        self.retry = retry
        self.dismiss = dismiss
    }

    var body: some View {
        HStack(alignment: .top, spacing: NXSpace.s2) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 13))
                .foregroundStyle(NXColor.danger)
                .padding(.top, 2)
                .accessibilityHidden(true)
            Text(message)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.danger)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let retry {
                Button("Retry", action: retry)
                    .font(NXType.footnote.weight(.semibold).font)
                    .foregroundStyle(NXColor.accent)
                    .frame(minWidth: NXSize.touchMin, minHeight: NXSize.touchMin)
                    .nxFocusRing()
            }
            if let dismiss {
                NXIconButton(systemImage: "xmark", label: "Dismiss", tint: NXColor.muted, action: dismiss)
            }
        }
        .padding(.horizontal, NXSpace.s3)
        .padding(.vertical, NXSpace.s2)
        .background(NXColor.dangerSoft, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
        .accessibilityElement(children: .contain)
        .onAppear {
            UIAccessibility.post(notification: .announcement, argument: message)
        }
    }
}

/// Spinner with a label ("Loading approval…").
struct NXLoadingState: View {
    var label = "Loading…"

    var body: some View {
        VStack(spacing: NXSpace.s3) {
            ProgressView()
            Text(label)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
        }
        .padding(NXSpace.s8)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
    }
}

/// Placeholder block of the size of the content it stands in for. Breathes unless Reduce
/// Motion is on; hidden from VoiceOver (the container says "Loading").
struct NXSkeleton: View {
    var height: CGFloat = 16
    var width: CGFloat?
    var cornerRadius: CGFloat = NXRadius.sm

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dimmed = false

    var body: some View {
        RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
            .fill(NXColor.surface3)
            .frame(width: width, height: height)
            .frame(maxWidth: width == nil ? .infinity : nil, alignment: .leading)
            .opacity(dimmed ? 0.5 : 1)
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) {
                    dimmed = true
                }
            }
            .accessibilityHidden(true)
    }
}

/// Skeleton list rows inside a card.
struct NXSkeletonRows: View {
    var rows = 3

    var body: some View {
        VStack(spacing: 0) {
            ForEach(0..<rows, id: \.self) { index in
                HStack(spacing: NXSpace.s3) {
                    NXSkeleton(height: 32, width: 32, cornerRadius: NXRadius.md)
                    VStack(alignment: .leading, spacing: 6) {
                        NXSkeleton(height: 12, width: 180)
                        NXSkeleton(height: 10, width: 120)
                    }
                    Spacer()
                }
                .padding(.horizontal, NXSpace.s4)
                .frame(minHeight: 60)
                if index < rows - 1 { NXDivider() }
            }
        }
        .nxCard(padding: 0)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Loading")
    }
}
