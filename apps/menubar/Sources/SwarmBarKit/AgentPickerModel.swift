import Foundation
import Observation

/// The Agent / Model / Effort / Advisor picks shared by the New orchestrator and
/// Orchestrate task windows. Moved out of `NewOrchestratorForm`.
@MainActor
@Observable
public final class AgentPickerModel {
    public private(set) var choice: AgentChoice
    public private(set) var advisor: AdvisorChoice
    public private(set) var advisorEffort: String
    public private(set) var catalog: [AgentCatalogEntry] = []
    public private(set) var agentChangeErrors = FieldErrors()
    public private(set) var effortNote: String?
    public let settings: Settings

    public init(settings: Settings) {
        self.settings = settings
        (choice, advisor) = CatalogRules.prefill(settings)
        advisorEffort = settings[.advisor]?.effort ?? ""
    }

    /// The catalog arrived: normalise the Settings prefill against it.
    public func apply(catalog: [AgentCatalogEntry]) {
        self.catalog = catalog
        advisor = CatalogRules.normalizedAdvisor(advisor, settings: settings, catalog: catalog)
        normalizeAdvisorEffort()
        // The catalog wasn't loaded yet when Settings prefilled `choice`: re-check the stored effort
        // against it now, so a level the model no longer offers can't survive into the picker.
        choice.effort = CatalogRules.normalizeEffort(choice.agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model), choice.effort)
    }

    public var errors: FieldErrors {
        var e = CatalogRules.validate(choice, advisor: advisor, catalog: catalog, enabled: settings.enabledAgents, role: .orchestrator)
        if e.model == nil { e.model = agentChangeErrors.model }
        return e
    }

    public var agentOptions: [PickerOption] { CatalogRules.agentOptions(enabled: settings.enabledAgents) }
    public var modelOptions: [PickerOption] { CatalogRules.modelOptions(CatalogRules.entry(catalog, choice.agent)) }
    /// nil hides the Effort row.
    public var effortOptions: [PickerOption]? {
        CatalogRules.effortOptions(choice.agent, CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model))
    }
    public var advisorAgentOptions: [PickerOption] { CatalogRules.advisorAgentOptions(enabled: settings.enabledAgents) }
    public var advisorModelOptions: [PickerOption] {
        guard case let .pair(agent, _) = advisor else { return [] }
        return CatalogRules.advisorModelOptions(agent, catalog: catalog)
    }
    /// Native Claude advisor pairing uses Claude's own advisor session and has no separate effort.
    public var advisorEffortOptions: [PickerOption]? {
        guard case let .pair(agent, model) = advisor,
              !(choice.agent == .claude && agent == .claude &&
                CatalogRules.resolve(CatalogRules.entry(catalog, agent), model)?.advisorCapable == true) else { return nil }
        return CatalogRules.effortOptions(agent, CatalogRules.resolve(CatalogRules.entry(catalog, agent), model))
    }

    // MARK: edits

    public func setAgent(_ value: String) {
        guard let kind = AgentKind(rawValue: value) else { return }
        (choice, agentChangeErrors) = CatalogRules.changeAgent(choice, to: kind, catalog: catalog)
        effortNote = nil
        normalizeAdvisorEffort()
    }

    public func setModel(_ value: String) {
        let result = CatalogRules.changeModel(choice, to: value, catalog: catalog)
        choice = result.0
        effortNote = result.note
        agentChangeErrors = FieldErrors()
    }

    public func setEffort(_ value: String) {
        choice.effort = CatalogRules.normalizeEffort(choice.agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model), value)
        effortNote = nil
    }

    public func setAdvisorEffort(_ value: String) {
        guard case let .pair(agent, model) = advisor else { return }
        advisorEffort = CatalogRules.normalizeEffort(agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, agent), model), value)
    }

    public func setAdvisorAgent(_ value: String) {
        guard let agent = AgentKind(rawValue: value), settings.enabledAgents.contains(agent) else {
            advisor = .none
            advisorEffort = ""
            return
        }
        let options = CatalogRules.advisorModelOptions(agent, catalog: catalog)
        guard let first = options.first else { advisor = .none; advisorEffort = ""; return }
        let previousAgent: AgentKind? = if case let .pair(kind, _) = advisor { kind } else { nil }
        let current: String? = if case let .pair(kind, model) = advisor, kind == agent { model } else { nil }
        let saved = settings[.advisor]?.agent == agent ? settings[.advisor]?.model : nil
        let model = [current, saved].compactMap { $0 }.first(where: { candidate in options.contains { $0.value == candidate } }) ?? first.value
        advisor = .pair(agent, model)
        if previousAgent != agent { advisorEffort = settings[.advisor]?.agent == agent ? settings[.advisor]?.effort ?? "" : "" }
        normalizeAdvisorEffort()
    }

    public func setAdvisorModel(_ value: String) {
        guard case let .pair(agent, _) = advisor,
              advisorModelOptions.contains(where: { $0.value == value }) else { return }
        advisor = .pair(agent, value)
        normalizeAdvisorEffort()
    }

    private func normalizeAdvisorEffort() {
        guard case let .pair(agent, model) = advisor else { advisorEffort = ""; return }
        advisorEffort = CatalogRules.normalizeEffort(agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, agent), model), advisorEffort)
    }

    // MARK: payloads

    /// The primary effort normalised against the catalog; "" → nil.
    public var effortPayload: String? {
        guard let agent = choice.agent else { return nil }
        let e = CatalogRules.normalizeEffort(agent, CatalogRules.resolve(CatalogRules.entry(catalog, agent), choice.model), choice.effort)
        return e.isEmpty ? nil : e
    }

    public var advisorPayload: AdvisorPayload {
        guard case let .pair(agent, model) = CatalogRules.normalizedAdvisor(advisor, settings: settings, catalog: catalog) else { return .none }
        let normalized = CatalogRules.normalizeEffort(agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, agent), model), advisorEffort)
        return .pair(agent: agent, model: model, effort: advisorEffortOptions == nil || normalized.isEmpty ? nil : normalized)
    }
}
