import Foundation
import NexusCore
import UserNotifications

/// Decrypts end-to-end encrypted notification content (SECURITY.md §4, IOS_LIVE.md §3.2).
///
/// The Hub sends `mutable-content: 1` pushes whose visible alert is generic
/// ("Nexus — new notification") plus `nexus_envelope` (HPKE-sealed `NotificationPlaintext`).
/// We open the envelope with the device encryption key, replace title/body, set
/// `threadIdentifier = task_id`, and download `attachment_url` (Hub blob URLs only).
/// Any failure falls back to the original (generic) content.
final class NotificationService: UNNotificationServiceExtension {
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var bestAttemptContent: UNMutableNotificationContent?
    private var work: Task<Void, Never>?

    override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        self.contentHandler = contentHandler
        guard let content = request.content.mutableCopy() as? UNMutableNotificationContent else {
            contentHandler(request.content)
            return
        }
        bestAttemptContent = content

        let userInfo = request.content.userInfo
        // Plaintext hints the Hub may send outside the envelope (ids only, no content).
        if let taskID = userInfo["task_id"] as? String, !taskID.isEmpty {
            content.threadIdentifier = taskID
        }

        guard userInfo[EnvelopeFormat.pushPayloadKey] != nil else {
            contentHandler(content)
            return
        }

        let plaintext: NotificationPlaintext
        do {
            plaintext = try EnvelopeCrypto.openNotification(userInfo: userInfo)
        } catch {
            // Could not decrypt (e.g. re-paired device, old key): keep the generic alert.
            contentHandler(content)
            return
        }

        apply(plaintext, to: content)

        guard let urlString = plaintext.attachmentURL,
              let url = URL(string: urlString),
              Self.isHubURL(url) else {
            contentHandler(content)
            return
        }

        work = Task { [weak self] in
            if let attachment = await Self.downloadAttachment(from: url) {
                content.attachments = [attachment]
            }
            self?.finish()
        }
    }

    override func serviceExtensionTimeWillExpire() {
        work?.cancel()
        finish()
    }

    private func finish() {
        guard let handler = contentHandler, let content = bestAttemptContent else { return }
        contentHandler = nil
        handler(content)
    }

    private func apply(_ plaintext: NotificationPlaintext, to content: UNMutableNotificationContent) {
        content.title = plaintext.title
        if let subtitle = plaintext.subtitle { content.subtitle = subtitle }
        content.body = plaintext.body
        if let category = plaintext.category, !category.isEmpty {
            content.categoryIdentifier = category
        }
        // IOS_LIVE.md §3.1 interruption levels (the Hub also sets them in `aps`).
        switch content.categoryIdentifier {
        case "APPROVAL", "APPROVAL_BIOMETRIC", "ALERT":
            content.interruptionLevel = .timeSensitive
        case "BRIEF", "FACT_REVIEW", "SUGGESTION":
            content.interruptionLevel = .passive
        default:
            break
        }
        if let thread = plaintext.threadID ?? plaintext.taskID, !thread.isEmpty {
            content.threadIdentifier = thread
        }
        var info = content.userInfo
        info.removeValue(forKey: EnvelopeFormat.pushPayloadKey)
        if let taskID = plaintext.taskID { info["task_id"] = taskID }
        if let approvalID = plaintext.approvalID { info["approval_id"] = approvalID }
        if let choices = plaintext.choices { info["choices"] = choices }
        if let items = plaintext.items,
           let data = try? JSONEncoder().encode(items),
           let json = String(data: data, encoding: .utf8) {
            // Rendered by the Notification Content extension (cart list / diff summary).
            info["items_json"] = json
        }
        content.userInfo = info
    }

    /// Architecture rule: only fetch from the paired Hub (never third-party hosts).
    private static func isHubURL(_ url: URL) -> Bool {
        guard let configuration = HubConfiguration.load() else { return false }
        return configuration.hubURLs.contains { hub in
            hub.host?.lowercased() == url.host?.lowercased() && hub.scheme == url.scheme
        }
    }

    private static func downloadAttachment(from url: URL) async -> UNNotificationAttachment? {
        do {
            var request = URLRequest(url: url)
            request.timeoutInterval = 20
            if let hub = try? HubClient.shared(),
               let headers = try? await hub.authHeaders() {
                for (name, values) in headers {
                    request.setValue(values.joined(separator: ","), forHTTPHeaderField: name)
                }
            }
            let (temporaryURL, response) = try await URLSession.shared.download(for: request)
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else { return nil }

            let ext = Self.fileExtension(mimeType: http.mimeType, url: url)
            let destination = FileManager.default.temporaryDirectory
                .appendingPathComponent(UUID().uuidString)
                .appendingPathExtension(ext)
            try FileManager.default.moveItem(at: temporaryURL, to: destination)
            return try UNNotificationAttachment(identifier: "nexus-attachment", url: destination, options: nil)
        } catch {
            return nil
        }
    }

    private static func fileExtension(mimeType: String?, url: URL) -> String {
        switch mimeType {
        case "image/png": return "png"
        case "image/jpeg": return "jpg"
        case "image/gif": return "gif"
        case "image/heic": return "heic"
        case "video/mp4": return "mp4"
        default:
            let ext = url.pathExtension
            return ext.isEmpty ? "png" : ext
        }
    }
}
