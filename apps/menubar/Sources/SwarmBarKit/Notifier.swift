import Foundation

/// Board URL fragments (contracts §6): the inbox is `#/inbox?req=<id>`.
public enum BoardLink {
    public static let inbox = "/inbox"
    public static func request(_ id: String) -> String { "/inbox?req=\(id)" }
    public static func item(_ key: String) -> String { "/hierarchy?item=\(key)" }
}

public struct NotificationActionSpec: Equatable, Sendable {
    public var id: String
    public var title: String
    public var textInput: Bool
}

public struct NotificationCategorySpec: Equatable, Sendable {
    public var id: String
    public var actions: [NotificationActionSpec]
}

/// A Notification Center request, independent of UserNotifications so tests can inspect it.
public struct PostedNotification: Equatable, Sendable {
    public var id: String
    public var title: String
    public var body: String
    public var category: String
    public var sound: Bool
    public var userInfo: [String: String]
}

@MainActor
public protocol NotificationPosting: AnyObject {
    func requestAuthorization() async
    func register(_ categories: [NotificationCategorySpec])
    func post(_ notification: PostedNotification) async
}

public enum NotificationAction {
    public static let openItem = "open_item"
    public static let openTerminal = "open_terminal"
    public static let viewAgent = "view_agent"
    public static let answer = "answer"
    public static let review = "review"
    public static let startOrchestrator = "start_orchestrator"
    /// UNNotificationDefaultActionIdentifier: the user clicked the banner.
    public static let `default` = "com.apple.UNNotificationDefaultActionIdentifier"
}

/// Posts `notification.created` events to Notification Center (§14) and performs their actions.
@MainActor
public final class Notifier {
    public static let categories: [NotificationCategorySpec] = [
        NotificationCategorySpec(id: "swarm.info", actions: [
            NotificationActionSpec(id: NotificationAction.openItem, title: Copy.openItem, textInput: false),
        ]),
        NotificationCategorySpec(id: "swarm.agent", actions: [
            NotificationActionSpec(id: NotificationAction.openTerminal, title: Copy.openTerminal, textInput: false),
            NotificationActionSpec(id: NotificationAction.viewAgent, title: Copy.viewAgent, textInput: false),
        ]),
        NotificationCategorySpec(id: "swarm.question", actions: [
            NotificationActionSpec(id: NotificationAction.answer, title: Copy.answer, textInput: true),
            NotificationActionSpec(id: NotificationAction.openTerminal, title: Copy.openTerminal, textInput: false),
        ]),
        NotificationCategorySpec(id: "swarm.approval", actions: [
            NotificationActionSpec(id: NotificationAction.review, title: Copy.review, textInput: false),
        ]),
        NotificationCategorySpec(id: "swarm.item", actions: [
            NotificationActionSpec(id: NotificationAction.startOrchestrator, title: Copy.startOrchestrator, textInput: false),
            NotificationActionSpec(id: NotificationAction.openItem, title: Copy.openItem, textInput: false),
        ]),
    ]

    /// §17.5 kind → category.
    public static func category(forKind kind: String) -> String {
        switch kind {
        case "item.created": return "swarm.item"
        case "agent.paused", "agent.interrupted", "agent.failed", "agent.crashed", "agent.stale", "agent.undeliverable":
            return "swarm.agent"
        case "request.question": return "swarm.question"
        default: return kind.hasPrefix("request.") ? "swarm.approval" : "swarm.info"
        }
    }

    private let poster: NotificationPosting
    private let client: DaemonClient
    private let openBoard: @MainActor (String) -> Void
    private let openTerminal: @MainActor (String) async -> Void

    public init(poster: NotificationPosting, client: DaemonClient,
                openBoard: @escaping @MainActor (String) -> Void,
                openTerminal: @escaping @MainActor (String) async -> Void) {
        self.poster = poster
        self.client = client
        self.openBoard = openBoard
        self.openTerminal = openTerminal
    }

    public func start() async {
        poster.register(Self.categories)
        await poster.requestAuthorization()
    }

    /// Nothing is posted when Notification Center is off for the level; the popover list still fills (§23.4).
    public func deliver(_ n: SwarmNotification, settings: Settings) async {
        let pref = settings.pref(n.level)
        guard pref.center else { return }
        var info = ["id": n.id, "kind": n.kind]
        info["agent"] = n.agentName
        info["item"] = n.itemKey
        info["request"] = n.requestId
        await poster.post(PostedNotification(id: n.id, title: n.title, body: n.body,
                                             category: Self.category(forKind: n.kind), sound: pref.sound, userInfo: info))
    }

    /// Approvals are never granted here (§14): Review only opens the board.
    public func handle(action: String, userInfo: [String: String], text: String?) async {
        switch action {
        case NotificationAction.answer:
            guard let req = userInfo["request"], let text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
            do {
                try await client.answer(requestID: req, text: text)
            } catch {
                await answerFailed(req, text, userInfo)
            }
        case NotificationAction.openTerminal:
            if let agent = userInfo["agent"] { await openTerminal(agent) }
        case NotificationAction.review:
            if let req = userInfo["request"] { openBoard(BoardLink.request(req)) }
        case NotificationAction.openItem, NotificationAction.viewAgent, NotificationAction.startOrchestrator:
            openBoard(userInfo["item"].map(BoardLink.item) ?? "")
        default:
            if let req = userInfo["request"] {
                openBoard(BoardLink.request(req))
            } else {
                openBoard(userInfo["item"].map(BoardLink.item) ?? "")
            }
        }
    }

    /// The answer didn't reach the daemon, and a notification action has no other way to say so. Posting it
    /// back in the question category keeps the typed text visible and the Answer field one tap away, so the
    /// retry needs nothing the user has to type again. `text` also rides in `userInfo` for the popover draft.
    private func answerFailed(_ request: String, _ text: String, _ userInfo: [String: String]) async {
        let id = "answer-failed:\(request)"
        var info = ["id": id, "kind": "answer.failed", "request": request, "text": text]
        info["item"] = userInfo["item"]
        await poster.post(PostedNotification(id: id, title: Copy.answerNotSent, body: text,
                                             category: "swarm.question", sound: true, userInfo: info))
    }
}
