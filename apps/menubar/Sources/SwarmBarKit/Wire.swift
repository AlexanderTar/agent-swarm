import Foundation

// Wire types for the daemon HTTP API. The authority is
// ~/.superpowers/specs/2026-09-17-agent-swarm-contracts.md (§3): snake_case keys,
// integer-millisecond timestamps, optional scalars present as null.
// Swift ignores JSON keys it doesn't declare, so read-only types list only what the
// menubar uses; the fixtures in Tests/Fixtures carry the full shapes for the Go check.

public enum AgentKind: String, Codable, Sendable, CaseIterable {
    case claude, codex, agy, cursor, muse, fake

    /// The five agents a user can enable, in settings order (claude first).
    public static let selectable: [AgentKind] = [.claude, .codex, .agy, .cursor, .muse]
}

public enum Role: String, Codable, Sendable {
    case orchestrator, coder, reviewer, uiReviewer = "ui_reviewer", researcher, debugger, mechanical, designer
}

/// Keys of `Settings.roles`, plus "advisor" (L28) and "fallback" -- the last
/// one isn't a `roles` dictionary key at all (it's `Settings.fallbackDefault`,
/// a top-level field), but the subscript below maps it there so every
/// Defaults-tab row (`SettingsModel.defaultsRows`/`setAgent`/`setModel`/
/// `setEffort`) can treat it exactly like a role default with no other code
/// change (docs/specs/2026-09-19-usage-fallback-agent.md).
public enum SettingsRole: String, Sendable, CaseIterable {
    case orchestrator, advisor, coder, reviewer, uiReviewer = "ui_reviewer", researcher, debugger, mechanical, designer, fallback
}

public enum AgentState: String, Codable, Sendable {
    case queued, active, finished, acknowledged
}

public enum SessionState: String, Codable, Sendable {
    case spawning, running, pauseRequested = "pause_requested", quiescing, stopping, paused
    case interrupted, completed, failed, crashed, cancelled
}

public struct SessionInfo: Codable, Sendable, Equatable {
    public var id: String
    public var state: SessionState
    public var attempt: Int
    public var generation: Int
    public var waiting: Bool
    public var stale: Bool
    public var tmuxAlive: Bool
    public var startedAt: Timestamp
    public var endedAt: Timestamp?

    enum CodingKeys: String, CodingKey {
        case id, state, attempt, generation, waiting, stale
        case tmuxAlive = "tmux_alive", startedAt = "started_at", endedAt = "ended_at"
    }

    public init(id: String = "ses_1", state: SessionState, attempt: Int = 1, generation: Int = 1,
                waiting: Bool = false, stale: Bool = false, tmuxAlive: Bool = true,
                startedAt: Timestamp = Timestamp(ms: 0), endedAt: Timestamp? = nil) {
        self.id = id; self.state = state; self.attempt = attempt; self.generation = generation
        self.waiting = waiting; self.stale = stale; self.tmuxAlive = tmuxAlive
        self.startedAt = startedAt; self.endedAt = endedAt
    }
}

public struct AgentNode: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var name: String
    public var kind: AgentKind
    public var model: String
    /// Stored effort slug, as the Go state wire already serves it (`effort`,
    /// null when the agent default applies). Optional on decode: daemons
    /// that predate it simply omit the key.
    public var effort: String?
    public var role: Role
    public var step: String?
    public var itemKey: String
    public var itemTitle: String
    public var rootKey: String
    public var parentName: String?
    public var state: AgentState
    public var session: SessionInfo?
    /// The in-flight replacement operation, when the coordinator owns one.
    public var replacement: AgentReplacement?
    public var preflightError: String?
    public var children: [AgentNode]
    public var finished: [AgentNode]

    enum CodingKeys: String, CodingKey {
        case id, name, kind, model, effort, role, step, state, session, replacement, children, finished
        case itemKey = "item_key", itemTitle = "item_title", rootKey = "root_key"
        case parentName = "parent_name", preflightError = "preflight_error"
    }

    public init(id: String = "agt_1", name: String, kind: AgentKind = .claude, model: String,
                effort: String? = nil, role: Role = .coder, step: String? = nil,
                itemKey: String = "TASK-1", itemTitle: String = "Task",
                rootKey: String = "EPIC-1", parentName: String? = nil, state: AgentState = .active,
                session: SessionInfo? = SessionInfo(state: .running),
                replacement: AgentReplacement? = nil, preflightError: String? = nil,
                children: [AgentNode] = [], finished: [AgentNode] = []) {
        self.id = id; self.name = name; self.kind = kind; self.model = model; self.effort = effort
        self.role = role; self.step = step
        self.itemKey = itemKey; self.itemTitle = itemTitle; self.rootKey = rootKey
        self.parentName = parentName; self.state = state; self.session = session
        self.replacement = replacement
        self.preflightError = preflightError; self.children = children; self.finished = finished
    }
}

