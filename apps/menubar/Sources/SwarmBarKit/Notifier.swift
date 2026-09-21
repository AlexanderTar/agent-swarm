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
    public static let openOrchestrator = "open_orchestrator"
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
            NotificationActionSpec(id: NotificationAction.openItem, title: Copy.openItem),
        ]),
        NotificationCategorySpec(id: "swarm.agent", actions: [
            NotificationActionSpec(id: NotificationAction.openTerminal, title: Copy.openTerminal),
            NotificationActionSpec(id: NotificationAction.viewAgent, title: Copy.viewAgent),
        ]),
        NotificationCategorySpec(id: "swarm.question", actions: [
            NotificationActionSpec(id: NotificationAction.openOrchestrator, title: Copy.openOrchestratorTerminal),
        ]),
        NotificationCategorySpec(id: "swarm.approval", actions: [
            NotificationActionSpec(id: NotificationAction.review, title: Copy.review),
        ]),
        NotificationCategorySpec(id: "swarm.item", actions: [
            NotificationActionSpec(id: NotificationAction.startOrchestrator, title: Copy.startOrchestrator),
            NotificationActionSpec(id: NotificationAction.openItem, title: Copy.openItem),
        ]),
    ]

    /// §17.5 kind → category.
    public static func category(forKind kind: String) -> String {
        switch kind {
        case "item.created": return "swarm.item"
        case "agent.paused", "agent.interrupted", "agent.failed", "agent.crashed", "agent.stale", "agent.undeliverable":
            return "swarm.agent"
        case "request.question", "request.prompt", "request.blocker": return "swarm.question"
        default: return kind.hasPrefix("request.") ? "swarm.approval" : "swarm.info"
        }
    }

    /// Question, permission and blocker rows are answered in a terminal, never on the board.
    private static func opensTerminal(_ kind: String) -> Bool { category(forKind: kind) == "swarm.question" }

    private let poster: NotificationPosting
    private let client: DaemonClient
    private let openBoard: @MainActor (String) -> Void
    private let openTerminal: @MainActor (String) async -> Void
    /// Request id in; the terminal the daemon named for that request out (`AppModel.openRequest`).
    private let openRequestTerminal: @MainActor (String) async -> Void

    public init(poster: NotificationPosting, client: DaemonClient,
                openBoard: @escaping @MainActor (String) -> Void,
                openTerminal: @escaping @MainActor (String) async -> Void,
                openRequestTerminal: @escaping @MainActor (String) async -> Void) {
        self.poster = poster
        self.client = client
        self.openBoard = openBoard
        self.openTerminal = openTerminal
        self.openRequestTerminal = openRequestTerminal
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

    /// Approvals are never granted here (§14): Review only opens the board. Questions are never answered here either.
    public func handle(action: String, userInfo: [String: String], text: String?) async {
        switch action {
        case NotificationAction.openOrchestrator:
            if let req = userInfo["request"] { await openRequestTerminal(req) }
        case NotificationAction.openTerminal:
            if let agent = userInfo["agent"] { await openTerminal(agent) }
        case NotificationAction.review:
            if let req = userInfo["request"] { openBoard(BoardLink.request(req)) }
        case NotificationAction.openItem, NotificationAction.viewAgent, NotificationAction.startOrchestrator:
            openBoard(userInfo["item"].map(BoardLink.item) ?? "")
        default:
            if let req = userInfo["request"], Self.opensTerminal(userInfo["kind"] ?? "") {
                await openRequestTerminal(req)
            } else if let req = userInfo["request"] {
                openBoard(BoardLink.request(req))
            } else {
                openBoard(userInfo["item"].map(BoardLink.item) ?? "")
            }
        }
    }
}
