import Foundation

public struct PickerOption: Equatable, Sendable, Hashable, Identifiable {
    public var value: String
    public var label: String
    public var id: String { value }
    public init(_ value: String, _ label: String) { self.value = value; self.label = label }
}

/// Agent / Model / Effort for one role or one spawn. `effort == ""` is the agent default (L27).
public struct AgentChoice: Equatable, Sendable {
    public var agent: AgentKind?
    public var model: String
    public var effort: String
    public init(agent: AgentKind?, model: String, effort: String = "") {
        self.agent = agent; self.model = model; self.effort = effort
    }
}

public enum AdvisorChoice: Equatable, Sendable, Hashable {
    case none
    case pair(AgentKind, String)

    /// "claude:fable" or "none"; the value of the Advisor menu.
    public var encoded: String {
        switch self {
        case .none: return "none"
        case let .pair(agent, model): return "\(agent.rawValue):\(model)"
        }
    }

    public init(encoded: String) {
        let parts = encoded.split(separator: ":", maxSplits: 1).map(String.init)
        if parts.count == 2, let kind = AgentKind(rawValue: parts[0]) {
            self = .pair(kind, parts[1])
        } else {
            self = .none
        }
    }
}

public struct FieldErrors: Equatable, Sendable {
    public var agent: String?
    public var model: String?
    public var advisor: String?
    public init(agent: String? = nil, model: String? = nil, advisor: String? = nil) {
        self.agent = agent; self.model = model; self.advisor = advisor
    }
    public var isValid: Bool { agent == nil && model == nil && advisor == nil }
}

/// Model, effort and advisor rules shared by New orchestrator and Settings (§16.3, §16.4, L26–L28).
/// The same rules as the board's `web/src/logic/catalog.ts`. Model ids only ever come from the catalog.
public enum CatalogRules {
    public static func entry(_ catalog: [AgentCatalogEntry], _ kind: AgentKind?) -> AgentCatalogEntry? {
        catalog.first { $0.kind == kind }
    }

    public static func resolve(_ entry: AgentCatalogEntry?, _ value: String) -> CatalogModel? {
        guard let entry, !value.isEmpty else { return nil }
        return entry.models.first { $0.id == value } ?? entry.models.first { $0.aliases.contains(value) }
    }

    private static func aliasLabel(_ alias: String) -> String { alias.prefix(1).uppercased() + alias.dropFirst() + " (latest)" }

    public static func modelLabel(_ entry: AgentCatalogEntry?, _ value: String) -> String {
        guard let m = resolve(entry, value) else { return value }
        return m.id == value ? m.label : aliasLabel(value)
    }

    public static func agentOptions(enabled: [AgentKind]) -> [PickerOption] {
        AgentKind.selectable.filter(enabled.contains).map { PickerOption($0.rawValue, Copy.agentLabel($0)) }
    }

    /// Aliases first ("Opus (latest)"), then full names; hidden models are left out. One option per launch
    /// id: the served alias fallback has `id == aliases[0]` (`catalog.ClaudeAliasFallback`), and a picker
    /// needs unique ids, so the first option for a value wins.
    public static func modelOptions(_ entry: AgentCatalogEntry?, advisorOnly: Bool = false) -> [PickerOption] {
        guard let entry else { return [] }
        let visible = entry.models.filter { !$0.hidden && (!advisorOnly || $0.advisorCapable) }
        var seen = Set<String>()
        return (visible.flatMap { m in m.aliases.map { PickerOption($0, aliasLabel($0)) } }
            + visible.map { PickerOption($0.id, $0.label) }).filter { seen.insert($0.value).inserted }
    }

    /// The level a bare slug stands for beside its suffixed siblings (cursor only, `catalog.DefaultLevel`).
    /// It is the agent's own choice, so it reads "Default (<Agent>)" rather than the bare word.
    private static let bareLevel = "default"

    public static func defaultEffortLabel(_ kind: AgentKind, _ model: CatalogModel) -> String {
        if model.defaultEffort == bareLevel { return Copy.defaultLevel(Copy.agentLabel(kind)) }
        if !model.defaultEffort.isEmpty { return Copy.defaultLevel(model.defaultEffort) }
        if kind == .claude { return model.efforts.contains("high") ? Copy.defaultLevel("high") : Copy.defaultClaudeCode }
        return Copy.defaultLevel(model.efforts.contains("high") ? "high" : model.efforts.last ?? "")
    }

    /// nil means the model has no effort control: hidden in New orchestrator, "Not supported" in Settings.
    /// When the bare level *is* the default effort it resolves to the same launch id as `""`, so it gets
    /// one row, not two (`catalog.CatalogModel.LaunchModel`).
    public static func effortOptions(_ kind: AgentKind?, _ model: CatalogModel?) -> [PickerOption]? {
        guard let kind, let model, !model.efforts.isEmpty else { return nil }
        let levels = model.defaultEffort == bareLevel ? model.efforts.filter { $0 != bareLevel } : model.efforts
        return [PickerOption("", defaultEffortLabel(kind, model))]
            + levels.map { PickerOption($0, $0 == bareLevel ? Copy.defaultLevel(Copy.agentLabel(kind)) : $0) }
    }