/// What the hover preview header resolves an agent name to: the raw kind,
/// model id, stored effort slug and item key. The panel formats them via
/// Copy.paneHeader (agent display label, CatalogRules.modelLabel, stored
/// effort's human label in parenthesis).
public struct AgentHeader: Sendable, Equatable {
    public var kind: AgentKind
    public var model: String
    public var effort: String?
    public var itemKey: String

    public init(kind: AgentKind, model: String, effort: String? = nil, itemKey: String) {
        self.kind = kind; self.model = model; self.effort = effort; self.itemKey = itemKey
    }
}

/// One agent's in-flight replacement operation (`replacement` on AgentNode):
/// the coordinator's durable walk the Handoff action starts.
public struct AgentReplacement: Codable, Sendable, Equatable {
    public var operationID: String
    public var mode: String
    public var phase: String
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case mode, phase, error
        case operationID = "operation_id"
    }

    public init(operationID: String, mode: String, phase: String, error: String? = nil) {
        self.operationID = operationID; self.mode = mode; self.phase = phase; self.error = error
    }
}

public enum RequestKind: String, Codable, Sendable, CaseIterable {
    case question, prompt, blocker
    case confirmRepos = "confirm_repos", approveSection = "approve_section"
    case approvePlan = "approve_plan", approveReport = "approve_report"
    case acceptEpic = "accept_epic", acceptFix = "accept_fix", closeSpike = "close_spike"
}

