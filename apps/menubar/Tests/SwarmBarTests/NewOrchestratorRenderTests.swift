import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class NewOrchestratorRenderTests: XCTestCase {
    func testFormRendersValidInvalidAndFailed() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let form = m.makeNewOrchestratorForm()
        await form.load()
        XCTAssertEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 480)
        form.name = "🔥"
        form.setAgent("codex")
        XCTAssertEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 480)
        form.name = "Investigate login crash"
        form.setModel("gpt-6-astra")
        client.spikeResult = .failure(.unreachable)
        _ = await form.submit()
        XCTAssertNotNil(form.failure)
        XCTAssertEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 480)
        let repos: ReposResponse = try Fixture.decode("repos.json")
        XCTAssertGreaterThan(renderedSize(RepoRow(repo: repos.all[4], selected: false, toggle: {})).height, 0)
    }
}
