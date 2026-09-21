import AppKit
import SwarmBarKit
import UserNotifications

/// NSAppleScript on the main thread. The first run triggers the Automation prompt for Ghostty.
final class AppleScriptRunner: ScriptRunning {
    struct ScriptError: Error, CustomStringConvertible {
        let description: String
    }

    @MainActor
    func run(_ source: String) async throws -> String {
        var info: NSDictionary?
        guard let script = NSAppleScript(source: source) else { throw ScriptError(description: "Couldn't compile the script.") }
        let result = script.executeAndReturnError(&info)
        if let info { throw ScriptError(description: "\(info[NSAppleScript.errorMessage] ?? info)") }
        return result.stringValue ?? ""
    }
}

/// Notification Center through UserNotifications (§14).
@MainActor
final class UserNotificationPoster: NSObject, NotificationPosting, UNUserNotificationCenterDelegate {
    var onAction: (@MainActor (String, [String: String], String?) async -> Void)?
    private let center = UNUserNotificationCenter.current()

    override init() {
        super.init()
        center.delegate = self
    }

    func requestAuthorization() async {
        _ = try? await center.requestAuthorization(options: [.alert, .sound])
    }

    func register(_ categories: [NotificationCategorySpec]) {
        center.setNotificationCategories(Set(categories.map { spec in
            UNNotificationCategory(identifier: spec.id, actions: spec.actions.map { a in
                UNNotificationAction(identifier: a.id, title: a.title, options: [.foreground])
            }, intentIdentifiers: [])
        }))
    }

    func post(_ n: PostedNotification) async {
        let content = UNMutableNotificationContent()
        content.title = n.title
        content.body = n.body
        content.categoryIdentifier = n.category
        content.userInfo = n.userInfo
        content.sound = n.sound ? .default : nil
        try? await center.add(UNNotificationRequest(identifier: n.id, content: content, trigger: nil))
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        [.banner, .sound, .list]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo.reduce(into: [String: String]()) { out, pair in
            if let k = pair.key as? String, let v = pair.value as? String { out[k] = v }
        }
        let text = (response as? UNTextInputNotificationResponse)?.userText
        let action = response.actionIdentifier
        let handler = await onAction
        await handler?(action, info, text)
    }
}

/// Used when the binary runs outside Swarm.app (`swift run`), where UserNotifications would crash.
@MainActor
final class SilentPoster: NotificationPosting {
    func requestAuthorization() async {}
    func register(_ categories: [NotificationCategorySpec]) {}
    func post(_ notification: PostedNotification) async {}
}

enum Ghostty {
    static let bundleID = "com.mitchellh.ghostty"

    @MainActor
    static func pids() -> Set<Int32> {
        Set(NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).map(\.processIdentifier))
    }
}
