import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class SettingsRenderTests: XCTestCase {
    func testWindowAndEveryTabRender() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let settings = m.makeSettings()
        await settings.load()
        XCTAssertEqual(renderedSize(SettingsView(model: settings)), CGSize(width: 740, height: 520))
        let tabs: [AnyView] = [AnyView(AgentsTab(model: settings)), AnyView(DefaultsTab(model: settings)),
                               AnyView(NotificationsTab(model: settings)), AnyView(LimitsTab(model: settings))]
        for tab in tabs {
            XCTAssertGreaterThan(renderedSize(tab.frame(width: 740)).height, 0)
        }
        await settings.setEnabled(.codex, false)
        await settings.setLimit(.subagents, 1)
        settings.connected = false
        await settings.save()
        XCTAssertEqual(renderedSize(SettingsView(model: settings)), CGSize(width: 740, height: 520))
    }
}
