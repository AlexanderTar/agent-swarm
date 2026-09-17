import Foundation
import XCTest
@testable import SwarmBarKit

final class CatalogRulesTests: XCTestCase {
    typealias R = CatalogRules
    let catalog: [AgentCatalogEntry] = [
        AgentCatalogEntry(kind: .claude, models: [
            CatalogModel(id: "m-opus", label: "Opus 5", aliases: ["opus"], efforts: ["low", "medium", "high", "xhigh", "max"], advisorCapable: true),
            CatalogModel(id: "m-sonnet46", label: "Sonnet 4.6", efforts: ["low", "medium", "high"], advisorCapable: true),
            CatalogModel(id: "m-haiku", label: "Haiku 4.5", aliases: ["haiku"]),
            CatalogModel(id: "m-hidden", label: "Hidden", hidden: true),
        ]),
        AgentCatalogEntry(kind: .codex, models: [
            CatalogModel(id: "gpt-x", label: "GPT X", efforts: ["low", "medium", "high"], defaultEffort: "medium"),
        ]),
    ]
    var settings: Settings {
        var s = Settings.defaults
        s.enabledAgents = [.claude, .codex]
        s.roles = ["orchestrator": RoleDefault(agent: .claude, model: "opus"),
                   "advisor": RoleDefault(agent: .claude, model: "m-sonnet46", effort: "high")]
        return s
    }

    func testAliasesComeFirstAndHiddenModelsAreLeftOut() {
        XCTAssertEqual(R.resolve(catalog[0], "opus")?.id, "m-opus")
        XCTAssertNil(R.resolve(catalog[0], ""))
        XCTAssertEqual(R.modelLabel(catalog[0], "opus"), "Opus (latest)")
        XCTAssertEqual(R.modelLabel(catalog[0], "m-haiku"), "Haiku 4.5")
        XCTAssertEqual(R.modelLabel(nil, "zzz"), "zzz")
        XCTAssertEqual(R.modelOptions(catalog[0]).map(\.label), ["Opus (latest)", "Haiku (latest)", "Opus 5", "Sonnet 4.6", "Haiku 4.5"])
        XCTAssertEqual(R.modelOptions(catalog[0], advisorOnly: true).map(\.value), ["opus", "m-opus", "m-sonnet46"])
        XCTAssertEqual(R.modelOptions(nil), [])
        XCTAssertEqual(R.agentOptions(enabled: [.agy, .claude, .fake]), [PickerOption("claude", "Claude"), PickerOption("agy", "agy")])
    }

    func testDefaultEffortLabels() {
        XCTAssertEqual(R.defaultEffortLabel(.claude, CatalogModel(id: "a", efforts: ["low", "high"])), "Default (high)")
        XCTAssertEqual(R.defaultEffortLabel(.claude, CatalogModel(id: "a", efforts: ["low", "medium"])), "Default (Claude Code)")
        XCTAssertEqual(R.defaultEffortLabel(.codex, CatalogModel(id: "a", efforts: ["low", "medium"], defaultEffort: "medium")), "Default (medium)")
        XCTAssertEqual(R.defaultEffortLabel(.agy, CatalogModel(id: "a", efforts: ["low", "high"])), "Default (high)")
        XCTAssertEqual(R.defaultEffortLabel(.cursor, CatalogModel(id: "a", efforts: ["low", "medium"])), "Default (medium)")
    }

    func testEffortOptionsOrNotSupported() {
        XCTAssertEqual(R.effortOptions(.codex, R.resolve(catalog[1], "gpt-x")), [
            PickerOption("", "Default (medium)"), PickerOption("low", "low"), PickerOption("medium", "medium"), PickerOption("high", "high"),
        ])
        XCTAssertNil(R.effortOptions(.claude, R.resolve(catalog[0], "haiku")))
        XCTAssertNil(R.effortOptions(.claude, nil))
        XCTAssertNil(R.effortOptions(nil, R.resolve(catalog[1], "gpt-x")))
    }

