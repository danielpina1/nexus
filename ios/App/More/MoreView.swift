import NexusCore
import SwiftUI

/// More (web More sheet): Knowledge, Machines and Settings, the connection indicator and the
/// Light / System / Dark control.
struct MoreView: View {
    @Environment(NexusStore.self) private var store
    @Environment(AppRouter.self) private var router

    var body: some View {
        let online = store.nodes.filter { store.status(ofNode: $0.id)?.online == true }.count
        ScrollView {
            VStack(alignment: .leading, spacing: NXSpace.s6) {
                VStack(spacing: 0) {
                    row(.knowledge, title: "Knowledge", subtitle: "Search notes, capture, backlinks", symbol: "books.vertical")
                    NXDivider(inset: 60)
                    row(.machines, title: "Machines", subtitle: store.nodes.isEmpty ? "No machines yet" : "\(online) of \(store.nodes.count) online", symbol: "cpu")
                    NXDivider(inset: 60)
                    row(.settings, title: "Settings", subtitle: "Connection, appearance, devices, security", symbol: "gearshape")
                }
                .nxCard(padding: 0)

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Appearance")
                    AppearancePicker()
                }

                VStack(alignment: .leading, spacing: NXSpace.s3) {
                    NXSectionHeader("Connection")
                    HStack {
                        LiveConnectionPill()
                        Spacer()
                        Text(store.routeLabel)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.muted)
                    }
                    .nxCard()
                }

                NXButton("Quick capture", systemImage: "square.and.pencil", kind: .secondary, fullWidth: true) {
                    router.showCapture = true
                }
            }
            .padding(NXSpace.s4)
        }
        .nxScreenBackground()
        .navigationTitle("More")
        .toolbar { CommonToolbar() }
    }

    private func row(_ route: MoreRoute, title: String, subtitle: String, symbol: String) -> some View {
        NavigationLink(value: route) {
            NXRow(title, subtitle: subtitle, minHeight: 60, leading: {
                NXIconTile(systemImage: symbol, tone: .neutral, size: 36)
            }, trailing: {
                NXChevron()
            })
        }
        .buttonStyle(NXPlainPressStyle(cornerRadius: NXRadius.lg))
    }
}
