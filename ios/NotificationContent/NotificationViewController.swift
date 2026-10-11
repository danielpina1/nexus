import NexusCore
import Observation
import SwiftUI
import UIKit
import UserNotifications
import UserNotificationsUI

/// Long-press UI for approvals / results: a list of items (cart lines, diff summary) taken from
/// the decrypted payload (`items_json`, written by the Notification Service extension).
final class NotificationViewController: UIViewController, UNNotificationContentExtension {
    private let model = NotificationContentModel()
    private var host: UIHostingController<NotificationContentView>?

    override func viewDidLoad() {
        super.viewDidLoad()
        let host = UIHostingController(rootView: NotificationContentView(model: model))
        host.view.translatesAutoresizingMaskIntoConstraints = false
        host.view.backgroundColor = .clear
        addChild(host)
        view.addSubview(host.view)
        NSLayoutConstraint.activate([
            host.view.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            host.view.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            host.view.topAnchor.constraint(equalTo: view.topAnchor),
            host.view.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])
        host.didMove(toParent: self)
        self.host = host
    }

    func didReceive(_ notification: UNNotification) {
        let content = notification.request.content
        model.title = content.title
        model.body = content.body
        model.isApproval = content.categoryIdentifier.hasPrefix("APPROVAL")

        let userInfo = content.userInfo
        if let json = userInfo["items_json"] as? String,
           let data = json.data(using: .utf8),
           let items = try? JSONDecoder().decode([NotificationPlaintext.Item].self, from: data) {
            model.items = items
        } else {
            model.items = []
        }
        if let attachment = content.attachments.first,
           attachment.url.startAccessingSecurityScopedResource() {
            defer { attachment.url.stopAccessingSecurityScopedResource() }
            if let data = try? Data(contentsOf: attachment.url) {
                model.image = UIImage(data: data)
            }
        }
        preferredContentSize = CGSize(width: view.bounds.width, height: model.preferredHeight)
    }
}

@Observable
final class NotificationContentModel {
    var title = ""
    var body = ""
    var isApproval = false
    var items: [NotificationPlaintext.Item] = []
    var image: UIImage?

    var preferredHeight: CGFloat {
        var height: CGFloat = 80
        height += CGFloat(min(items.count, 8)) * 32
        if image != nil { height += 200 }
        return height
    }
}

struct NotificationContentView: View {
    let model: NotificationContentModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let image = model.image {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFit()
                    .frame(maxHeight: 200)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            }
            if !model.items.isEmpty {
                VStack(spacing: 0) {
                    ForEach(Array(model.items.prefix(8).enumerated()), id: \.offset) { _, item in
                        HStack(alignment: .firstTextBaseline) {
                            VStack(alignment: .leading, spacing: 1) {
                                Text(item.name).font(.subheadline).lineLimit(1)
                                if let detail = item.detail {
                                    Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                }
                            }
                            Spacer()
                            if let amount = item.amount {
                                Text(amount).font(.subheadline.monospacedDigit())
                            }
                        }
                        .padding(.vertical, 4)
                        Divider()
                    }
                    if model.items.count > 8 {
                        Text("+ \(model.items.count - 8) more")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 4)
                    }
                }
            } else {
                Text(model.body).font(.body)
            }
            if model.isApproval {
                Label("Check every line before approving — the signature covers exactly this action.", systemImage: "checkmark.shield")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
