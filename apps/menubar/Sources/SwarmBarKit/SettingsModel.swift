import Foundation
import Observation

/// The Settings window (§16.4). Every change is sent with `PUT /api/settings` right away.
@MainActor
@Observable
public final class SettingsModel {
    public struct AgentRow: Equatable, Identifiable {
        public var kind: AgentKind
        public var label: String
        public var checked: Bool
        public var checkboxDisabled: Bool
        /// "Installed · 2.1.274 · Signed in" or "Not installed on this Mac".
        public var status: String
        public var signInNote: String?
        public var superpowersNote: String?
        public var id: AgentKind { kind }
    }

    public struct DefaultsRow: Equatable, Identifiable {
        public var role: SettingsRole
        public var label: String
        public var agent: String
        public var agentOptions: [PickerOption]
        public var model: String
        public var modelOptions: [PickerOption]
        /// nil shows "Not supported" (or nothing for "No advisor").
        public var effortOptions: [PickerOption]?
        public var effort: String
        public var error: String?
        public var note: String?
        public var id: SettingsRole { role }
    }

    public struct LevelRow: Equatable, Identifiable {
        public var level: NotificationLevel
        public var label: String
        public var caption: String
        public var center: Bool
        public var sound: Bool
        public var soundDisabled: Bool
        public var id: NotificationLevel { level }
    }

    public enum Limit: CaseIterable, Sendable {
        case agents, subagents, pauseDeadline

        public var range: ClosedRange<Int> {
            switch self {
            case .agents: return 1...32
            case .subagents: return 1...16
            case .pauseDeadline: return 30...600
            }
        }
    }

    public static let defaultsOrder: [SettingsRole] = [.orchestrator, .advisor, .coder, .reviewer, .uiReviewer, .researcher, .debugger, .mechanical, .fallback]
    static let alwaysSkipped: Set<String> = ["~/Library", "~/.Trash"]

    public private(set) var settings: Settings
    public private(set) var catalog: [AgentCatalogEntry] = []
    public private(set) var repos = ReposResponse()
    public var connected: Bool
    public var agents: [AgentNode]
    public private(set) var agentsError: String?
    public private(set) var pendingDisable: AgentKind?
    public private(set) var pendingLimit: (limit: Limit, value: Int)?
    public private(set) var saveError: String?
    public private(set) var notes: [SettingsRole: String] = [:]
    public private(set) var incomplete: [SettingsRole: String] = [:]
    public var compact: Bool
    private let client: DaemonClient
    private let format: Format
    private let home: String
    private let onCompactChange: @MainActor (Bool) -> Void

    public init(client: DaemonClient, settings: Settings, agents: [AgentNode], connected: Bool, compact: Bool,
                format: Format = Format(), home: String = NSHomeDirectory(),
                onCompactChange: @escaping @MainActor (Bool) -> Void = { _ in }) {
        self.client = client
        self.settings = settings
        self.agents = agents
        self.connected = connected
        self.compact = compact
        self.format = format
        self.home = home
        self.onCompactChange = onCompactChange
    }

    public func load() async {
        async let c = try? client.catalog()
        async let r = try? client.repos(query: "")
        catalog = await c ?? []
        repos = await r ?? ReposResponse()
        normalizeStoredEfforts()
    }

    /// A stored effort the catalog no longer offers for its model must never survive into a save
    /// (L27): normalise it into `settings` itself, at the source, so `defaultsRows` stays a pure
    /// projection and `setAgent`/`setModel` can't rebuild `AgentChoice` from a still-stale raw value.
    ///
    /// Mirrors `goneModel`'s guard: a missing or empty-`models` entry (a failed `/api/catalog`, or an
    /// agent that isn't installed or was never probed — `catalog/service.go:101` serves exactly that)
    /// means there is nothing to check the stored level against, not that every level is invalid. Wiping
    /// it here would be silent (no row error, `blocked` stays false) and would ride out on the very next
    /// unrelated save.
    private func normalizeStoredEfforts() {
        for role in Self.defaultsOrder {
            guard let d = settings[role] else { continue }
            guard let e = CatalogRules.entry(catalog, d.agent), !e.models.isEmpty else { continue }
            let effort = CatalogRules.normalizeEffort(d.agent, CatalogRules.resolve(e, d.model), d.effort)
            if effort != d.effort {
                settings[role] = RoleDefault(agent: d.agent, model: d.model, effort: effort)
            }
        }
    }

