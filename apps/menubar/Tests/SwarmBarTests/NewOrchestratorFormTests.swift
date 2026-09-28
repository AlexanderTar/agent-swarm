import AppKit
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
        if let max { settings.maxConcurrentAgents = max }
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
        XCTAssertEqual(f.agentOptions.map(\.label), ["Claude", "Codex", "Antigravity"])
        XCTAssertEqual(f.modelOptions.first?.label, "Fable 5.1 (latest)")
        XCTAssertEqual(f.effortOptions?.first?.label, "Default (high)")
        XCTAssertEqual(f.advisorAgentOptions.first?.label, "Claude")
        XCTAssertEqual(f.advisorAgentOptions.last?.label, "No advisor")
        XCTAssertEqual(f.intent, .chore)
        XCTAssertEqual(f.intentCaption, Copy.choreCaption)
        f.intent = .feature
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

    func testExplicitPrimaryEffortReachesBodyAndInvalidSelectionDoesNot() async {
        let f = await form()
        f.name = "x"
        f.setEffort("xhigh")
        XCTAssertEqual(f.body()?.effort, "xhigh")
        f.setEffort("ultra")
        XCTAssertNil(f.body()?.effort, "Opus does not offer ultra")
    }

    func testNativeClaudePairOmitsStoredAndSelectedAdvisorEffort() async {
        var settings = state.settings
        settings[.advisor] = RoleDefault(agent: .claude, model: "fable", effort: "xhigh")
        let f = NewOrchestratorForm(client: client, settings: settings, agents: state.agents, connected: true)
        await f.load()
        XCTAssertNil(f.advisorEffortOptions)
        f.setAdvisorEffort("max")
        XCTAssertEqual(f.body()?.advisor, .pair(agent: .claude, model: "fable", effort: nil))
    }

    func testClaudePrimaryCodexAdvisorUsesSelectedEffort() async {
        let f = await form()
        f.setAdvisorAgent("codex")
        XCTAssertNotNil(f.advisorEffortOptions)
        f.setAdvisorEffort("xhigh")
        XCTAssertEqual(f.body()?.advisor, .pair(agent: .codex, model: "gpt-6-astra", effort: "xhigh"))
        f.setAdvisorModel("gpt-5.3-codex")
        XCTAssertEqual(f.advisorEffort, "", "new model does not offer xhigh")
        XCTAssertEqual(f.body()?.advisor, .pair(agent: .codex, model: "gpt-5.3-codex", effort: nil))
    }

    func testCodexPrimaryClaudeAdvisorUsesNormalizedSettingsEffort() async {
        var settings = state.settings
        settings[.advisor] = RoleDefault(agent: .claude, model: "fable", effort: "xhigh")
        let f = NewOrchestratorForm(client: client, settings: settings, agents: state.agents, connected: true)
        await f.load()
        f.setAgent("codex")
        XCTAssertNotNil(f.advisorEffortOptions)
        XCTAssertEqual(f.advisorEffort, "xhigh")
        XCTAssertEqual(f.body()?.advisor, .pair(agent: .claude, model: "fable", effort: "xhigh"))
        f.setAdvisorModel("claude-sonnet-4-6")
        XCTAssertEqual(f.advisorEffort, "", "selected level is normalized for the new model")
    }

    func testNoAdvisorAndEffortlessModelHideAdvisorEffort() async {
        let f = await form()
        f.setAdvisorAgent("none")
        XCTAssertNil(f.advisorEffortOptions)
        XCTAssertEqual(f.body()?.advisor, AdvisorPayload.none)
        f.setModel("claude-haiku-4-5-20251001")
        XCTAssertNil(f.effortOptions)
        XCTAssertNil(f.body()?.effort)
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
        XCTAssertEqual(f.choice, AgentChoice(agent: .codex, model: "gpt-6-astra", effort: ""))
        XCTAssertNil(f.errors.model, "an incompatible switch substitutes the first model instead of an error")
        XCTAssertTrue(f.canStart)
        XCTAssertEqual(f.effortOptions?.first?.label, "Default (medium)")

        f.setAgent("agy")
        f.setModel("gemini-3.8-flash")
        XCTAssertEqual(f.errors.agent, "Antigravity isn't signed in. Run `agy` in a terminal.")
        f.setAgent("claude")
        f.setModel("claude-haiku-4-5-20251001")
        XCTAssertNil(f.effortOptions, "Effort is hidden for models without it")
        f.setAgent("bogus")
        XCTAssertEqual(f.choice.agent, .claude)

        f.setAdvisorModel("gone")
        XCTAssertEqual(f.advisor, .pair(.claude, "fable"), "unavailable models cannot enter a payload")
        f.setAdvisorAgent("none")
        XCTAssertTrue(f.errors.isValid)

        var noSuperpowers = client.catalogEntries
        noSuperpowers[0].superpowers = false
        client.catalogEntries = noSuperpowers
        await f.load()
        XCTAssertEqual(f.errors.agent, "Install the superpowers plugin for Claude to run orchestrators.")
    }

    func testReposPicker() async {
        let f = await form()
        XCTAssertEqual(f.rows.map(\.id), ["repo_swarm", "repo_app", "repo_chat", "repo_landing"])
        XCTAssertEqual(f.scanLine, "Scanned 2h ago")
        f.toggle(f.repos.all[4])
        XCTAssertEqual(f.selection, [], "missing repos can't be selected")
        f.toggle(f.repos.recent[0])
        f.toggle(f.repos.all[1])
        f.toggle(f.repos.all[3])
        XCTAssertEqual(f.selectedLine, "Selected: endurio-chat, endurio-app, endurio-landing")
        await f.search()
        await f.rescan()
        XCTAssertEqual(client.calls.suffix(3), ["repos ", "rescan", "repos "])

        client.reposResponse.all.append(Repo(id: "repo_new", name: "notes", path: "/Users/alex/.config/notes"))
        await f.addFolder("/Users/alex/.config/notes")
        XCTAssertEqual(f.selection.last, "repo_new")
        XCTAssertTrue(f.repos.all.contains { $0.id == "repo_new" })
        XCTAssertNil(f.repoError)
        client.failNext = .api(status: 422, code: "bad_request", message: "No git repository found in this folder.")
        await f.addFolder("/tmp")
        XCTAssertEqual(f.repoError, "No git repository found in this folder.")
    }

    func testRescanKeepsPresentSelectionAndReportsOneRemoval() async {
        let f = await form()
        f.selection = ["repo_chat", "repo_app"]
        client.reposResponse.all.removeAll { $0.id == "repo_chat" }
        await f.rescan()
        XCTAssertEqual(f.selection, ["repo_app"])
        XCTAssertEqual(f.selectionNotice, "1 selected repository is no longer available.")
        XCTAssertEqual(client.calls.suffix(2), ["rescan", "repos "])
    }

    func testAddFolderOnlySelectsVerifiedRefreshedRow() async {
        let f = await form()
        f.name = "x"
        await f.addFolder("/Users/alex/.config/notes")
        XCTAssertFalse(f.rows.contains { $0.id == "repo_new" })
        XCTAssertFalse(f.selection.contains("repo_new"))
        XCTAssertFalse(f.body()?.repos.contains("repo_new") ?? true)
        XCTAssertNotNil(f.repoError)
    }

    func testFailedRepoRefreshAndRescanShowErrorAndStopScanning() async {
        let f = await form()
        client.failNext = .unreachable
        await f.search()
        XCTAssertNotNil(f.repoError)
        client.failNext = .unreachable
        await f.rescan()
        XCTAssertFalse(f.repos.scanning)
        XCTAssertNotNil(f.repoError)
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
        // state.agents[0] (an orchestrator) carries 2 active children in the fixture --
        // 3 live agents total once the unified pool counts every role, not just orchestrators.
        XCTAssertFalse(NewOrchestratorForm.wouldQueue([state.agents[0]], max: 4))
        XCTAssertTrue(NewOrchestratorForm.wouldQueue([state.agents[0]], max: 1))
        XCTAssertFalse(NewOrchestratorForm.wouldQueue([], max: 1))

        let down = await form(connected: false)
        down.name = "x"
        XCTAssertFalse(down.canStart)
        let result = await down.submit()
        XCTAssertNil(result)
        XCTAssertFalse(client.calls.contains { $0.hasPrefix("spike") })
    }

    func testAdvisorAgentAndModelStayValid() async {
        let f = await form()
        XCTAssertEqual(f.advisorAgentOptions.map(\.label), ["Claude", "Codex", "Antigravity", "No advisor"])
        XCTAssertFalse(f.advisorModelOptions.isEmpty)
        f.setAdvisorAgent("codex")
        XCTAssertEqual(f.advisor, .pair(.codex, f.advisorModelOptions[0].value))
        let alternate = try? XCTUnwrap(f.advisorModelOptions.last?.value)
        if let alternate { f.setAdvisorModel(alternate) }
        if case let .pair(agent, model, _) = f.body()?.advisor {
            XCTAssertEqual(agent, .codex)
            XCTAssertEqual(model, alternate)
        } else { XCTFail("advisor pair missing") }
        f.setAdvisorAgent("none")
        XCTAssertEqual(f.advisor, .none)
        XCTAssertTrue(f.advisorModelOptions.isEmpty)
        XCTAssertEqual(f.body()?.advisor, AdvisorPayload.none)
    }

    func testRemovedAdvisorModelAndEmptyCatalogCannotReachBody() async {
        var settings = state.settings
        settings[.advisor] = RoleDefault(agent: .claude, model: "gone", effort: "high")
        let f = NewOrchestratorForm(client: client, settings: settings, agents: state.agents, connected: true)
        await f.load()
        XCTAssertNotEqual(f.advisor, .pair(.claude, "gone"))
        f.name = "x"
        XCTAssertNil(f.errors.advisor)
        client.catalogEntries = []
        await f.load()
        XCTAssertEqual(f.advisor, .none)
        XCTAssertEqual(f.body()?.advisor, AdvisorPayload.none)
    }

    // MARK: images

    /// A valid 1×1 PNG, inline so the images tests need no fixture file.
    private var pngData: Data {
        Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
              0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
              0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0xF8, 0x0F, 0x00, 0x00,
              0x01, 0x01, 0x00, 0x05, 0x18, 0xD8, 0x4E, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
              0x42, 0x60, 0x82])
    }

    func testAddImageKeepsPNGAsIs() async {
        let f = await form()
        f.addImage(data: pngData, name: "a.png")
        XCTAssertEqual(f.images.count, 1)
        XCTAssertEqual(f.images.first?.data, pngData)
        XCTAssertNil(f.imageError)
    }

    func testAddImageConvertsTIFFToPNG() async {
        let f = await form()
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        let tiff = rep.tiffRepresentation!
        f.addImage(data: tiff, name: "a.tiff")
        XCTAssertEqual(f.images.count, 1)
        XCTAssertEqual(f.images.first?.data.prefix(4), Data([0x89, 0x50, 0x4E, 0x47]))
    }

    func testAddImageRejectsUnreadableData() async {
        let f = await form()
        f.addImage(data: Data("text".utf8), name: "n.txt")
        XCTAssertEqual(f.imageError, "n.txt is not an image Swarm can attach.")
        XCTAssertEqual(f.images.count, 0)
    }

    func testAddImageCapsAtTen() async {
        let f = await form()
        for i in 0..<11 { f.addImage(data: pngData, name: "\(i).png") }
        XCTAssertEqual(f.imageError, "Up to 10 images.")
        XCTAssertEqual(f.images.count, 10)
    }

    func testAddImageRejectsOversize() async {
        let f = await form()
        var big = pngData
        big.append(Data(count: 10 << 20))
        f.addImage(data: big, name: "big.png")
        XCTAssertEqual(f.imageError, "big.png is larger than 10 MB.")
        XCTAssertEqual(f.images.count, 0)
    }

    func testRemoveImageClearsErrorAndEntry() async {
        let f = await form()
        f.addImage(data: pngData, name: "a.png")
        f.addImage(data: Data("text".utf8), name: "n.txt")
        XCTAssertNotNil(f.imageError)
        f.removeImage(f.images[0].id)
        XCTAssertEqual(f.images.count, 0)
        XCTAssertNil(f.imageError)
    }

    func testSubmitCarriesAttachmentsAsBase64() async throws {
        let f = await form()
        f.name = "x"
        f.addImage(data: pngData, name: "a.png")
        let body = try XCTUnwrap(f.body())
        XCTAssertEqual(body.attachments, [AttachmentPayload(name: "a.png", data: pngData.base64EncodedString())])
    }

    func testBodyOmitsAttachmentsWhenEmpty() async throws {
        let f = await form()
        f.name = "x"
        let body = try XCTUnwrap(f.body())
        XCTAssertNil(body.attachments)
    }

    func testAttachmentsFailedSetsStartedWithUnsavedImages() async throws {
        let f = await form()
        f.name = "x"
        f.addImage(data: pngData, name: "a.png")
        client.spikeResult = .success(CreateSpikeResponse(
            agent: AgentNode(name: "x", kind: .claude, model: "opus", role: .orchestrator), queued: false, attachmentsFailed: true))
        let created = await f.submit()
        XCTAssertNotNil(created)
        XCTAssertNotNil(f.startedWithUnsavedImages)
    }
}