    func testPrefillFromSettings() {
        let (choice, advisor) = R.prefill(settings)
        XCTAssertEqual(choice, AgentChoice(agent: .claude, model: "opus"))
        XCTAssertEqual(advisor, .pair(.claude, "m-sonnet46"))
        var bare = Settings.defaults
        bare.enabledAgents = [.codex]
        bare.roles = ["advisor": RoleDefault(agent: .claude, model: "none")]
        let (c2, a2) = R.prefill(bare)
        XCTAssertEqual(c2, AgentChoice(agent: .codex, model: ""))
        XCTAssertEqual(a2, AdvisorChoice.none)
        bare.roles = [:]
        XCTAssertEqual(R.prefill(bare, role: .coder).1, AdvisorChoice.none)
    }

    func testChangingAgentNeverSubstitutes() {
        let (c, e) = R.changeAgent(AgentChoice(agent: .claude, model: "opus", effort: "high"), to: .codex, catalog: catalog)
        XCTAssertEqual(c, AgentChoice(agent: .codex, model: ""))
        XCTAssertEqual(e, FieldErrors(model: "Choose a model available for this agent."))
        let (kept, ok) = R.changeAgent(AgentChoice(agent: .codex, model: "m-opus", effort: "max"), to: .claude, catalog: catalog)
        XCTAssertEqual(kept, AgentChoice(agent: .claude, model: "m-opus", effort: "max"))
        XCTAssertTrue(ok.isValid)
        XCTAssertEqual(R.changeAgent(AgentChoice(agent: .codex, model: "m-sonnet46", effort: "xhigh"), to: .claude, catalog: catalog).0.effort, "")
    }

    func testChangingModelKeepsOrResetsEffort() {
        let keep = R.changeModel(AgentChoice(agent: .claude, model: "m-opus", effort: "high"), to: "m-sonnet46", catalog: catalog)
        XCTAssertEqual(keep.0, AgentChoice(agent: .claude, model: "m-sonnet46", effort: "high"))
        XCTAssertNil(keep.note)
        let reset = R.changeModel(AgentChoice(agent: .claude, model: "m-opus", effort: "xhigh"), to: "m-sonnet46", catalog: catalog)
        XCTAssertEqual(reset.0.effort, "")
        XCTAssertEqual(reset.note, "xhigh isn't available for Sonnet 4.6; using the default.")
        let none = R.changeModel(AgentChoice(agent: .claude, model: "m-opus"), to: "m-haiku", catalog: catalog)
        XCTAssertEqual(none.0, AgentChoice(agent: .claude, model: "m-haiku"))
        XCTAssertNil(none.note)
        XCTAssertNil(R.changeModel(AgentChoice(agent: .claude, model: "m-opus", effort: "max"), to: "gone", catalog: catalog).note)
    }

    func testValidation() {
        let ok = AgentChoice(agent: .claude, model: "opus")
        XCTAssertEqual(R.validate(ok, advisor: .none, catalog: catalog, enabled: [.claude], role: .orchestrator), FieldErrors())
        XCTAssertEqual(R.validate(AgentChoice(agent: .claude, model: ""), advisor: .none, catalog: catalog, enabled: [.claude], role: .orchestrator),
                       FieldErrors(model: "Choose a model available for this agent."))
        XCTAssertEqual(R.validate(AgentChoice(agent: .claude, model: "gone-1"), advisor: .none, catalog: catalog, enabled: [.claude], role: .orchestrator),
                       FieldErrors(model: "gone-1 is no longer offered by Claude."))
        XCTAssertEqual(R.validate(ok, advisor: .pair(.claude, "gone-2"), catalog: catalog, enabled: [.claude], role: .orchestrator),
                       FieldErrors(advisor: "gone-2 is no longer offered by Claude."))
        var broken = catalog
        broken[0].installed = false
        XCTAssertEqual(R.validate(ok, advisor: .none, catalog: broken, enabled: [.claude], role: .orchestrator),
                       FieldErrors(agent: "Claude isn't installed on this Mac."))
        broken[0].installed = true
        broken[0].authOk = false
        XCTAssertEqual(R.validate(ok, advisor: .none, catalog: broken, enabled: [.claude], role: .orchestrator),
                       FieldErrors(agent: "Claude isn't signed in. Run `claude` in a terminal."))
        broken[0].authOk = true
        broken[0].superpowers = false
        XCTAssertEqual(R.validate(ok, advisor: .none, catalog: broken, enabled: [.claude], role: .orchestrator),
                       FieldErrors(agent: "Install the superpowers plugin for Claude to run orchestrators."))
        XCTAssertTrue(R.validate(ok, advisor: .none, catalog: broken, enabled: [.claude], role: .coder).isValid)
        XCTAssertFalse(R.validate(AgentChoice(agent: nil, model: ""), advisor: .none, catalog: catalog, enabled: [.claude], role: .orchestrator).isValid)
        XCTAssertFalse(R.validate(ok, advisor: .none, catalog: catalog, enabled: [.codex], role: .orchestrator).isValid)
        XCTAssertEqual(R.validate(AgentChoice(agent: .agy, model: "x"), advisor: .none, catalog: catalog, enabled: [.agy], role: .coder).agent,
                       "agy isn't installed on this Mac.")
        XCTAssertEqual(["claude", "codex login", "agy", "cursor-agent login"], AgentKind.selectable.map(Copy.loginCommand))
    }