    /// A stored level the model's menu doesn't offer means the agent default (L27): an effort the model
    /// dropped, or a bare level that merged into the "" row.
    public static func normalizeEffort(_ kind: AgentKind?, _ model: CatalogModel?, _ effort: String) -> String {
        effortOptions(kind, model)?.contains { $0.value == effort } == true ? effort : ""
    }

    public static func prefill(_ settings: Settings, role: SettingsRole = .orchestrator) -> (AgentChoice, AdvisorChoice) {
        let choice = settings[role].map { AgentChoice(agent: $0.agent, model: $0.model, effort: $0.effort) }
            ?? AgentChoice(agent: settings.enabledAgents.first, model: "")
        guard let a = settings[.advisor], a.model != RoleDefault.noAdvisorModel else { return (choice, .none) }
        return (choice, .pair(a.agent, a.model))
    }

    /// Changing Agent keeps the model only if the new agent offers it. Nothing is substituted (§16.3).
    public static func changeAgent(_ choice: AgentChoice, to agent: AgentKind, catalog: [AgentCatalogEntry]) -> (AgentChoice, FieldErrors) {
        if let m = resolve(entry(catalog, agent), choice.model) {
            return (AgentChoice(agent: agent, model: choice.model, effort: normalizeEffort(agent, m, choice.effort)),
                    FieldErrors())
        }
        return (AgentChoice(agent: agent, model: ""), FieldErrors(model: Copy.modelUnavailable))
    }

    /// Changing Model keeps a supported level, otherwise resets to Default with a note (§16.4).
    public static func changeModel(_ choice: AgentChoice, to model: String, catalog: [AgentCatalogEntry]) -> (AgentChoice, note: String?) {
        let m = resolve(entry(catalog, choice.agent), model)
        var next = choice
        next.model = model
        next.effort = normalizeEffort(choice.agent, m, choice.effort)
        // A bare level that merged into the "" row launches the same way, so there is nothing to tell the user.
        if next.effort.isEmpty, !choice.effort.isEmpty, choice.effort != bareLevel {
            return (next, m.map { Copy.effortUnavailable(choice.effort, $0.label) })
        }
        return (next, nil)
    }

    public static func validate(_ choice: AgentChoice, advisor: AdvisorChoice, catalog: [AgentCatalogEntry],
                                enabled: [AgentKind], role: SettingsRole) -> FieldErrors {
        guard let agent = choice.agent, enabled.contains(agent) else { return FieldErrors(agent: Copy.chooseAgent) }
        var e = FieldErrors()
        let found = entry(catalog, agent)
        let name = Copy.agentLabel(agent)
        if !(found?.installed ?? false) {
            e.agent = Copy.agentNotInstalled(name)
        } else if !(found?.authOk ?? false) {
            e.agent = Copy.agentNotSignedIn(name, Copy.loginCommand(agent))
        } else if role == .orchestrator && !(found?.superpowers ?? false) {
            e.agent = Copy.superpowersMissing(name)
        }
        if choice.model.isEmpty {
            e.model = Copy.modelUnavailable
        } else if resolve(found, choice.model) == nil {
            e.model = Copy.modelGone(choice.model, name)
        }
        if case let .pair(kind, model) = advisor, resolve(entry(catalog, kind), model) == nil {
            e.advisor = Copy.modelGone(model, Copy.agentLabel(kind))
        }
        return e
    }

    /// Settings grid check: a saved model the catalog no longer lists (skipped while the list is empty, like the daemon).
    public static func goneModel(_ role: RoleDefault, catalog: [AgentCatalogEntry]) -> String? {
        guard role.model != RoleDefault.noAdvisorModel, let e = entry(catalog, role.agent), !e.models.isEmpty,
              resolve(e, role.model) == nil else { return nil }
        return Copy.modelGone(role.model, Copy.agentLabel(role.agent))
    }

    public static func catalogNote(_ entry: AgentCatalogEntry?, format: Format) -> String? {
        guard let entry, entry.catalogStale else { return nil }
        return Copy.catalogStale(format.ageCompact(entry.catalogFetchedAt.date), entry.catalogError)
    }

    /// One menu of "Agent · Model" pairs plus "No advisor" (§16.3). Claude lists only models that can advise.
    public static func advisorOptions(_ catalog: [AgentCatalogEntry], enabled: [AgentKind]) -> [PickerOption] {
        AgentKind.selectable.filter(enabled.contains).flatMap { kind in
            modelOptions(entry(catalog, kind), advisorOnly: kind == .claude).map {
                PickerOption(AdvisorChoice.pair(kind, $0.value).encoded, "\(Copy.agentLabel(kind)) · \($0.label)")
            }
        } + [PickerOption("none", Copy.noAdvisor)]
    }

    /// The spike body's advisor; its effort comes from the Settings advisor row (§16.3), re-checked against
    /// the model the user actually picked. An unsupported level is dropped, which means that model's own
    /// default effort (§6.7): the daemon resolves a missing `effort` from the catalog.
    public static func advisorPayload(_ advisor: AdvisorChoice, settings: Settings,
                                      catalog: [AgentCatalogEntry]) -> AdvisorPayload {
        guard case let .pair(agent, model) = advisor else { return .none }
        let stored = settings[.advisor]?.effort ?? ""
        let effort = normalizeEffort(agent, resolve(entry(catalog, agent), model), stored)
        return .pair(agent: agent, model: model, effort: effort.isEmpty ? nil : effort)
    }
}
