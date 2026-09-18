import Foundation

/// Every user-facing string the menubar shows (spec §16, §17). Tests compare against
/// literal spec text, never against these constants.
public enum Copy {
    // §17.1 menubar
    public static let appTitle = "Agent Swarm"
    public static func active(_ n: Int) -> String { "\(n) active" }
    /// The header while the daemon is down, matching the label's "?".
    public static let activeUnknown = "? active"
    public static let needsYou = "Needs you"
    public static let agents = "Agents"
    public static let usage = "Usage"
    public static func notifications(unread n: Int) -> String { n > 0 ? "Notifications (\(n) unread)" : "Notifications" }
    public static let readAll = "Read all"
    public static let pauseAll = "Pause all"
    public static let pausing = "Pausing…"
    public static let newOrchestrator = "New orchestrator"
    public static let openBoard = "Open board"
    public static let review = "Review"
    public static let answer = "Answer"
    public static let sendAnswer = "Send answer"
    public static let openTerminal = "Open terminal"
    public static let pause = "Pause"
    public static let pauseGroup = "Pause group"
    public static let resume = "Resume"
    public static let acknowledge = "Acknowledge"
    public static let retry = "Retry"
    public static let cancel = "Cancel"
    public static func finished(_ n: Int) -> String { "Finished (\(n))" }
    public static func viewAllRequests(_ n: Int) -> String { "View all \(n) requests" }
    public static let viewAllNotifications = "View all notifications"
    public static let refresh = "Refresh"
    public static let retryConnection = "Retry connection"
    public static let advisor = "Advisor"
    public static let noAdvisor = "No advisor"
    public static func defaultLevel(_ level: String) -> String { "Default (\(level))" }
    public static let defaultClaudeCode = "Default (Claude Code)"
    public static let refreshModels = "Refresh models"
    public static func modelListsUpdated(_ age: String) -> String { "Model lists updated \(age) ago" }
    public static let advisorCaption = "Claude agents use the built-in advisor. Other agents get a simulated one."
    public static let settings = "Settings"
    public static func removeFolder(_ path: String) -> String { "Remove \(path)" }
    /// DisplayState.label returns nil for .running (it's the unlabelled default state); this is
    /// only the accessibility-string fallback for that case, not a DisplayState value.
    public static let runningLabel = "Running"

    // §17.1 new orchestrator window
    public static func agentName(_ kebab: String) -> String { "Agent name: \(kebab)" }
    public static let name = "Name"
    public static let intent = "Intent"
    public static let featureSpike = "Feature spike"
    public static let debugSpike = "Debug spike"
    public static let featureCaption = "Creates a spike to explore this request and turn it into an epic."
    public static let debugCaption = "Creates a spike to find the root cause and turn it into a bug with a fix plan."
    public static let repositoriesOptional = "Repositories (optional)"
    public static let reposCaption = "The spike suggests repositories and asks you to confirm them."
    public static let searchRepos = "Search repos…"
    public static let recent = "Recent"
    public static let all = "All"
    public static let addFolder = "Add folder…"
    public static let rescan = "Rescan"
    public static func selected(_ names: String) -> String { "Selected: \(names)" }
    public static let role = "Role"
    public static let agent = "Agent"
    public static let model = "Model"
    public static let effort = "Effort"
    public static let defaultsFromSettings = "Defaults from Settings"
    public static let requestOptional = "Request (optional)"
    public static let startOrchestrator = "Start orchestrator"
    public static let queueOrchestrator = "Queue orchestrator"
    public static let tryAgain = "Try again"

    // §17.1 settings
    public static let tabAgents = "Agents"
    public static let tabDefaults = "Defaults"
    public static let tabNotifications = "Notifications"
    public static let tabLimits = "Limits"
    public static let agentsUsedBySwarm = "Agents used by Swarm"
    public static let installed = "Installed"
    public static let notInstalled = "Not installed on this Mac"
    public static let signedIn = "Signed in"
    public static func notSignedIn(_ cmd: String) -> String { "Not signed in. Run `\(cmd)` in a terminal." }
    public static let superpowersMissingRow = "Superpowers missing — orchestrators unavailable"
    public static let checkAgain = "Check again"
    public static let defaultsForNewAgents = "Defaults for new agents"
    public static let notSupported = "Not supported"
    public static let notificationCenter = "Notification Center"
    public static let sound = "Sound"
    public static let levelInfo = "Info"
    public static let levelAttention = "Attention"
    public static let levelAction = "Action required"
    public static let levelInfoCaption = "Task accepted, task completed, epic ready"
    public static let levelAttentionCaption = "Paused, stopped, failed, crashed, worktree kept"
    public static let levelActionCaption = "Questions and approvals"
    public static let notificationsFooter = "Turning off notifications does not hide requests in Needs you."
    public static let maxAgents = "Maximum concurrent agents"
    public static let maxAgentsPerItem = "Maximum concurrent agents per item"
    public static let maxOrchestrators = "Maximum concurrent orchestrators"
    public static let orchestratorLimitCaption = "Orchestrators have their own limit and don't use agent slots."
    public static let pauseDeadline = "Pause deadline"
    public static let seconds = "seconds"
    public static let repositoryDiscovery = "Repository discovery"
    public static let discoveryCaption = "Scans your home folder every 6 hours. Hidden folders and ~/Library are skipped."
    public static let excludedFolders = "Excluded folders"
    public static func lastScan(_ age: String, _ n: Int) -> String { "Last scan: \(age) ago · \(n) repositories" }
    public static let rescanNow = "Rescan now"
    public static let compact = "Compact (icons only)"
    public static let menuBar = "Menu bar:"
    public static let apply = "Apply"