    func testGoneModelOnTheSettingsGrid() {
        XCTAssertNil(R.goneModel(RoleDefault(agent: .claude, model: "opus"), catalog: catalog))
        XCTAssertEqual(R.goneModel(RoleDefault(agent: .claude, model: "claude-opus-4-1"), catalog: catalog),
                       "claude-opus-4-1 is no longer offered by Claude.")
        XCTAssertNil(R.goneModel(RoleDefault(agent: .claude, model: "none"), catalog: catalog))
        XCTAssertNil(R.goneModel(RoleDefault(agent: .agy, model: "whatever"), catalog: catalog), "no catalog entry")
        XCTAssertNil(R.goneModel(RoleDefault(agent: .cursor, model: "auto"), catalog: [AgentCatalogEntry(kind: .cursor)]), "empty list")
    }

    func testStaleCatalogNote() throws {
        let format = Format(now: fixtureNow, timeZone: TimeZone(identifier: "UTC")!)
        let live: [AgentCatalogEntry] = try Fixture.decode("catalog.json")
        XCTAssertEqual(R.catalogNote(live[2], format: format), "Model list from 1d ago. Couldn't refresh: agy models timed out")
        XCTAssertNil(R.catalogNote(live[0], format: format))
        XCTAssertNil(R.catalogNote(nil, format: format))
    }

    func testAdvisorMenuAndPayload() {
        XCTAssertEqual(R.advisorOptions(catalog, enabled: [.codex, .claude]).map(\.label), [
            "Claude · Opus (latest)", "Claude · Opus 5", "Claude · Sonnet 4.6", "Codex · GPT X", "No advisor",
        ])
        XCTAssertEqual(AdvisorChoice.pair(.claude, "opus").encoded, "claude:opus")
        XCTAssertEqual(AdvisorChoice.none.encoded, "none")
        XCTAssertEqual(AdvisorChoice(encoded: "codex:gpt-x"), .pair(.codex, "gpt-x"))
        XCTAssertEqual(AdvisorChoice(encoded: "claude:a:b"), .pair(.claude, "a:b"))
        XCTAssertEqual(AdvisorChoice(encoded: "none"), AdvisorChoice.none)
        XCTAssertEqual(AdvisorChoice(encoded: "robot:x"), AdvisorChoice.none)
        XCTAssertEqual(R.advisorPayload(.pair(.claude, "opus"), settings: settings), .pair(agent: .claude, model: "opus", effort: "high"))
        XCTAssertEqual(R.advisorPayload(.pair(.claude, "opus"), settings: .defaults), .pair(agent: .claude, model: "opus", effort: nil))
        XCTAssertEqual(R.advisorPayload(.none, settings: settings), AdvisorPayload.none)
    }
}
