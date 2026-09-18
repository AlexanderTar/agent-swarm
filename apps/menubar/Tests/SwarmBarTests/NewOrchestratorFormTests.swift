import Foundation
import XCTest
@testable import SwarmBarKit

@MainActor
final class NewOrchestratorFormTests: XCTestCase {
    var client: MockDaemonClient!
    var state = StateResponse()

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        state = try Fixture.decode("state.json")
    }

    private func form(connected: Bool = true, max: Int? = nil, agents: [AgentNode]? = nil) async -> NewOrchestratorForm {
        var settings = state.settings
        if let max { settings.maxOrchestrators = max }
        let f = NewOrchestratorForm(client: client, settings: settings, agents: agents ?? state.agents, connected: connected,
                                    format: Format(now: fixtureNow))
        await f.load()
        return f
    }

    func testPrefillFromSettingsAndPreview() async {
        let f = await form()
        XCTAssertEqual(f.choice, AgentChoice(agent: .claude, model: "opus"))
        XCTAssertEqual(f.advisor, .pair(.claude, "fable"))
        XCTAssertEqual(f.preview, "")
        XCTAssertNil(f.nameError)
        XCTAssertFalse(f.canStart)
        f.name = "Investigate login crash"
        XCTAssertEqual(f.preview, "Agent name: investigate-login-crash")
        XCTAssertNil(f.nameError)
        XCTAssertTrue(f.canStart)
        XCTAssertEqual(f.agentOptions.map(\.label), ["Claude", "Codex", "agy"])
        XCTAssertEqual(f.modelOptions.first?.label, "Fable (latest)")
        XCTAssertEqual(f.effortOptions?.first?.label, "Default (high)")
        XCTAssertEqual(f.advisorOptions.first?.label, "Claude · Fable (latest)")
        XCTAssertEqual(f.advisorOptions.last?.label, "No advisor")
        XCTAssertEqual(f.intentCaption, "Creates a spike to explore this request and turn it into an epic.")
        f.intent = .debug
        XCTAssertEqual(f.intentCaption, "Creates a spike to find the root cause and turn it into a bug with a fix plan.")
    }

    /// `CatalogRules.prefill` builds `choice` from `Settings` while `catalog == []` (it hasn't loaded
    /// yet), so a stored effort the catalog doesn't offer for that model must be re-checked once
    /// `load()` brings the catalog in, or it survives straight into `body()`.
    func testStoredEffortIsNormalisedOnceTheCatalogArrives() async {
        var settings = state.settings
        settings[.orchestrator] = RoleDefault(agent: .claude, model: "opus", effort: "ultra") // opus doesn't offer "ultra"
        let f = NewOrchestratorForm(client: client, settings: settings, agents: state.agents, connected: true,
                                    format: Format(now: fixtureNow))
        XCTAssertEqual(f.choice.effort, "ultra", "before load() the catalog hasn't arrived to check it against")
        await f.load()
        XCTAssertEqual(f.choice.effort, "", "normalised once the catalog arrives")
        f.name = "x"
        XCTAssertNil(f.body()?.effort, "a level the model doesn't offer must never reach the spike body")
    }

    func testNameValidationMessages() async {
        let f = await form()
        f.name = "🔥🔥"
        XCTAssertEqual(f.nameError, "Enter a name containing a letter or number.")
        XCTAssertEqual(f.preview, "")
        XCTAssertFalse(f.canStart)
        f.name = "Login Form Coder"
        XCTAssertEqual(f.nameError, "This agent name is already in use.")
        f.name = "Session coder"
        XCTAssertEqual(f.nameError, "This agent name is already in use.", "finished agents keep their names")
        XCTAssertFalse(f.canStart)
    }

    func testAgentModelEffortAndAdvisorMessages() async {
        let f = await form()
        f.name = "x"
        f.setModel("claude-opus-5")
        f.setEffort("xhigh")
        f.setModel("claude-sonnet-4-6")
        XCTAssertEqual(f.effortNote, "xhigh isn't available for Sonnet 4.6; using the default.")
        XCTAssertEqual(f.choice.effort, "")
        f.setEffort("max")
        XCTAssertNil(f.effortNote)

        f.setAgent("codex")
        XCTAssertEqual(f.choice, AgentChoice(agent: .codex, model: ""))
        XCTAssertEqual(f.errors.model, "Choose a model available for this agent.")
        XCTAssertFalse(f.canStart)
        XCTAssertNil(f.effortOptions)
        f.setModel("gpt-6-astra")
        XCTAssertTrue(f.canStart)
        XCTAssertEqual(f.effortOptions?.first?.label, "Default (medium)")

        f.setAgent("agy")
        f.setModel("gemini-3.8-flash")
        XCTAssertEqual(f.errors.agent, "agy isn't signed in. Run `agy` in a terminal.")
        f.setAgent("claude")
        f.setModel("claude-haiku-4-5-20251001")
        XCTAssertNil(f.effortOptions, "Effort is hidden for models without it")
        f.setAgent("bogus")
        XCTAssertEqual(f.choice.agent, .claude)

        f.setAdvisor("claude:gone")
        XCTAssertEqual(f.errors.advisor, "gone is no longer offered by Claude.")
        f.setAdvisor("none")
        XCTAssertTrue(f.errors.isValid)

        var noSuperpowers = client.catalogEntries
        noSuperpowers[0].superpowers = false
        client.catalogEntries = noSuperpowers
        await f.load()
        XCTAssertEqual(f.errors.agent, "Install the superpowers plugin for Claude to run orchestrators.")
    }

    func testReposPicker() async {
        let f = await form()
        XCTAssertEqual(f.sections.map(\.title), ["Recent", "endurio", "AlexanderTar", "EndurioApp", "All"])
        XCTAssertEqual(f.scanLine, "Scanned 2h ago")
        f.toggle(f.repos.all[4])
        XCTAssertEqual(f.selection, [], "missing repos can't be selected")
        f.toggle(f.repos.recent[0])
        f.selectAll(f.sections[1])
        XCTAssertEqual(f.selectedLine, "Selected: endurio-chat, endurio-app, endurio-landing")
        f.query = "endurio"
        await f.search()
        await f.rescan()
        XCTAssertEqual(client.calls.suffix(3), ["repos endurio", "rescan", "repos endurio"])

        await f.addFolder("/Users/alex/.config/notes")
        XCTAssertEqual(f.selection.last, "repo_new")
        XCTAssertTrue(f.repos.all.contains { $0.id == "repo_new" })
        XCTAssertNil(f.repoError)
        client.failNext = .api(status: 422, code: "bad_request", message: "No git repository found in this folder.")
        await f.addFolder("/tmp")
        XCTAssertEqual(f.repoError, "No git repository found in this folder.")
    }

    func testSubmitBuildsTheSpikeBody() async throws {
        let f = await form()
        f.name = "  Investigate login crash "
        f.intent = .debug
        f.toggle(f.repos.recent[0])
        f.request = "Users see a crash after the second login attempt.\n"
        let body = try XCTUnwrap(f.body())
        XCTAssertEqual(body.name, "Investigate login crash")
        XCTAssertEqual(body.intent, .debug)
        XCTAssertEqual(body.repos, ["repo_chat"])
        XCTAssertEqual(body.agent, .claude)
        XCTAssertEqual(body.model, "opus")
        XCTAssertNil(body.effort)
        XCTAssertEqual(body.advisor, .pair(agent: .claude, model: "fable", effort: nil))
        XCTAssertEqual(body.request, "Users see a crash after the second login attempt.")
        f.request = " "
        XCTAssertNil(f.body()?.request)

        let created = await f.submit()
        XCTAssertEqual(created?.name, "Investigate login crash")
        XCTAssertEqual(client.calls.last, "spike Investigate login crash")
        XCTAssertNil(f.failure)
    }

    func testFailureKeepsEntriesAndOffersTryAgain() async {
        let f = await form()
        f.name = "Investigate login crash"
        f.toggle(f.repos.recent[0])
        XCTAssertEqual(f.startLabel, "Queue orchestrator")
        client.spikeResult = .failure(.api(status: 422, code: "preflight_failed", message: "Commit signing is off for endurio-chat. Enable it in git config."))
        let firstID = f.body()?.requestId
        let created = await f.submit()
        XCTAssertNil(created)
        XCTAssertEqual(f.failure, "Couldn't start orchestrator. Your entries are saved. Commit signing is off for endurio-chat. Enable it in git config.")
        XCTAssertEqual(f.startLabel, "Try again")
        XCTAssertEqual(f.name, "Investigate login crash")
        XCTAssertEqual(f.selection, ["repo_chat"])
        XCTAssertNotEqual(f.body()?.requestId, firstID, "a refused request gets a new id")

        client.spikeResult = .failure(.unreachable)
        let keptID = f.body()?.requestId
        _ = await f.submit()
        XCTAssertEqual(f.failure, "Couldn't start orchestrator. Your entries are saved.")
        XCTAssertEqual(f.body()?.requestId, keptID, "a lost response keeps the id so a retry is idempotent")

        f.request = "Now with more detail."
        _ = await f.submit()
        XCTAssertNotEqual(f.body()?.requestId, keptID, "an edited retry must not replay the earlier result")

        client.spikeResult = nil
        let retried = await f.submit()
        XCTAssertNotNil(retried)
        XCTAssertNil(f.failure)
    }

    func testPreflightFailureStillCreatesTheSpike() async throws {
        let f = await form()
        f.name = "Investigate login crash"
        client.spikeResult = .success(try Fixture.decode("spike-response-preflight.json"))
        let result = await f.submit()
        let created = try XCTUnwrap(result, "the window closes; the row offers Retry")
        XCTAssertNil(f.failure)
        XCTAssertEqual(DisplayState(created), .preflightFailed)
        XCTAssertEqual(AgentTree.actions(created, tmuxAlive: false, connected: true).map(\.label), ["Retry", "Cancel"])
    }

    func testQueuedLabelAndDaemonDown() async {
        let atLimit = await form(max: 1)
        XCTAssertEqual(atLimit.startLabel, "Queue orchestrator")
        XCTAssertEqual(atLimit.queuedCaption, "Starts when an agent slot becomes available.")
        let waiting = await form(max: 8)
        XCTAssertEqual(waiting.startLabel, "Queue orchestrator", "a queued orchestrator already waits in the fixture")
        let free = await form(max: 8, agents: [state.agents[0]])
        XCTAssertEqual(free.startLabel, "Start orchestrator")
        XCTAssertNil(free.queuedCaption)
        XCTAssertFalse(NewOrchestratorForm.wouldQueue([state.agents[0]], max: 2))
        XCTAssertTrue(NewOrchestratorForm.wouldQueue([state.agents[0]], max: 1))
        XCTAssertFalse(NewOrchestratorForm.wouldQueue([], max: 1))

        let down = await form(connected: false)
        down.name = "x"
        XCTAssertFalse(down.canStart)
        let result = await down.submit()
        XCTAssertNil(result)
        XCTAssertFalse(client.calls.contains { $0.hasPrefix("spike") })
    }
}