    // §17.2
    public static func roleLabel(_ r: Role) -> String {
        switch r {
        case .orchestrator: return "Orchestrator"
        case .coder: return "Coder"
        case .reviewer: return "Reviewer"
        case .uiReviewer: return "UI reviewer"
        case .researcher: return "Researcher"
        case .debugger: return "Debugger"
        case .mechanical: return "Mechanical"
        }
    }

    /// Row labels on the Defaults tab (§16.4 sketch).
    public static func defaultsRowLabel(_ r: SettingsRole) -> String {
        switch r {
        case .orchestrator: return "Orchestrator"
        case .advisor: return "Advisor"
        case .coder: return "Coding"
        case .reviewer: return "Code review"
        case .uiReviewer: return "UI review"
        case .researcher: return "Research"
        case .debugger: return "Debugging"
        case .mechanical: return "Mechanical"
        }
    }

    public static func agentLabel(_ k: AgentKind) -> String {
        switch k {
        case .claude: return "Claude"
        case .codex: return "Codex"
        case .agy: return "agy"
        case .cursor: return "Cursor"
        case .fake: return "Fake"
        }
    }

    public static func loginCommand(_ k: AgentKind) -> String {
        switch k {
        case .claude: return "claude"
        case .codex: return "codex login"
        case .agy: return "agy"
        case .cursor: return "cursor-agent login"
        case .fake: return "true"
        }
    }

    // §17.3
    public static let nameTaken = "This agent name is already in use."
    public static let scanning = "Scanning your home folder…"
    public static func scannedAgo(_ age: String) -> String { "Scanned \(age) ago" }
    public static let neverScanned = "Never scanned"
    public static let neverFetched = "Never fetched"
    public static let neverUpdated = "Never updated"
    public static let repoMissing = "Repository is unavailable. Choose another location."
    public static let repoDirty = "Has uncommitted changes. The orchestrator works in its own worktree."
    public static func agentNotInstalled(_ agent: String) -> String { "\(agent) isn't installed on this Mac." }
    public static func agentNotSignedIn(_ agent: String, _ cmd: String) -> String { "\(agent) isn't signed in. Run `\(cmd)` in a terminal." }
    public static let chooseAgent = "Choose an agent."
    public static let modelUnavailable = "Choose a model available for this agent."
    public static func superpowersMissing(_ agent: String) -> String { "Install the superpowers plugin for \(agent) to run orchestrators." }
    public static let launchFailed = "Couldn't start orchestrator. Your entries are saved."
    public static let answerNotSent = "Couldn't send your answer. Answer again to retry."
    public static let queuedCaption = "Starts when an agent slot becomes available."
    /// An empty error (nothing to report) drops the sentence instead of leaving a dangling
    /// "Couldn't refresh: " — the same twin bug `catalogNeverFetched` had.
    public static func catalogStale(_ age: String, _ error: String) -> String {
        error.isEmpty ? "Model list from \(age) ago" : "Model list from \(age) ago. Couldn't refresh: \(error)"
    }
    /// The same note for a catalog that was never fetched, where an age would be nonsense.
    /// An empty error (nothing to report) shows the bare label instead of a dangling sentence.
    public static func catalogNeverFetched(_ error: String) -> String {
        error.isEmpty ? neverFetched : "\(neverFetched). Couldn't refresh: \(error)"
    }
    public static func modelGone(_ model: String, _ agent: String) -> String { "\(model) is no longer offered by \(agent)." }
    public static func effortUnavailable(_ level: String, _ model: String) -> String { "\(level) isn't available for \(model); using the default." }
    public static func cancelOrchestrator(_ name: String, _ n: Int) -> String { "Cancel \(name) and its \(n) agents?" }
    public static let lastAgent = "At least one agent must stay enabled."
    public static let compactFallback = "Switched to compact to fit the menu bar."
    public static let settingsSaveFailed = "Couldn't save settings."
    public static let settingsDaemonDown = "Can't save while the daemon is unavailable."
    public static func lowerLimit(_ n: Int) -> String {
        "\(n) agents are running above the new limit. They keep running; new agents wait for a free slot."
    }
    public static func disableAgent(_ agent: String, _ first: String) -> String {
        "Defaults that use \(agent) will switch to \(first). Running agents are not affected."
    }
    public static func daemonDown(_ time: String) -> String { "Daemon unavailable. Showing last known state from \(time)." }

    // §17.4
    public static let emptyNeedsYou = "Nothing needs your attention."
    public static let emptyAgents = "No active agents."
    public static let emptyUsage = "Usage unavailable."
    public static let emptyNotifications = "No notifications."

    // §17.5 request lines in Needs you
    public static let approvePlan = "Approve plan"
    public static let approveReport = "Approve report"
    public static let acceptEpic = "Accept epic"
    public static let acceptFix = "Accept fix"
    public static let confirmRepositories = "Confirm repositories"
    public static func confirmRepositories(_ n: Int) -> String { "Confirm \(n) repositories" }
    public static let closeSpike = "Close spike?"
    public static func approveSection(_ title: String) -> String { "Approve \"\(title)\"" }

    // §14 notification actions
    public static let openItem = "Open item"
    public static let viewAgent = "View agent"
}
