import Foundation
import XCTest
@testable import SwarmBarKit

@MainActor
final class SettingsModelTests: XCTestCase {
    var client: MockDaemonClient!
    var state = StateResponse()
    var compactChanges: [Bool] = []

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        state = try Fixture.decode("state.json")
    }

    private func model(connected: Bool = true) async -> SettingsModel {
        let m = SettingsModel(client: client, settings: state.settings, agents: state.agents, connected: connected,
                              compact: false, format: Format(now: fixtureNow), home: "/Users/alex",
                              onCompactChange: { [unowned self] in self.compactChanges.append($0) })
        await m.load()
        return m
    }

    private var saves: Int { client.calls.filter { $0 == "settings" }.count }

    func testAgentsTabRows() async {
        let m = await model()
        let rows = m.agentRows
        XCTAssertEqual(rows.map(\.label), ["Claude", "Codex", "Antigravity", "Cursor"])
        XCTAssertEqual(rows.map(\.checked), [true, true, true, false])
        XCTAssertEqual(rows.map(\.checkboxDisabled), [false, false, false, true])
        XCTAssertEqual(rows.map(\.status), [
            "Installed · 2.1.274 · Signed in", "Installed · 0.154.0 · Signed in", "Installed · 1.2.5", "Not installed on this Mac",
        ])
        XCTAssertEqual(rows.map(\.signInNote), [nil, nil, "Not signed in. Run `agy` in a terminal.", nil])
        XCTAssertEqual(rows.map(\.superpowersNote), [nil, nil, "Superpowers missing — orchestrators unavailable", nil])
    }

    func testDisablingAnAgentAsksFirstAndTheLastOneStays() async {
        let m = await model()
        await m.setEnabled(.codex, false)
        XCTAssertEqual(m.disableNotice, "Defaults that use Codex will switch to Claude. Running agents are not affected.")
        XCTAssertEqual(saves, 0)
        m.cancelDisable()
        XCTAssertNil(m.disableNotice)
        await m.setEnabled(.claude, false)
        XCTAssertEqual(m.disableNotice, "Defaults that use Claude will switch to Codex. Running agents are not affected.")
        await m.applyDisable()
        XCTAssertEqual(m.settings.enabledAgents, [.codex, .agy])
        XCTAssertEqual(saves, 1)
        await m.applyDisable()
        XCTAssertEqual(saves, 1)

        await m.setEnabled(.claude, true)
        XCTAssertEqual(m.settings.enabledAgents, [.claude, .codex, .agy], "settings order is kept")
        await m.setEnabled(.claude, true)
        XCTAssertEqual(saves, 2)

        await m.setEnabled(.codex, false)
        await m.applyDisable()
        await m.setEnabled(.agy, false)
        await m.applyDisable()
        await m.setEnabled(.claude, false)
        XCTAssertEqual(m.agentsError, "At least one agent must stay enabled.")
        XCTAssertNil(m.pendingDisable)
        await m.checkAgain()
        XCTAssertEqual(client.calls.last, "catalog-refresh")
    }

    func testDefaultsGrid() async {
        let m = await model()
        let rows = m.defaultsRows
        XCTAssertEqual(rows.map(\.label), ["Orchestrator", "Advisor", "Coding", "Code review", "UI review", "Research", "Debugging", "Mechanical", "Fallback"])
        XCTAssertEqual(rows[0].agentOptions.map(\.label), ["Claude", "Codex", "Antigravity"])
        XCTAssertEqual(rows[0].modelOptions.map(\.label).prefix(4),
                       ["Fable 5.1 (latest)", "Opus 5 (latest)", "Sonnet 5 (latest)", "Haiku 4.5 (latest)"])
        XCTAssertEqual(rows[0].effortOptions?.first?.label, "Default (high)")
        XCTAssertEqual(rows[1].modelOptions.map(\.label), [
            "Fable 5.1 (latest)", "Opus 5 (latest)", "Sonnet 5 (latest)", "Fable 5.1", "Opus 5", "Sonnet 5", "Sonnet 4.6", "No advisor",
        ])
        XCTAssertEqual(rows[3].agent, "codex")
        XCTAssertEqual(rows[3].effort, "high")
        XCTAssertEqual(rows[3].effortOptions?.first?.label, "Default (medium)")
        XCTAssertFalse(rows[3].modelOptions.contains { $0.value == "gpt-legacy" }, "hidden models stay out")
        XCTAssertNil(rows[7].effortOptions, "Haiku shows Not supported")
        // docs/specs/2026-09-19-usage-fallback-agent.md: the Fallback row is
        // a plain default like any other -- no "no advisor" option, no
        // advisor-only model filtering.
        XCTAssertEqual(rows[8].agent, "claude")
        XCTAssertEqual(rows[8].model, "sonnet")
        XCTAssertFalse(rows[8].modelOptions.contains { $0.value == "none" })
        XCTAssertEqual(m.catalogLine, "Model lists updated 3h ago")
        XCTAssertEqual(m.staleNotes, ["Model list from 1d ago. Couldn't refresh: agy models timed out"])
        await m.refreshModels()
        XCTAssertEqual(client.calls.last, "catalog-refresh")
    }

    func testDefaultsEditsSaveAndBlockOnErrors() async {
        let m = await model()
        await m.setModel(.coder, "claude-opus-5")
        await m.setEffort(.coder, "xhigh")
        await m.setModel(.coder, "claude-sonnet-4-6")
        XCTAssertEqual(m.defaultsRows[2].note, "xhigh isn't available for Sonnet 4.6; using the default.")
        XCTAssertEqual(m.settings[.coder], RoleDefault(agent: .claude, model: "claude-sonnet-4-6"))
        XCTAssertEqual(saves, 3)

        await m.setAgent(.coder, "codex")
        XCTAssertNil(m.defaultsRows[2].error, "an incompatible switch substitutes the first model instead of an error")
        XCTAssertEqual(m.settings[.coder]?.model, "gpt-6-astra")
        XCTAssertEqual(saves, 4)
        await m.setAgent(.coder, "robot")
        XCTAssertEqual(saves, 4)

        await m.setModel(.advisor, "none")
        XCTAssertNil(m.defaultsRows[1].effortOptions)
        XCTAssertEqual(m.settings[.advisor], RoleDefault(agent: .claude, model: "none"))

        var gone = state.settings
        gone[.mechanical] = RoleDefault(agent: .claude, model: "claude-haiku-3")
        state.settings = gone
        let g = await model()
        XCTAssertEqual(g.defaultsRows[7].error, "claude-haiku-3 is no longer offered by Claude.")
        let before = saves
        await g.setEffort(.orchestrator, "max")
        XCTAssertEqual(saves, before, "a missing model blocks saving until it's changed")
        await g.setModel(.mechanical, "haiku")
        XCTAssertEqual(saves, before + 1)
    }

    /// docs/specs/2026-09-19-usage-fallback-agent.md: the Fallback row goes
    /// through the exact same setAgent/setModel path as a real role, reading
    /// and writing Settings.fallbackDefault (not the roles dictionary) via
    /// the `.fallback` subscript case.
    func testFallbackRowRoundTrips() async {
        let m = await model()
        XCTAssertEqual(m.settings[.fallback], RoleDefault(agent: .claude, model: "sonnet"))
        await m.setAgent(.fallback, "codex")
        await m.setModel(.fallback, "gpt-6-astra")
        XCTAssertEqual(m.settings.fallbackDefault, RoleDefault(agent: .codex, model: "gpt-6-astra"))
        XCTAssertEqual(m.settings[.fallback], RoleDefault(agent: .codex, model: "gpt-6-astra"))
        XCTAssertNil(m.settings.roles["fallback"], "the fallback default is never stored in the roles dictionary")
    }

    /// A stored effort the catalog no longer offers must be normalised at the source, not just for
    /// display: `settings[role]` itself must never carry it, or a save PUTs the stale level and
    /// `setAgent` can resurrect it from the still-stale stored value.
    func testStoredEffortIsNormalisedAtTheSourceNotJustForDisplay() async {
        var stale = state.settings
        stale[.mechanical] = RoleDefault(agent: .claude, model: "haiku", effort: "high") // Haiku has no efforts
        state.settings = stale
        let m = await model()
        XCTAssertEqual(m.defaultsRows[7].effort, "", "the row never shows a level the model doesn't offer")
        XCTAssertEqual(m.settings[.mechanical]?.effort, "", "normalised at the source, not just for display")

        // An unrelated save round-trips the whole `Settings` object through the mock's echo, which is
        // the PUT payload: the stale level must not ride along on it.
        await m.setCenter(.info, false)
        XCTAssertEqual(m.settings[.mechanical]?.effort, "", "the PUT payload must not resurrect the stale level")

        // Changing the agent rebuilds from the stored value (`d.effort`); with the source already
        // clean this can no longer resurrect the stale level either.
        await m.setAgent(.mechanical, "codex")
        await m.setAgent(.mechanical, "claude")
        XCTAssertNotEqual(m.settings[.mechanical]?.effort, "high")
    }

    /// A missing or empty-`models` catalog entry (a failed `/api/catalog`, or an agent that isn't
    /// installed or was never probed — `internal/catalog/service.go:101` serves exactly that) means
    /// there is nothing to normalise against, not that every level is invalid: the stored effort must
    /// survive untouched, including through a save, or an outage silently wipes Settings.
    func testNormalizeStoredEffortsSkipsARoleWithNoUsableCatalogEntry() async {
        var stale = state.settings
        stale[.mechanical] = RoleDefault(agent: .agy, model: "whatever", effort: "high")
        state.settings = stale

        // No entry at all for `.agy`.
        client.catalogEntries = []
        let missing = await model()
        XCTAssertEqual(missing.settings[.mechanical]?.effort, "high", "no catalog entry: nothing to normalise against")
        await missing.setCenter(.info, false)
        XCTAssertEqual(missing.settings[.mechanical]?.effort, "high", "an unrelated save must not PUT the wipe")

        // An entry that exists but has no models yet.
        client.catalogEntries = [AgentCatalogEntry(kind: .agy, models: [])]
        let empty = await model()
        XCTAssertEqual(empty.settings[.mechanical]?.effort, "high", "an entry with no models yet must not wipe the stored effort either")
        await empty.setCenter(.info, false)
        XCTAssertEqual(empty.settings[.mechanical]?.effort, "high")

        // A real catalog (Haiku, no efforts) must still normalise, exactly as before this fix.
        client.catalogEntries = try! MockDaemonClient(fixtures: Fixture.dir).catalogEntries
        var validButNoEfforts = state.settings
        validButNoEfforts[.mechanical] = RoleDefault(agent: .claude, model: "haiku", effort: "high")
        state.settings = validButNoEfforts
        let real = await model()
        XCTAssertEqual(real.settings[.mechanical]?.effort, "", "a real catalog entry still normalises a level the model doesn't offer")
    }

    /// `ReposResponse()` defaults `scannedAt` to 0 (never scanned), and the wire is a trust boundary
    /// (anything <= 0), so this must read "Never scanned", not an age from 1970.
    func testScanLineNeverRendersTheEpochOrANegativeTimestampAsAnAge() async {
        client.reposResponse.scannedAt = Timestamp(ms: 0)
        let neverScanned = await model()
        XCTAssertEqual(neverScanned.scanLine, "Never scanned")

        client.reposResponse.scannedAt = Timestamp(ms: -1)
        let negative = await model()
        XCTAssertEqual(negative.scanLine, "Never scanned", "the wire is a trust boundary: anything <= 0 is never")
    }

    /// `catalogLine` is guarded in practice by the `!catalogStale` filter (the daemon serves
    /// `fetched == 0` as stale), but the guard living in the daemon rather than here means a raw
    /// `ageCompact` call here would still render the epoch as an age if that guard were ever bypassed.
    /// Route it through `ageLine` like every other age render, so it can't.
    func testCatalogLineNeverRendersAnEpochOrNegativeTimestampAsAnAge() async {
        client.catalogEntries = [AgentCatalogEntry(kind: .claude, catalogFetchedAt: Timestamp(ms: 0))]
        let zero = await model()
        XCTAssertEqual(zero.catalogLine, "Never fetched")

        client.catalogEntries = [AgentCatalogEntry(kind: .claude, catalogFetchedAt: Timestamp(ms: -1))]
        let negative = await model()
        XCTAssertEqual(negative.catalogLine, "Never fetched", "the wire is a trust boundary: anything <= 0 is never")
    }

    func testNotificationsTab() async {
        let m = await model()
        XCTAssertEqual(m.levelRows.map(\.label), ["Info", "Attention", "Action required"])
        XCTAssertEqual(m.levelRows.map(\.caption), [
            "Task accepted, task completed, epic ready", "Paused, stopped, failed, crashed, worktree kept", "Questions and approvals",
        ])
        await m.setCenter(.info, false)
        XCTAssertTrue(m.levelRows[0].soundDisabled)
        await m.setSound(.info, true)
        XCTAssertFalse(m.levelRows[0].sound, "sound can't change while Notification Center is off")
        await m.setCenter(.info, true)
        await m.setSound(.info, true)
        XCTAssertEqual(m.settings.pref(.info), NotifyPref(center: true, sound: true))
        XCTAssertEqual(saves, 3)
    }

    func testLimitsTab() async {
        let m = await model()
        XCTAssertEqual([m.value(.subagents), m.value(.pauseDeadline)], [3, 120])
        // fixture: 2 live subagents login-form-coder, login-review under auth-epic-orchestrator
        XCTAssertEqual(m.overLimit(.subagents, 1), 1)
        XCTAssertEqual(m.overLimit(.pauseDeadline, 30), 0)

        await m.setLimit(.subagents, 1)
        XCTAssertEqual(m.limitNotice, "1 agents are running above the new limit. They keep running; new agents wait for a free slot.")
        XCTAssertEqual(saves, 0)
        await m.applyLimit()
        XCTAssertEqual(m.settings.maxConcurrentSubagents, 1)
        XCTAssertNil(m.limitNotice)
        await m.applyLimit()
        XCTAssertEqual(saves, 1)

        await m.setLimit(.subagents, 99)
        XCTAssertEqual(m.settings.maxConcurrentSubagents, 16)
        await m.setLimit(.pauseDeadline, 5)
        XCTAssertEqual(m.settings.pauseDeadlineSec, 30)
        await m.setLimit(.subagents, 16)
        XCTAssertEqual(saves, 3)
        XCTAssertEqual([SettingsModel.Limit.subagents, .pauseDeadline].map(\.range),
                       [1...16, 30...600])
    }

    func testDiscoveryAndCompact() async {
        let m = await model()
        XCTAssertEqual(m.visibleExcludes, ["~/Downloads"])
        await m.addExclude("/Users/alex/Movies")
        await m.addExclude("/Volumes/Backup")
        await m.addExclude("/Users/alex/Movies")
        await m.addExclude("/Users/alex")
        XCTAssertEqual(m.visibleExcludes, ["~/Downloads", "~/Movies", "/Volumes/Backup", "~"])
        await m.removeExclude("~/Downloads")
        XCTAssertEqual(m.settings.scanExcludes, ["~/Library", "~/.Trash", "~/Movies", "/Volumes/Backup", "~"])
        XCTAssertEqual(m.scanLine, "Last scan: 2h ago · 5 repositories")
        await m.rescanNow()
        XCTAssertEqual(client.calls.suffix(2), ["rescan", "repos "])

        await m.setCompact(true)
        await m.setCompact(true)
        XCTAssertEqual(compactChanges, [true, true])
        XCTAssertTrue(m.settings.menubarCompact)
        XCTAssertEqual(saves, 5)
    }

    /// docs/specs/2026-09-22-isolated-mcp-and-custom-instructions.md: `instructions` decodes from
    /// the wire (present or, for daemons that predate it, absent) and `setInstructions` PUTs the
    /// updated settings like every other single-field setter.
    func testInstructionsDecodesAndSaves() async throws {
        let withInstructions = try SwarmJSON.decode(Settings.self, from: Data("""
        {"enabled_agents":["claude"],"roles":{},"notifications":{},"max_orchestrators":3,
         "max_agents":8,"max_agents_per_root":4,"scan_excludes":[],"scan_interval_sec":21600,
         "menubar_compact":false,"usage_poll_sec":300,"pause_deadline_sec":120,
         "instructions":"# Team rules\\nStandard library first."}
        """.utf8))
        XCTAssertEqual(withInstructions.instructions, "# Team rules\nStandard library first.")

        let withoutInstructions: Settings = try Fixture.decode("settings.json")
        XCTAssertEqual(withoutInstructions.instructions, "", "daemons that predate the field decode to empty")

        let m = await model()
        XCTAssertEqual(m.instructions, "")
        await m.setInstructions("# Project Guidelines\n- Go 1.24 + standard library first")
        XCTAssertEqual(m.instructions, "# Project Guidelines\n- Go 1.24 + standard library first")
        XCTAssertEqual(saves, 1)

        await m.setInstructions("# Project Guidelines\n- Go 1.24 + standard library first")
        XCTAssertEqual(saves, 1, "an unchanged value doesn't PUT again")

        // The mock echoes back whatever was PUT, so the round trip through `save()` also exercises
        // `Settings.encode(to:)` carrying `instructions` over the wire.
        XCTAssertEqual(String(decoding: try SwarmJSON.encode(m.settings), as: UTF8.self).contains("\"instructions\""), true)
    }

    func testSavingWhileDownOrFailing() async {
        let down = await model(connected: false)
        await down.setCenter(.info, false)
        XCTAssertEqual(down.saveError, "Can't save while the daemon is unavailable.")
        XCTAssertEqual(saves, 0)

        let m = await model()
        client.failNext = .unreachable
        await m.setCenter(.info, false)
        XCTAssertEqual(m.saveError, "Couldn't save settings.")
        await m.save()
        XCTAssertNil(m.saveError)
        m.connected = false
        await m.save()
        XCTAssertEqual(m.saveError, "Can't save while the daemon is unavailable.")
    }
}
