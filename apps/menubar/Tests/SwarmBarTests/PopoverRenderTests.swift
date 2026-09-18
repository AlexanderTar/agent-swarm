import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class PopoverRenderTests: XCTestCase {
    func testMenuBarLabelRendersEveryState() throws {
        let usage: [UsageSnapshot] = try Fixture.decode("usage.json")
        // Deterministic across machine time zones, matching MenuLabelTests' own fixed-UTC pattern:
        // "resets 1 Oct" depends on dayMonth, which uses `timeZone` and would read "30 Sep" west of UTC.
        let format = Format(now: fixtureNow, timeZone: TimeZone(identifier: "UTC")!)
        let full = MenuLabel.make(activeCount: 6, connected: true, enabled: AgentKind.selectable, usage: usage, compact: false, format: format)
        let compact = MenuLabel.make(activeCount: 6, connected: true, enabled: AgentKind.selectable, usage: usage, compact: true, format: format)
        let down = MenuLabel.make(activeCount: 0, connected: false, enabled: [.claude], usage: [], compact: false, format: format)
        let fullImage = LabelRenderer.image(full)
        XCTAssertTrue(fullImage.isTemplate)
        XCTAssertGreaterThan(fullImage.size.width, LabelRenderer.image(compact).size.width)
        XCTAssertGreaterThan(LabelRenderer.image(down).size.width, 0)
        XCTAssertEqual(MenuBarLabelView.tooltipSlots(full).map(\.0).last, "Monthly Auto usage · resets 1 Oct")
        XCTAssertTrue(Icons.image(.codex).isTemplate)
        XCTAssertEqual(IconName(.fake), .claude)
    }

    func testPopoverRendersConnectedEmptyAndDown() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)
        let sections = VStack {
            NeedsYouSection(model: m)
            AgentsSection(model: m, openNewOrchestrator: {})
            UsageSectionView(model: m)
            NotificationsSection(model: m)
        }
        XCTAssertGreaterThan(renderedSize(sections.frame(width: 360)).height, 600)

        client.stateResult = .success(try Fixture.decode("state-empty.json"))
        await m.refresh()
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)

        client.stateResult = .failure(.unreachable)
        await m.refresh()
        m.setSection(.notifications, open: true)
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)
        XCTAssertGreaterThan(renderedSize(DaemonBanner(text: m.banner ?? "", retry: {}).frame(width: 336)).height, 0)
    }
}
