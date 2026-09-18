import Foundation
import XCTest
@testable import SwarmBarKit

final class FixtureTests: XCTestCase {
    func testStateFixtureDecodes() throws {
        let s: StateResponse = try Fixture.decode("state.json")
        XCTAssertEqual(s.agents.map(\.name), [
            "auth-epic-orchestrator", "crash-debug-orchestrator", "docs-fix-coder",
            "billing-spike-orchestrator", "search-spike-orchestrator",
        ])
        XCTAssertEqual(s.agents[0].children.map(\.name), ["login-form-coder", "login-review"])
        XCTAssertEqual(s.agents[0].finished.map(\.name), ["session-coder"])
        XCTAssertEqual(s.agents[0].children[1].session?.waiting, true)
        XCTAssertNil(s.agents[3].session)
        XCTAssertEqual(s.agents[4].preflightError, "Codex isn't installed on this Mac.")
        XCTAssertEqual(s.requests.map(\.kind), [.approveSection, .question, .approvePlan, .confirmRepos])
        XCTAssertEqual(s.requests[0].sectionTitle, "Session handling")
        XCTAssertEqual(s.requests.map(\.proposedRepos), [nil, nil, nil, 2])
        XCTAssertEqual(s.usage[0].error, "HTTP 429")
        XCTAssertNil(s.usage[1].error)
        XCTAssertEqual(s.notifications.unread, 4)
        XCTAssertEqual(s.notifications.items.count, 5)
        XCTAssertEqual(s.usage.map(\.agent), [.claude, .codex, .agy, .cursor])
        XCTAssertEqual(s.usage[2].headline?.label, "Claude & GPT 5h")
        XCTAssertEqual(s.activeCount, 4)
        XCTAssertEqual(s.settings.enabledAgents, [.claude, .codex, .agy])
        XCTAssertEqual(s.settings[.reviewer], RoleDefault(agent: .codex, model: "gpt-6-astra", effort: "high"))
        XCTAssertEqual(s.settings[.orchestrator]?.effort, "")
        let empty: StateResponse = try Fixture.decode("state-empty.json")
        XCTAssertTrue(empty.agents.isEmpty && empty.requests.isEmpty && empty.usage.isEmpty)
    }

    func testCatalogReposAndSmallShapesDecode() throws {
        let catalog: [AgentCatalogEntry] = try Fixture.decode("catalog.json")
        XCTAssertEqual(catalog.map(\.kind), [.claude, .codex, .agy, .cursor])
        XCTAssertEqual(catalog[0].models[0].aliases, ["fable"])
        XCTAssertEqual(catalog[0].models[3].efforts, [])
        XCTAssertEqual(catalog[1].models[0].defaultEffort, "medium")
        XCTAssertTrue(catalog[1].models[2].hidden)
        XCTAssertTrue(catalog[2].catalogStale)
        XCTAssertEqual(catalog[2].catalogError, "agy models timed out")
        XCTAssertEqual(catalog[2].authError, "agy isn't signed in.")
        XCTAssertEqual([catalog[0].catalogError, catalog[0].authError, catalog[3].version, catalog[3].defaultModel], ["", "", "", ""])
        XCTAssertEqual(catalog[0].defaultModel, "claude-opus-5")
        XCTAssertFalse(catalog[3].installed)

        let repos: ReposResponse = try Fixture.decode("repos.json")
        XCTAssertEqual(repos.recent.map(\.name), ["endurio-chat"])
        XCTAssertEqual(repos.groups.map(\.name), ["AlexanderTar", "EndurioApp", "endurio"])
        XCTAssertEqual(repos.all.last?.missing, true)
        XCTAssertNil(repos.all.last?.remoteOwner)
        XCTAssertNil(repos.all.last?.remoteUrl)
        XCTAssertNil(repos.all.last?.defaultBranch)
        XCTAssertEqual(repos.recent[0].lastUsedAt, Timestamp.minutes(-60))
        XCTAssertNil(repos.all[0].lastUsedAt)
        XCTAssertEqual(repos.recent[0].groups, ["EndurioApp", "endurio"])
        XCTAssertEqual(repos.scannedAt, Timestamp.minutes(-120))

        let manual: Repo = try Fixture.decode("repo.json")
        XCTAssertEqual(manual.source, "manual")
        XCTAssertEqual(try Fixture.decode("rescan.json", as: ScanStats.self), ScanStats(found: 112, missing: 1))
        XCTAssertEqual(try Fixture.decode("agent.json", as: AgentNode.self).name, "login-form-coder")
        XCTAssertEqual(try Fixture.decode("spike-response.json", as: CreateSpikeResponse.self).queued, false)
        let failed = try Fixture.decode("spike-response-preflight.json", as: CreateSpikeResponse.self)
        XCTAssertNil(failed.agent.session)
        XCTAssertEqual(failed.agent.preflightError, "Commit signing is off for endurio-chat. Enable it in git config.")
        XCTAssertEqual(try Fixture.decode("request.json", as: SwarmRequest.self).state, "answered")
        XCTAssertEqual(try Fixture.decode("notifications.json", as: [SwarmNotification].self).count, 5)
        XCTAssertEqual(try Fixture.decode("usage.json", as: [UsageSnapshot].self).count, 4)
        XCTAssertEqual(try Fixture.decode("terminal-open.json", as: TerminalOpen.self),
                       TerminalOpen(name: "login-form-coder", tmux: "login-form-coder"))
        XCTAssertEqual(try Fixture.decode("error-conflict.json", as: APIErrorBody.self).error.code, "conflict")
        XCTAssertEqual(try Fixture.decode("error-not-repo.json", as: APIErrorBody.self).error.code, "bad_request")
    }

