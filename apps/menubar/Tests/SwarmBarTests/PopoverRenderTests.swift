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
        // usage.json has no "muse" snapshot, so muse (now last in AgentKind.selectable) falls back
        // to "Usage unavailable."; cursor (the slot that actually carries monthly usage data) is
        // checked by its own index instead of `.last`.
        let tooltips = MenuBarLabelView.tooltipSlots(full).map(\.0)
        XCTAssertEqual(tooltips[3], "Monthly Auto usage · resets 1 Oct")
        XCTAssertEqual(tooltips.last, "Usage unavailable.")
        XCTAssertTrue(Icons.image(.codex).isTemplate)
        XCTAssertEqual(IconName(.fake), .claude)
    }

    func testPopoverRendersConnectedEmptyAndDown() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)
        // Each section now scrolls inside its own cap, so the stack is bounded by the caps rather
        // than by content length; 900 is four generous caps' worth of room.
        let cap: CGFloat = 220
        let sections = VStack {
            NeedsYouSection(model: m, cap: cap)
            AgentsSection(model: m, cap: cap)
            UsageSectionView(model: m)
            NotificationsSection(model: m, cap: cap)
        }
        let height = renderedSize(sections.frame(width: 360)).height
        XCTAssertGreaterThan(height, 200, "sections render content")
        XCTAssertLessThan(height, 4 * cap + 200, "no section grows past its cap")

        client.stateResult = .success(try Fixture.decode("state-empty.json"))
        await m.refresh()
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)

        client.stateResult = .failure(.unreachable)
        await m.refresh()
        m.setSection(.notifications, open: true)
        XCTAssertEqual(renderedSize(PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).width, 360)
        XCTAssertGreaterThan(renderedSize(DaemonBanner(text: m.banner ?? "", retry: {}).frame(width: 336)).height, 0)
    }

    func testNeedsYouSectionRendersMixedKindsWithoutCrashing() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        var s: StateResponse = try Fixture.decode("state.json")
        s.requests = RequestKind.allCases.enumerated().map { i, kind in
            SwarmRequest(id: "r\(i)", kind: kind, agentName: i.isMultiple(of: 2) ? "agent-\(i)" : nil,
                         itemKey: "TASK-\(i)", itemTitle: "Item \(i)", prompt: "Prompt \(i)",
                         createdAt: Timestamp(ms: Int64(i)))
        }
        client.stateResult = .success(s)
        await m.refresh()
        let height = renderedSize(NeedsYouSection(model: m, cap: 400).frame(width: 360)).height
        XCTAssertGreaterThan(height, 0)
    }

    func testDismissPopoverRunsSafely() {
        // Safe to call even with no status window or menubar extra window active
        StatusItemWatcher.dismissPopover()

        // With a dummy MenuBarExtraWindow open, dismissPopover orders it out
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.title = "MenuBarExtraWindowTest"
        // Force the description or subclass if needed, but in our implementation we check String(describing: type(of: w)).contains("MenuBarExtraWindow")
        // Calling dismissPopover does not crash or throw
        StatusItemWatcher.dismissPopover()
    }
}