public struct SwarmRequest: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var kind: RequestKind
    public var isHITL: Bool
    public var agentName: String?
    public var itemKey: String
    public var itemTitle: String
    public var sectionTitle: String?
    public var prompt: String
    public var state: String
    public var createdAt: Timestamp
    /// `confirm_repos` only: how many repos the agent proposes (`options.proposed`).
    public var proposedRepos: Int?
    public var options: [String]?
    /// The agent whose terminal answers this row; nil for approval kinds. Chosen by the daemon.
    public var terminalAgent: String?
    /// True while an approval's native prompt is open in the asking agent's terminal: the bound
    /// question row already represents it in Needs you, so this request is excluded (spec 2.2.1).
    public var nativePending: Bool

    enum CodingKeys: String, CodingKey {
        case id, kind, prompt, state, options
        case isHITL = "is_hitl"
        case agentName = "agent_name", terminalAgent = "terminal_agent", itemKey = "item_key", itemTitle = "item_title"
        case sectionTitle = "section_title", createdAt = "created_at"
        case nativePending = "native_pending"
    }

    private struct RepoOptions: Codable { var proposed: [Proposal]; struct Proposal: Codable { var repo: String } }

    public init(id: String, kind: RequestKind, isHITL: Bool = false, agentName: String? = nil, itemKey: String = "TASK-1",
                itemTitle: String = "Task", sectionTitle: String? = nil, prompt: String = "",
                state: String = "open", createdAt: Timestamp = Timestamp(ms: 0), proposedRepos: Int? = nil,
                options: [String]? = nil, terminalAgent: String? = nil, nativePending: Bool = false) {
        self.terminalAgent = terminalAgent
        self.id = id; self.kind = kind; self.isHITL = isHITL; self.agentName = agentName; self.itemKey = itemKey
        self.itemTitle = itemTitle; self.sectionTitle = sectionTitle; self.prompt = prompt
        self.state = state; self.createdAt = createdAt; self.proposedRepos = proposedRepos; self.options = options
        self.nativePending = nativePending
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        kind = try c.decode(RequestKind.self, forKey: .kind)
        isHITL = try c.decodeIfPresent(Bool.self, forKey: .isHITL) ?? (kind == .question || kind == .prompt || kind == .blocker)
        agentName = try c.decodeIfPresent(String.self, forKey: .agentName)
        terminalAgent = try c.decodeIfPresent(String.self, forKey: .terminalAgent)
        itemKey = try c.decode(String.self, forKey: .itemKey)
        itemTitle = try c.decode(String.self, forKey: .itemTitle)
        sectionTitle = try c.decodeIfPresent(String.self, forKey: .sectionTitle)
        prompt = try c.decode(String.self, forKey: .prompt)
        state = try c.decode(String.self, forKey: .state)
        createdAt = try c.decode(Timestamp.self, forKey: .createdAt)
        proposedRepos = kind == .confirmRepos
            ? (try? c.decode(RepoOptions.self, forKey: .options))?.proposed.count : nil
        options = try? c.decodeIfPresent([String].self, forKey: .options)
        nativePending = try c.decodeIfPresent(Bool.self, forKey: .nativePending) ?? false
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(kind, forKey: .kind)
        try c.encode(isHITL, forKey: .isHITL)
        try c.encode(agentName, forKey: .agentName)
        try c.encode(terminalAgent, forKey: .terminalAgent)
        try c.encode(itemKey, forKey: .itemKey)
        try c.encode(itemTitle, forKey: .itemTitle)
        try c.encode(sectionTitle, forKey: .sectionTitle)
        try c.encode(prompt, forKey: .prompt)
        try c.encode(state, forKey: .state)
        try c.encode(createdAt, forKey: .createdAt)
        try c.encode(nativePending, forKey: .nativePending)
        if let opts = options {
            try c.encode(opts, forKey: .options)
        } else if let n = proposedRepos {
            try c.encode(RepoOptions(proposed: Array(repeating: .init(repo: ""), count: n)), forKey: .options)
        }
    }
}

public enum NotificationLevel: String, Codable, Sendable, CaseIterable {
    case info, attention, action
}

public struct SwarmNotification: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var level: NotificationLevel
    public var kind: String
    public var title: String
    public var body: String
    public var agentName: String?
    public var itemKey: String?
    public var requestId: String?
    public var readAt: Timestamp?
    public var createdAt: Timestamp

    enum CodingKeys: String, CodingKey {
        case id, level, kind, title, body
        case agentName = "agent_name", itemKey = "item_key", requestId = "request_id"
        case readAt = "read_at", createdAt = "created_at"
    }

    public init(id: String, level: NotificationLevel, kind: String, title: String = "", body: String = "",
                agentName: String? = nil, itemKey: String? = nil, requestId: String? = nil,
                readAt: Timestamp? = nil, createdAt: Timestamp = Timestamp(ms: 0)) {
        self.id = id; self.level = level; self.kind = kind; self.title = title; self.body = body
        self.agentName = agentName; self.itemKey = itemKey; self.requestId = requestId
        self.readAt = readAt; self.createdAt = createdAt
    }
}

public struct NotificationList: Codable, Sendable, Equatable {
    public var unread: Int
    public var items: [SwarmNotification]
    public init(unread: Int = 0, items: [SwarmNotification] = []) { self.unread = unread; self.items = items }
}

