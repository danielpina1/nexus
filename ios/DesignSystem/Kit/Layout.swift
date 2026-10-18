import SwiftUI

// Layout pieces (MASTER.md §4, §9): page header, section header, list rows, key–value rows and
// section tabs.

/// Page title block like the web PageHeader: Montserrat title, optional subtitle.
struct NXPageHeader: View {
    let title: String
    var subtitle: String?

    init(_ title: String, subtitle: String? = nil) {
        self.title = title
        self.subtitle = subtitle
    }

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s1) {
            Text(title)
                .nx(NXType.title1)
                .foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
            if let subtitle, !subtitle.isEmpty {
                Text(subtitle)
                    .nx(NXType.footnote)
                    .foregroundStyle(NXColor.muted)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Section title with an optional count, live dot and trailing accessory (e.g. "Open inbox →").
struct NXSectionHeader<Accessory: View>: View {
    let title: String
    var count: Int?
    var live = false
    let accessory: Accessory

    init(_ title: String, count: Int? = nil, live: Bool = false, @ViewBuilder accessory: () -> Accessory) {
        self.title = title
        self.count = count
        self.live = live
        self.accessory = accessory()
    }

    var body: some View {
        HStack(spacing: NXSpace.s2) {
            if live {
                NXDot(color: NXColor.ok, size: 8, pulse: true)
            }
            Text(title)
                .nx(NXType.title3)
                .foregroundStyle(NXColor.fg)
                .accessibilityAddTraits(.isHeader)
            if let count, count > 0 {
                Text("\(count)")
                    .nx(NXType.footnote)
                    .monospacedDigit()
                    .foregroundStyle(NXColor.muted)
            }
            Spacer(minLength: NXSpace.s2)
            accessory
        }
        .frame(minHeight: 32)
        .accessibilityElement(children: .contain)
    }
}

extension NXSectionHeader where Accessory == EmptyView {
    init(_ title: String, count: Int? = nil, live: Bool = false) {
        self.init(title, count: count, live: live) { EmptyView() }
    }
}

/// "Open inbox →" style link label for section headers (44 pt tall).
struct NXLinkLabel: View {
    let title: String

    var body: some View {
        HStack(spacing: 4) {
            Text(title).nx(NXType.footnote)
            Image(systemName: "arrow.right").font(.system(size: 11, weight: .semibold)).accessibilityHidden(true)
        }
        .foregroundStyle(NXColor.muted)
        .frame(minHeight: NXSize.touchMin)
        .contentShape(Rectangle())
    }
}

/// List row (≥ 48 pt; 60 for tasks): leading tile, title, subtitle, trailing accessory.
struct NXRow<Leading: View, Trailing: View>: View {
    let title: String
    var subtitle: String?
    var minHeight: CGFloat
    var titleLineLimit: Int
    let leading: Leading
    let trailing: Trailing

    init(
        _ title: String,
        subtitle: String? = nil,
        minHeight: CGFloat = NXSpace.s12,
        titleLineLimit: Int = 1,
        @ViewBuilder leading: () -> Leading,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.title = title
        self.subtitle = subtitle
        self.minHeight = minHeight
        self.titleLineLimit = titleLineLimit
        self.leading = leading()
        self.trailing = trailing()
    }

    var body: some View {
        HStack(spacing: NXSpace.s3) {
            leading
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .nx(NXType.body.weight(.medium))
                    .foregroundStyle(NXColor.fg)
                    .lineLimit(titleLineLimit)
                if let subtitle, !subtitle.isEmpty {
                    Text(subtitle)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.muted)
                        .lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            trailing
        }
        .padding(.horizontal, NXSpace.s4)
        .padding(.vertical, NXSpace.s2 + 2)
        .frame(minHeight: minHeight)
        .contentShape(Rectangle())
    }
}

extension NXRow where Trailing == EmptyView {
    init(_ title: String, subtitle: String? = nil, minHeight: CGFloat = NXSpace.s12, titleLineLimit: Int = 1, @ViewBuilder leading: () -> Leading) {
        self.init(title, subtitle: subtitle, minHeight: minHeight, titleLineLimit: titleLineLimit, leading: leading) { EmptyView() }
    }
}

extension NXRow where Leading == EmptyView, Trailing == EmptyView {
    init(_ title: String, subtitle: String? = nil, minHeight: CGFloat = NXSpace.s12, titleLineLimit: Int = 1) {
        self.init(title, subtitle: subtitle, minHeight: minHeight, titleLineLimit: titleLineLimit, leading: { EmptyView() }, trailing: { EmptyView() })
    }
}

/// Chevron for rows that navigate.
struct NXChevron: View {
    var body: some View {
        Image(systemName: "chevron.right")
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(NXColor.muted)
            .accessibilityHidden(true)
    }
}

/// "Label ……… value" row (definition lists in cards).
struct NXKeyValueRow: View {
    let label: String
    let value: String
    var mono = false

    init(_ label: String, value: String, mono: Bool = false) {
        self.label = label
        self.value = value
        self.mono = mono
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: NXSpace.s3) {
            Text(label)
                .nx(NXType.footnote)
                .foregroundStyle(NXColor.muted)
            Spacer(minLength: NXSpace.s2)
            Text(value)
                .nx(mono ? NXType.mono : NXType.footnote)
                .foregroundStyle(NXColor.fg)
                .multilineTextAlignment(.trailing)
                .textSelection(.enabled)
        }
        .frame(minHeight: 28)
        .accessibilityElement(children: .combine)
    }
}

/// Small uppercase label above a block ("RESULT", "REQUEST").
struct NXOverline: View {
    let text: String

    init(_ text: String) {
        self.text = text
    }

    var body: some View {
        Text(text.uppercased())
            .nx(NXType.caption.weight(.semibold))
            .tracking(0.6)
            .foregroundStyle(NXColor.muted)
            .accessibilityAddTraits(.isHeader)
    }
}

/// Underline section tabs (web SectionTabs): 44 pt tall, 2 pt accent underline on the
/// selected tab, horizontally scrollable on narrow screens.
struct NXSectionTabs<Item: Hashable>: View {
    let items: [Item]
    @Binding var selection: Item
    let title: (Item) -> String
    var badge: (Item) -> Int? = { _ in nil }

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(_ items: [Item], selection: Binding<Item>, title: @escaping (Item) -> String, badge: @escaping (Item) -> Int? = { _ in nil }) {
        self.items = items
        self._selection = selection
        self.title = title
        self.badge = badge
    }

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: NXSpace.s5) {
                ForEach(items, id: \.self) { item in
                    let selected = item == selection
                    Button {
                        if reduceMotion {
                            selection = item
                        } else {
                            withAnimation(NXMotion.out(NXMotion.fast)) { selection = item }
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Text(title(item))
                                .nx(NXType.body.weight(selected ? .semibold : .medium))
                                .foregroundStyle(selected ? NXColor.fg : NXColor.muted)
                            if let count = badge(item), count > 0 {
                                NXCountBadge(count: count)
                            }
                        }
                        .frame(minHeight: NXSize.touchMin)
                        .overlay(alignment: .bottom) {
                            Rectangle()
                                .fill(selected ? NXColor.accent : Color.clear)
                                .frame(height: 2)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.sm))
                    .accessibilityAddTraits(selected ? [.isSelected, .isButton] : .isButton)
                }
            }
            .padding(.horizontal, NXSpace.s4)
        }
        .overlay(alignment: .bottom) { NXDivider() }
    }
}