    // MARK: saving

    private var blocked: Bool {
        !incomplete.isEmpty || defaultsRows.contains { $0.error != nil }
    }

    public func save() async {
        guard connected else {
            saveError = Copy.settingsDaemonDown
            return
        }
        guard !blocked else { return }
        do {
            settings = try await client.saveSettings(settings)
            // The daemon's own response is adopted verbatim; normalise it the same way a load is, so a
            // response that carries a level the (locally cached) catalog no longer offers can't sit
            // unnormalised until the next edit happens to touch that role.
            normalizeStoredEfforts()
            saveError = nil
        } catch {
            saveError = Copy.settingsSaveFailed
        }
    }

    // MARK: Agents tab

    public var agentRows: [AgentRow] {
        AgentKind.selectable.map { kind in
            let e = CatalogRules.entry(catalog, kind)
            let installed = e?.installed ?? false
            var status = Copy.notInstalled
            if let e, installed {
                status = [Copy.installed, e.version, e.authOk ? Copy.signedIn : nil].compactMap { $0 }
                    .filter { !$0.isEmpty }.joined(separator: " · ")
            }
            return AgentRow(kind: kind, label: Copy.agentLabel(kind), checked: settings.enabledAgents.contains(kind),
                            checkboxDisabled: !installed,
                            status: status,
                            signInNote: installed && !(e?.authOk ?? false) ? Copy.notSignedIn(Copy.loginCommand(kind)) : nil,
                            superpowersNote: installed && !(e?.superpowers ?? false) ? Copy.superpowersMissingRow : nil)
        }
    }

    public func setEnabled(_ kind: AgentKind, _ on: Bool) async {
        agentsError = nil
        if on {
            pendingDisable = nil
            guard !settings.enabledAgents.contains(kind) else { return }
            settings.enabledAgents = AgentKind.selectable.filter { settings.enabledAgents.contains($0) || $0 == kind }
            await save()
            return
        }
        let remaining = settings.enabledAgents.filter { $0 != kind }
        guard !remaining.isEmpty else {
            agentsError = Copy.lastAgent
            return
        }
        pendingDisable = kind
    }

    /// "Defaults that use Codex will switch to Claude. Running agents are not affected."
    public var disableNotice: String? {
        guard let kind = pendingDisable,
              let first = AgentKind.selectable.first(where: { $0 != kind && settings.enabledAgents.contains($0) }) else { return nil }
        return Copy.disableAgent(Copy.agentLabel(kind), Copy.agentLabel(first))
    }

    /// The daemon switches the affected defaults (I18) and returns the saved settings.
    public func applyDisable() async {
        guard let kind = pendingDisable else { return }
        pendingDisable = nil
        settings.enabledAgents.removeAll { $0 == kind }
        await save()
    }

    public func cancelDisable() { pendingDisable = nil }

    public func checkAgain() async {
        if let c = try? await client.refreshCatalog() {
            catalog = c
            normalizeStoredEfforts()
        }
    }

    // MARK: Defaults tab