public struct Meter: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var label: String
    public var window: String
    public var usedPct: Double
    public var resetsAt: Timestamp?

    enum CodingKeys: String, CodingKey {
        case id, label, window, usedPct = "used_pct", resetsAt = "resets_at"
    }

    public init(id: String, label: String, window: String = "5h", usedPct: Double, resetsAt: Timestamp? = nil) {
        self.id = id; self.label = label; self.window = window; self.usedPct = usedPct; self.resetsAt = resetsAt
    }
}

public struct UsageSnapshot: Codable, Sendable, Equatable {
    public var agent: AgentKind
    public var meters: [Meter]
    public var headlineId: String?
    public var error: String?
    public var fetchedAt: Timestamp
    public var stale: Bool

    enum CodingKeys: String, CodingKey {
        case agent, meters, error, stale, headlineId = "headline_id", fetchedAt = "fetched_at"
    }

    public init(agent: AgentKind, meters: [Meter], headlineId: String? = nil, error: String? = nil,
                fetchedAt: Timestamp = Timestamp(ms: 0), stale: Bool = false) {
        self.agent = agent; self.meters = meters; self.headlineId = headlineId; self.error = error
        self.fetchedAt = fetchedAt; self.stale = stale
    }

    public var headline: Meter? { meters.first { $0.id == headlineId } ?? meters.first }
}

public struct RoleDefault: Codable, Sendable, Equatable {
    public var agent: AgentKind
    public var model: String
    /// "" = the agent's default (L27). Encoded only when non-empty.
    public var effort: String

    enum CodingKeys: String, CodingKey { case agent, model, effort }

    public init(agent: AgentKind, model: String, effort: String = "") {
        self.agent = agent; self.model = model; self.effort = effort
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = try c.decode(AgentKind.self, forKey: .agent)
        model = try c.decode(String.self, forKey: .model)
        effort = try c.decodeIfPresent(String.self, forKey: .effort) ?? ""
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(agent, forKey: .agent)
        try c.encode(model, forKey: .model)
        if !effort.isEmpty { try c.encode(effort, forKey: .effort) }
    }

    /// Settings stores "No advisor" as model "none" (P1 settings.NoAdvisor).
    public static let noAdvisorModel = "none"
}

public struct NotifyPref: Codable, Sendable, Equatable {
    public var center: Bool
    public var sound: Bool
    public init(center: Bool = true, sound: Bool = true) { self.center = center; self.sound = sound }
}

public struct Settings: Codable, Sendable, Equatable {
    public var enabledAgents: [AgentKind]
    public var roles: [String: RoleDefault]
    /// The agent+model substituted when a role's configured agent is
    /// confirmed out of usage (docs/specs/2026-09-19-usage-fallback-agent.md).
    /// Defaulted here (not just in `.defaults` below) so every existing
    /// memberwise `Settings(...)` call site in this codebase's tests keeps
    /// compiling unchanged.
    public var fallbackDefault: RoleDefault = RoleDefault(agent: .claude, model: "sonnet")
    public var notifications: [String: NotifyPref]
    public var maxConcurrentSubagents: Int = 3
    /// The single global admission ceiling shared by every role, orchestrator
    /// included (docs/specs/2026-09-24-unify-agent-limits.md; replaces the old,
    /// separately-counted maxOrchestrators/maxAgents pair).
    public var maxConcurrentAgents: Int
    public var maxAgentsPerRoot: Int
    public var scanExcludes: [String]
    public var scanIntervalSec: Int
    public var menubarCompact: Bool
    public var usagePollSec: Int
    public var pauseDeadlineSec: Int
    /// Durable custom instructions injected into every spawned agent, isolated from the operator's
    /// own global CLAUDE.md/AGENTS.md files (docs/specs/2026-09-22-isolated-mcp-and-custom-instructions.md).
    /// "" = none configured.
    public var instructions: String = ""

    enum CodingKeys: String, CodingKey {
        case roles, notifications, instructions
        case fallbackDefault = "fallback_default"
        case enabledAgents = "enabled_agents"
        case maxConcurrentAgents = "max_concurrent_agents", maxAgentsPerRoot = "max_agents_per_root"
        case maxConcurrentSubagents = "max_concurrent_subagents"
        case scanExcludes = "scan_excludes", scanIntervalSec = "scan_interval_sec"
        case menubarCompact = "menubar_compact", usagePollSec = "usage_poll_sec"
        case pauseDeadlineSec = "pause_deadline_sec"
    }

