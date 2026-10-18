import SwiftUI
import UIKit

// Toast (MASTER.md §9): glass, above the tab bar, tone icon, optional action ("Retry").
// Announced to VoiceOver (status). Error toasts stay 8 s.

struct NXToastModel: Identifiable, Equatable {
    enum Kind {
        case success, info, error
    }

    let id = UUID()
    var kind: Kind
    var title: String
    var message: String?
    var actionTitle: String?

    static func == (lhs: NXToastModel, rhs: NXToastModel) -> Bool { lhs.id == rhs.id }

    var tone: NXTone {
        switch kind {
        case .success: return .ok
        case .info: return .info
        case .error: return .danger
        }
    }

    var systemImage: String {
        switch kind {
        case .success: return "checkmark.circle.fill"
        case .info: return "info.circle.fill"
        case .error: return "exclamationmark.triangle.fill"
        }
    }

    /// Seconds on screen.
    var duration: Double { kind == .error ? 8 : 4 }
}

struct NXToastView: View {
    let toast: NXToastModel
    var onAction: (() -> Void)?
    var onDismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: NXSpace.s3) {
            Image(systemName: toast.systemImage)
                .font(.system(size: 16))
                .foregroundStyle(toast.tone.color)
                .padding(.top, 1)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(toast.title)
                    .nx(NXType.body.weight(.semibold))
                    .foregroundStyle(NXColor.fg)
                if let message = toast.message, !message.isEmpty {
                    Text(message)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.fg2)
                        .lineLimit(3)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if let actionTitle = toast.actionTitle, let onAction {
                Button(actionTitle, action: onAction)
                    .font(NXType.body.weight(.semibold).font)
                    .foregroundStyle(NXColor.accent)
                    .frame(minWidth: NXSize.touchMin, minHeight: NXSize.touchMin)
                    .nxFocusRing()
            }
            NXIconButton(systemImage: "xmark", label: "Dismiss", tint: NXColor.muted, action: onDismiss)
                .padding(.vertical, -10)
        }
        .padding(.leading, NXSpace.s4)
        .padding(.trailing, NXSpace.s1)
        .padding(.vertical, NXSpace.s3)
        .nxGlass(cornerRadius: NXRadius.xl)
        .shadow(color: NXShadow.overlay.color, radius: 16, x: 0, y: 8)
        .accessibilityElement(children: .contain)
        .onAppear {
            let text = [toast.title, toast.message].compactMap { $0 }.joined(separator: ". ")
            UIAccessibility.post(notification: .announcement, argument: text)
        }
    }
}