    public var defaultsRows: [DefaultsRow] {
        Self.defaultsOrder.compactMap { role in
            guard let d = settings[role] else { return nil }
            let entry = CatalogRules.entry(catalog, d.agent)
            let isAdvisor = role == .advisor
            let none = isAdvisor && d.model == RoleDefault.noAdvisorModel
            var models = CatalogRules.modelOptions(entry, advisorOnly: isAdvisor && d.agent == .claude)
            if isAdvisor { models.append(PickerOption(RoleDefault.noAdvisorModel, Copy.noAdvisor)) }
            let model = CatalogRules.resolve(entry, d.model)
            // A stored model that no longer resolves (and isn't the advisor's own "none" sentinel)
            // shows the first available model rather than an empty picker; this is display-only, so
            // it never silently overwrites `settings` — `goneModel` below still flags the mismatch.
            let displayModel = (model != nil || none) ? d.model : (models.first?.value ?? d.model)
            // `d.effort` is already normalised at the source (`normalizeStoredEfforts`, called from
            // `load()`/`checkAgain()`), so this is a pure projection of `settings`.
            return DefaultsRow(role: role, label: Copy.defaultsRowLabel(role), agent: d.agent.rawValue,
                               agentOptions: CatalogRules.agentOptions(enabled: settings.enabledAgents),
                               model: displayModel, modelOptions: models,
                               effortOptions: none ? nil : CatalogRules.effortOptions(d.agent, model),
                               effort: d.effort,
                               error: incomplete[role] ?? CatalogRules.goneModel(d, catalog: catalog),
                               note: notes[role])
        }
    }

    public func setAgent(_ role: SettingsRole, _ value: String) async {
        guard let kind = AgentKind(rawValue: value), let d = settings[role] else { return }
        let (choice, errors) = CatalogRules.changeAgent(AgentChoice(agent: d.agent, model: d.model, effort: d.effort),
                                                        to: kind, catalog: catalog)
        settings[role] = RoleDefault(agent: kind, model: choice.model, effort: choice.effort)
        notes[role] = nil
        incomplete[role] = errors.model
        await save()
    }

    public func setModel(_ role: SettingsRole, _ value: String) async {
        guard let d = settings[role] else { return }
        let (choice, note) = CatalogRules.changeModel(AgentChoice(agent: d.agent, model: d.model, effort: d.effort),
                                                      to: value, catalog: catalog)
        let effort = value == RoleDefault.noAdvisorModel ? "" : choice.effort
        settings[role] = RoleDefault(agent: d.agent, model: value, effort: effort)
        notes[role] = note
        incomplete[role] = nil
        await save()
    }

    public func setEffort(_ role: SettingsRole, _ value: String) async {
        guard let d = settings[role] else { return }
        settings[role] = RoleDefault(agent: d.agent, model: d.model, effort: value)
        notes[role] = nil
        await save()
    }

    /// "Model lists updated 3h ago", using the oldest list among enabled agents. Guarded in practice by
    /// `!catalogStale` (the daemon serves `fetched == 0` as stale), but routed through `ageLine` anyway
    /// so a zero or negative timestamp can never render as an age even if that guard is ever bypassed.
    public var catalogLine: String? {
        let dates = catalog.filter { settings.enabledAgents.contains($0.kind) && $0.installed && !$0.catalogStale }
            .map(\.catalogFetchedAt)
        return dates.min().map { format.ageLine($0, never: Copy.neverFetched, Copy.modelListsUpdated) }
    }

    public var staleNotes: [String] {
        catalog.filter { settings.enabledAgents.contains($0.kind) }.compactMap { CatalogRules.catalogNote($0, format: format) }
    }

    public func refreshModels() async { await checkAgain() }

    // MARK: Notifications tab

    public var levelRows: [LevelRow] {
        let text: [NotificationLevel: (String, String)] = [
            .info: (Copy.levelInfo, Copy.levelInfoCaption),
            .attention: (Copy.levelAttention, Copy.levelAttentionCaption),
            .action: (Copy.levelAction, Copy.levelActionCaption),
        ]
        return NotificationLevel.allCases.map { level in
            let p = settings.pref(level)
            return LevelRow(level: level, label: text[level]!.0, caption: text[level]!.1,
                            center: p.center, sound: p.sound, soundDisabled: !p.center)
        }
    }