    /// §6.5 defaults with §2.1 A3 roles. Used before the first successful load.
    public static let defaults = Settings(
        enabledAgents: [.claude],
        roles: [
            "orchestrator": RoleDefault(agent: .claude, model: "opus"),
            "coder": RoleDefault(agent: .claude, model: "sonnet"),
            "reviewer": RoleDefault(agent: .claude, model: "opus"),
            "ui_reviewer": RoleDefault(agent: .claude, model: "opus"),
            "researcher": RoleDefault(agent: .claude, model: "sonnet"),
            "debugger": RoleDefault(agent: .claude, model: "opus"),
            "mechanical": RoleDefault(agent: .claude, model: "haiku"),
            "designer": RoleDefault(agent: .claude, model: "opus"),
            "advisor": RoleDefault(agent: .claude, model: "fable"),
        ],
        fallbackDefault: RoleDefault(agent: .claude, model: "sonnet"),
        notifications: ["info": NotifyPref(), "attention": NotifyPref(), "action": NotifyPref()],
        maxConcurrentSubagents: 3,
        maxConcurrentAgents: 4, maxAgentsPerRoot: 4,
        scanExcludes: ["~/Library", "~/.Trash", "~/Downloads"], scanIntervalSec: 21600,
        menubarCompact: false, usagePollSec: 300, pauseDeadlineSec: 120)

    /// `.fallback` isn't a `roles` dictionary key (see `SettingsRole`'s own
    /// doc comment): it reads/writes `fallbackDefault` directly, which is
    /// what lets every `SettingsModel` Defaults-tab method work for it with
    /// no other change.
    public subscript(role: SettingsRole) -> RoleDefault? {
        get { role == .fallback ? fallbackDefault : roles[role.rawValue] }
        set {
            if role == .fallback {
                if let newValue { fallbackDefault = newValue }
            } else {
                roles[role.rawValue] = newValue
            }
        }
    }

    public init(enabledAgents: [AgentKind] = [.claude], roles: [String: RoleDefault] = [:],
                fallbackDefault: RoleDefault = RoleDefault(agent: .claude, model: "sonnet"),
                notifications: [String: NotifyPref] = [:], maxConcurrentSubagents: Int = 3,
                maxConcurrentAgents: Int = 4, maxAgentsPerRoot: Int = 4,
                scanExcludes: [String] = [], scanIntervalSec: Int = 21600,
                menubarCompact: Bool = false, usagePollSec: Int = 300, pauseDeadlineSec: Int = 120,
                instructions: String = "") {
        self.enabledAgents = enabledAgents
        self.roles = roles
        self.fallbackDefault = fallbackDefault
        self.notifications = notifications
        self.maxConcurrentSubagents = maxConcurrentSubagents
        self.maxConcurrentAgents = maxConcurrentAgents
        self.maxAgentsPerRoot = maxAgentsPerRoot
        self.scanExcludes = scanExcludes
        self.scanIntervalSec = scanIntervalSec
        self.menubarCompact = menubarCompact
        self.usagePollSec = usagePollSec
        self.pauseDeadlineSec = pauseDeadlineSec
        self.instructions = instructions
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabledAgents = try c.decode([AgentKind].self, forKey: .enabledAgents)
        roles = try c.decode([String: RoleDefault].self, forKey: .roles)
        fallbackDefault = try c.decodeIfPresent(RoleDefault.self, forKey: .fallbackDefault) ?? RoleDefault(agent: .claude, model: "sonnet")
        notifications = try c.decode([String: NotifyPref].self, forKey: .notifications)
        maxConcurrentSubagents = try c.decodeIfPresent(Int.self, forKey: .maxConcurrentSubagents) ?? 3
        // decodeIfPresent, not decode: a daemon that predates this rename (or a
        // stale fixture) sends no max_concurrent_agents key at all -- falling
        // back to the shipped default rather than throwing keeps the app usable
        // against it, the same tolerance maxConcurrentSubagents/instructions use.
        maxConcurrentAgents = try c.decodeIfPresent(Int.self, forKey: .maxConcurrentAgents) ?? 4
        maxAgentsPerRoot = try c.decode(Int.self, forKey: .maxAgentsPerRoot)
        scanExcludes = try c.decode([String].self, forKey: .scanExcludes)
        scanIntervalSec = try c.decode(Int.self, forKey: .scanIntervalSec)
        menubarCompact = try c.decode(Bool.self, forKey: .menubarCompact)
        usagePollSec = try c.decode(Int.self, forKey: .usagePollSec)
        pauseDeadlineSec = try c.decode(Int.self, forKey: .pauseDeadlineSec)
        instructions = try c.decodeIfPresent(String.self, forKey: .instructions) ?? ""
    }