    func testRequestBodiesEncodeExactlyLikeTheFixtures() throws {
        let settings: Settings = try Fixture.decode("settings.json")
        XCTAssertEqual(try Fixture.json(SwarmJSON.encode(settings)), try Fixture.json(Fixture.data("settings.json")))

        let spike = CreateSpikeBody(
            requestId: "4B1D6C8E-2F55-4E47-9A51-0C7B0D3F6A10", name: "Investigate login crash", intent: .debug,
            repos: ["repo_chat"], agent: .claude, model: "opus", effort: nil,
            advisor: .pair(agent: .claude, model: "fable", effort: nil),
            request: "Users see a crash after the second login attempt.")
        XCTAssertEqual(try Fixture.json(SwarmJSON.encode(spike)), try Fixture.json(Fixture.data("spike-request.json")))
        let cached: StateResponse = try Fixture.decode("state.json")
        XCTAssertEqual(try SwarmJSON.decode(StateResponse.self, from: SwarmJSON.encode(cached)), cached, "state survives the disk cache")
        XCTAssertEqual(try Fixture.decode("spike-request.json", as: CreateSpikeBody.self), spike)
    }

    func testAdvisorNoneAndTimestampForms() throws {
        XCTAssertEqual(String(decoding: try SwarmJSON.encode([AdvisorPayload.none]), as: UTF8.self), #"["none"]"#)
        XCTAssertEqual(try SwarmJSON.decode([AdvisorPayload].self, from: Data(#"["none"]"#.utf8)), [.none])
        let stamps = try SwarmJSON.decode([Timestamp].self, from: Data("[1789651920000]".utf8))
        XCTAssertEqual(stamps, [Timestamp(fixtureNow)])
        XCTAssertThrowsError(try SwarmJSON.decode([Timestamp].self, from: Data(#"["2026-09-17T13:32:00Z"]"#.utf8)),
                             "wire timestamps are integer ms only")
        XCTAssertEqual(String(decoding: try SwarmJSON.encode(Timestamp(ms: 5)), as: UTF8.self), "5")
    }

    func testContractManifestListsEveryFixture() throws {
        struct Manifest: Decodable { struct Entry: Decodable { let file: String; let direction: String }; let fixtures: [Entry] }
        let manifest: Manifest = try Fixture.decode("contract.json")
        let listed = Set(manifest.fixtures.map(\.file))
        let files = FileManager.default.enumerator(at: Fixture.dir, includingPropertiesForKeys: nil)!
            .compactMap { $0 as? URL }
            .filter { ["json", "sse"].contains($0.pathExtension) && $0.lastPathComponent != "contract.json" }
            .map { $0.path.replacingOccurrences(of: Fixture.dir.path + "/", with: "") }
        XCTAssertEqual(listed, Set(files))
        XCTAssertTrue(manifest.fixtures.allSatisfy { ["request", "response", "event"].contains($0.direction) })
    }

    func testSettingsDefaultsMatchTheSpec() {
        let d = Settings.defaults
        XCTAssertEqual(d[.orchestrator], RoleDefault(agent: .claude, model: "opus"))
        XCTAssertEqual(d[.advisor], RoleDefault(agent: .claude, model: "fable"))
        XCTAssertEqual(d[.mechanical]?.model, "haiku")
        XCTAssertEqual([d.maxOrchestrators, d.maxAgents, d.maxAgentsPerRoot, d.usagePollSec, d.pauseDeadlineSec], [3, 8, 4, 300, 120])
        XCTAssertEqual(d.pref(.info), NotifyPref(center: true, sound: true))
        var s = d
        s[.coder] = RoleDefault(agent: .codex, model: "gpt-6-astra")
        XCTAssertEqual(s.roles["coder"]?.agent, .codex)
    }
}