    public func setCenter(_ level: NotificationLevel, _ on: Bool) async {
        var p = settings.pref(level)
        p.center = on
        settings.notifications[level.rawValue] = p
        await save()
    }

    public func setSound(_ level: NotificationLevel, _ on: Bool) async {
        var p = settings.pref(level)
        guard p.center else { return }
        p.sound = on
        settings.notifications[level.rawValue] = p
        await save()
    }

    // MARK: Limits tab

    public func value(_ limit: Limit) -> Int {
        switch limit {
        case .agents: return settings.maxConcurrentAgents
        case .subagents: return settings.maxConcurrentSubagents
        case .pauseDeadline: return settings.pauseDeadlineSec
        }
    }

    private func store(_ limit: Limit, _ v: Int) {
        switch limit {
        case .agents: settings.maxConcurrentAgents = v
        case .subagents: settings.maxConcurrentSubagents = v
        case .pauseDeadline: settings.pauseDeadlineSec = v
        }
    }

    /// How many running agents a lower limit leaves above it.
    public func overLimit(_ limit: Limit, _ value: Int) -> Int {
        switch limit {
        case .agents:
            let running = AgentTree.flatten(agents).filter {
                !AgentTree.isFinished($0) &&
                [.spawning, .running, .waiting, .stale, .pauseRequested, .quiescing, .stopping].contains(DisplayState($0))
            }
            return running.count - value
        case .subagents:
            let subagents = AgentTree.flatten(agents).filter {
                $0.parentName != nil && !AgentTree.isFinished($0) &&
                [.spawning, .running, .waiting, .stale, .pauseRequested, .quiescing, .stopping].contains(DisplayState($0))
            }
            return subagents.count - value
        case .pauseDeadline:
            return 0
        }
    }

    public func setLimit(_ limit: Limit, _ raw: Int) async {
        let v = min(limit.range.upperBound, max(limit.range.lowerBound, raw))
        guard v != value(limit) else { return }
        if overLimit(limit, v) > 0 {
            pendingLimit = (limit, v)
            return
        }
        pendingLimit = nil
        store(limit, v)
        await save()
    }

    /// "3 agents are running above the new limit. They keep running; new agents wait for a free slot."
    public var limitNotice: String? {
        pendingLimit.map { Copy.lowerLimit(overLimit($0.limit, $0.value)) }
    }

    public func applyLimit() async {
        guard let p = pendingLimit else { return }
        pendingLimit = nil
        store(p.limit, p.value)
        await save()
    }

    public var visibleExcludes: [String] { settings.scanExcludes.filter { !Self.alwaysSkipped.contains($0) } }

    public func addExclude(_ path: String) async {
        let short = path == home ? "~" : path.hasPrefix(home + "/") ? "~" + path.dropFirst(home.count) : path
        guard !settings.scanExcludes.contains(short) else { return }
        settings.scanExcludes.append(short)
        await save()
    }

    public func removeExclude(_ path: String) async {
        settings.scanExcludes.removeAll { $0 == path }
        await save()
    }

    /// "Last scan: 2h ago · 112 repositories", or "Never scanned" before the daemon's first scan
    /// (`scannedAt` is 0 then) — the wire is a trust boundary, so anything at or below 0 is never.
    public var scanLine: String {
        repos.scanning ? Copy.scanning : format.ageLine(repos.scannedAt, never: Copy.neverScanned) {
            Copy.lastScan($0, repos.all.count)
        }
    }

    public func rescanNow() async {
        repos.scanning = true
        _ = try? await client.rescanRepos()
        if let r = try? await client.repos(query: "") { repos = r }
    }

    public func setCompact(_ on: Bool) async {
        compact = on
        onCompactChange(on)
        if settings.menubarCompact != on {
            settings.menubarCompact = on
            await save()
        }
    }

    // MARK: Instructions tab

    public var instructions: String { settings.instructions }

    public func setInstructions(_ text: String) async {
        guard settings.instructions != text else { return }
        settings.instructions = text
        await save()
    }
}