    /// Hand-written (like `RoleDefault.encode`) so an empty `instructions` -- the common case --
    /// is omitted rather than encoded as `""`, keeping the wire body byte-for-byte unchanged for
    /// every daemon and fixture that predates this field.
    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(enabledAgents, forKey: .enabledAgents)
        try c.encode(roles, forKey: .roles)
        try c.encode(fallbackDefault, forKey: .fallbackDefault)
        try c.encode(notifications, forKey: .notifications)
        try c.encode(maxConcurrentSubagents, forKey: .maxConcurrentSubagents)
        try c.encode(maxConcurrentAgents, forKey: .maxConcurrentAgents)
        try c.encode(maxAgentsPerRoot, forKey: .maxAgentsPerRoot)
        try c.encode(scanExcludes, forKey: .scanExcludes)
        try c.encode(scanIntervalSec, forKey: .scanIntervalSec)
        try c.encode(menubarCompact, forKey: .menubarCompact)
        try c.encode(usagePollSec, forKey: .usagePollSec)
        try c.encode(pauseDeadlineSec, forKey: .pauseDeadlineSec)
        if !instructions.isEmpty { try c.encode(instructions, forKey: .instructions) }
    }

    public func pref(_ level: NotificationLevel) -> NotifyPref {
        notifications[level.rawValue] ?? NotifyPref()
    }
}

public struct StateResponse: Codable, Sendable, Equatable {
    public var agents: [AgentNode]
    public var requests: [SwarmRequest]
    public var notifications: NotificationList
    public var usage: [UsageSnapshot]
    public var activeCount: Int
    public var settings: Settings

    enum CodingKeys: String, CodingKey {
        case agents, requests, notifications, usage, settings, activeCount = "active_count"
    }

    public init(agents: [AgentNode] = [], requests: [SwarmRequest] = [], notifications: NotificationList = NotificationList(),
                usage: [UsageSnapshot] = [], activeCount: Int = 0, settings: Settings = .defaults) {
        self.agents = agents; self.requests = requests; self.notifications = notifications
        self.usage = usage; self.activeCount = activeCount; self.settings = settings
    }
}

public struct CatalogModel: Codable, Sendable, Equatable {
    public var id: String
    public var label: String
    public var aliases: [String]
    public var efforts: [String]
    public var defaultEffort: String
    public var advisorCapable: Bool
    public var hidden: Bool

    enum CodingKeys: String, CodingKey {
        case id, label, aliases, efforts, hidden
        case defaultEffort = "default_effort", advisorCapable = "advisor_capable"
    }

    public init(id: String, label: String? = nil, aliases: [String] = [], efforts: [String] = [],
                defaultEffort: String = "", advisorCapable: Bool = false, hidden: Bool = false) {
        self.id = id; self.label = label ?? id; self.aliases = aliases; self.efforts = efforts
        self.defaultEffort = defaultEffort; self.advisorCapable = advisorCapable; self.hidden = hidden
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        label = try c.decode(String.self, forKey: .label)
        aliases = try c.decodeIfPresent([String].self, forKey: .aliases) ?? []
        efforts = try c.decodeIfPresent([String].self, forKey: .efforts) ?? []
        defaultEffort = try c.decodeIfPresent(String.self, forKey: .defaultEffort) ?? ""
        advisorCapable = try c.decodeIfPresent(Bool.self, forKey: .advisorCapable) ?? false
        hidden = try c.decodeIfPresent(Bool.self, forKey: .hidden) ?? false
    }
}

/// `version`, `auth_error`, `default_model` and `catalog_error` are plain strings, "" when empty (contracts §3.5).
public struct AgentCatalogEntry: Codable, Sendable, Equatable {
    public var kind: AgentKind
    public var installed: Bool
    public var version: String
    public var authOk: Bool
    public var authError: String
    public var superpowers: Bool
    public var models: [CatalogModel]
    public var defaultModel: String
    public var catalogSource: String
    public var catalogFetchedAt: Timestamp
    public var catalogStale: Bool
    public var catalogError: String

    enum CodingKeys: String, CodingKey {
        case kind, installed, version, superpowers, models
        case authOk = "auth_ok", authError = "auth_error", defaultModel = "default_model"
        case catalogSource = "catalog_source", catalogFetchedAt = "catalog_fetched_at"
        case catalogStale = "catalog_stale", catalogError = "catalog_error"
    }

    public init(kind: AgentKind, installed: Bool = true, version: String = "1.0", authOk: Bool = true,
                authError: String = "", superpowers: Bool = true, models: [CatalogModel] = [], defaultModel: String = "",
                catalogSource: String = "test", catalogFetchedAt: Timestamp = Timestamp(ms: 0),
                catalogStale: Bool = false, catalogError: String = "") {
        self.kind = kind; self.installed = installed; self.version = version; self.authOk = authOk
        self.authError = authError; self.superpowers = superpowers; self.models = models
        self.defaultModel = defaultModel; self.catalogSource = catalogSource
        self.catalogFetchedAt = catalogFetchedAt; self.catalogStale = catalogStale; self.catalogError = catalogError
    }
}

public struct Repo: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var name: String
    public var path: String
    public var remoteUrl: String?
    public var remoteOwner: String?
    public var defaultBranch: String?
    public var source: String // scan | manual
    public var groups: [String]
    public var missing: Bool
    public var dirty: Bool
    public var lastUsedAt: Timestamp?

    enum CodingKeys: String, CodingKey {
        case id, name, path, source, groups, missing, dirty
        case remoteUrl = "remote_url", remoteOwner = "remote_owner", defaultBranch = "default_branch"
        case lastUsedAt = "last_used_at"
    }

    public init(id: String, name: String, path: String, remoteUrl: String? = nil, remoteOwner: String? = nil,
                defaultBranch: String? = nil, source: String = "scan", groups: [String] = [],
                missing: Bool = false, dirty: Bool = false, lastUsedAt: Timestamp? = nil) {
        self.id = id; self.name = name; self.path = path; self.remoteUrl = remoteUrl; self.remoteOwner = remoteOwner
        self.defaultBranch = defaultBranch; self.source = source; self.groups = groups
        self.missing = missing; self.dirty = dirty; self.lastUsedAt = lastUsedAt
    }
}

public struct RepoGroup: Codable, Sendable, Equatable {
    public var name: String
    public var source: String // remote_owner | workspace_dir | code_workspace
    public var repos: [Repo]
    public init(name: String, source: String, repos: [Repo]) { self.name = name; self.source = source; self.repos = repos }
}

public struct ReposResponse: Codable, Sendable, Equatable {
    public var recent: [Repo]
    public var groups: [RepoGroup]
    public var all: [Repo]
    public var scannedAt: Timestamp
    public var scanning: Bool

    enum CodingKeys: String, CodingKey {
        case recent, groups, all, scanning, scannedAt = "scanned_at"
    }

    public init(recent: [Repo] = [], groups: [RepoGroup] = [], all: [Repo] = [],
                scannedAt: Timestamp = Timestamp(ms: 0), scanning: Bool = false) {
        self.recent = recent; self.groups = groups; self.all = all; self.scannedAt = scannedAt; self.scanning = scanning
    }
}

public struct ScanStats: Codable, Sendable, Equatable {
    public var found: Int
    public var missing: Int
    public init(found: Int, missing: Int) { self.found = found; self.missing = missing }
}

/// `advisor` in POST /api/spikes: an agent/model pair, or the string "none".
public enum AdvisorPayload: Codable, Sendable, Equatable {
    case none
    case pair(agent: AgentKind, model: String, effort: String?)

    enum CodingKeys: String, CodingKey { case agent, model, effort }

    public init(from decoder: Decoder) throws {
        if let s = try? decoder.singleValueContainer().decode(String.self), s == "none" {
            self = .none
            return
        }
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self = .pair(agent: try c.decode(AgentKind.self, forKey: .agent),
                     model: try c.decode(String.self, forKey: .model),
                     effort: try c.decodeIfPresent(String.self, forKey: .effort))
    }

    public func encode(to encoder: Encoder) throws {
        switch self {
        case .none:
            var c = encoder.singleValueContainer()
            try c.encode("none")
        case let .pair(agent, model, effort):
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(agent, forKey: .agent)
            try c.encode(model, forKey: .model)
            try c.encodeIfPresent(effort, forKey: .effort)
        }
    }
}

public enum SpikeIntent: String, Codable, Sendable, CaseIterable {
    case chore, feature, debug
}

public struct CreateSpikeBody: Codable, Sendable, Equatable {
    public var requestId: String
    public var name: String
    public var intent: SpikeIntent
    public var repos: [String]
    public var agent: AgentKind
    public var model: String
    public var effort: String?
    public var advisor: AdvisorPayload
    public var request: String?

    enum CodingKeys: String, CodingKey {
        case name, intent, repos, agent, model, effort, advisor, request, requestId = "request_id"
    }

    public init(requestId: String, name: String, intent: SpikeIntent, repos: [String], agent: AgentKind,
                model: String, effort: String?, advisor: AdvisorPayload, request: String?) {
        self.requestId = requestId; self.name = name; self.intent = intent; self.repos = repos
        self.agent = agent; self.model = model; self.effort = effort; self.advisor = advisor; self.request = request
    }
}

public struct CreateSpikeResponse: Codable, Sendable, Equatable {
    public var agent: AgentNode
    public var queued: Bool
    public init(agent: AgentNode, queued: Bool) { self.agent = agent; self.queued = queued }
}

public enum AgentEndpoint: String, Sendable, CaseIterable {
    case pause, resume, cancel, ack, retry, terminal, handoff
}

public enum PauseScope: String, Codable, Sendable {
    case session, subtree
}

/// `terminal.open` SSE payload (§7.1).
public struct TerminalOpen: Codable, Sendable, Equatable {
    public var name: String
    public var tmux: String
}

/// GET /api/agents/{name}/pane (agent-hover-preview spec). `text` is already ANSI-stripped by
/// the daemon; `ansi` is the raw capture with SGR kept (nil from daemons that predate it).
public struct PaneCapture: Codable, Sendable, Equatable {
    public var text: String
    public var ansi: String?
    public var tmuxAlive: Bool
    public var lines: Int

    enum CodingKeys: String, CodingKey {
        case text, ansi, lines
        case tmuxAlive = "tmux_alive"
    }

    public init(text: String, ansi: String? = nil, tmuxAlive: Bool = true, lines: Int = 40) {
        self.text = text
        self.ansi = ansi
        self.tmuxAlive = tmuxAlive
        self.lines = lines
    }
}

public struct APIErrorBody: Codable, Sendable, Equatable {
    public struct Detail: Codable, Sendable, Equatable {
        public var code: String
        public var message: String
    }
    public var error: Detail
}
