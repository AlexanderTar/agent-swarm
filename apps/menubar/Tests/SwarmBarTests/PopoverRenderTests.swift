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

    func testLabelFitsToContentAndTooltipSlotsMatch() {
        func label(_ texts: [String], compact: Bool = false) -> MenuLabel {
            MenuLabel(count: "1", segments: zip(AgentKind.selectable, texts).map { kind, text in
                MenuLabel.Segment(agent: kind, text: text, dimmed: false, tooltip: kind.rawValue)
            }, compact: compact)
        }
        func textWidth(_ s: String) -> CGFloat {
            let font = NSFont.monospacedDigitSystemFont(ofSize: MenuBarLabelView.fontSize, weight: .regular)
            return ceil((s as NSString).size(withAttributes: [.font: font]).width)
        }
        let mixed = label(["36%", "100%", "0%", "5%"])
        let widths = MenuBarLabelView.tooltipSlots(mixed).map(\.1)
        XCTAssertEqual(widths.count, 4)
        XCTAssertGreaterThan(widths[1], widths[0], "no fixed 100% slot: each segment is as wide as its own text")
        XCTAssertGreaterThan(widths[0], widths[2])
        // Each slot is its own icon + text plus the trailing inter-segment gap, so the
        // watcher can step x by slot widths and stay aligned with the HStack(spacing:) layout.
        let texts = ["36%", "100%", "0%", "5%"]
        for (i, text) in texts.enumerated() {
            let expected = MenuBarLabelView.iconSize + MenuBarLabelView.innerSpacing
                + textWidth(text) + MenuBarLabelView.segmentSpacing
            XCTAssertEqual(widths[i], expected, accuracy: 0.5, "slot \(i) equals icon+text width plus spacing")
        }
        var expectedSum: CGFloat = 0
        for text in texts {
            expectedSum += MenuBarLabelView.iconSize + MenuBarLabelView.innerSpacing + textWidth(text)
        }
        expectedSum += CGFloat(texts.count) * MenuBarLabelView.segmentSpacing
        let actualSum: CGFloat = widths.reduce(0, +)
        XCTAssertEqual(actualSum, expectedSum, accuracy: 1,
                       "slots sum to content plus one gap per segment, matching the rendered label minus the count")
        let compactWidths = MenuBarLabelView.tooltipSlots(label(["36%", "100%", "0%", "5%"], compact: true)).map(\.1)
        XCTAssertTrue(compactWidths.allSatisfy { abs($0 - MenuBarLabelView.compactSlotWidth) < 0.5 },
                      "compact shows icons only: every slot is the shared compact width")
        let narrow = LabelRenderer.image(label(["5%", "0%", "5%", "0%"]))
        let wide = LabelRenderer.image(label(["100%", "100%", "100%", "100%"]))
        XCTAssertLessThan(narrow.size.width, wide.size.width, "rendered label fits to content")
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

    func testLowTokenControlsRenderInEveryState() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        XCTAssertGreaterThan(renderedSize(EcoLeaf()).width, 0)
        for on in [true, false] {
            XCTAssertGreaterThan(renderedSize(IconButton(on ? "leaf.fill" : "leaf", help: Copy.lowTokenTurnOnAll,
                                                         tint: on ? .green : nil, toggleValue: on) {}).width, 0)
        }
        XCTAssertGreaterThan(renderedSize(AgentsSection(model: m, cap: 400).frame(width: 360)).height, 100, "header toggle on")
        var off: StateResponse = try Fixture.decode("state.json")
        off.settings.lowTokenMode = false
        client.stateResult = .success(off)
        await m.refresh()
        XCTAssertGreaterThan(renderedSize(AgentsSection(model: m, cap: 400).frame(width: 360)).height, 100, "header toggle off")
        client.stateResult = .failure(.unreachable)
        await m.refresh()
        XCTAssertTrue(m.lowTokenAllDisabled)
        XCTAssertGreaterThan(renderedSize(AgentsSection(model: m, cap: 400).frame(width: 360)).height, 100, "disconnected")
    }

    func testDefaultsTabShowsLowTokenCheckboxAndCaption() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let settings = m.makeSettings()
        await settings.load()
        let host = NSHostingView(rootView: DefaultsTab(model: settings).frame(width: 740))
        host.frame = NSRect(origin: .zero, size: host.fittingSize)
        let text = try ocrText(host)
        XCTAssertTrue(text.contains("Low-token mode"), text)
        XCTAssertTrue(text.contains("fewer tokens"), text)
    }

    func testNeedsYouSectionRendersMixedKindsWithoutCrashing() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        var s: StateResponse = try Fixture.decode("state.json")
        s.requests = RequestKind.allCases.enumerated().map { i, kind in
            SwarmRequest(id: "r\(i)", kind: kind, isHITL: true, agentName: i.isMultiple(of: 2) ? "agent-\(i)" : nil,
                         itemKey: "TASK-\(i)", itemTitle: "Item \(i)", prompt: "Prompt \(i)",
                         createdAt: Timestamp(ms: Int64(i)))
        }
        client.stateResult = .success(s)
        await m.refresh()
        XCTAssertEqual(m.visibleRequests.count, 3, "rows actually render")
        let height = renderedSize(NeedsYouSection(model: m, cap: 400).frame(width: 360)).height
        XCTAssertGreaterThan(height, 0)
    }

    /// TASK-504: on macOS 27 the MenuBarExtraWindow keeps its height when the popover's content
    /// gets shorter while it is shown, so the content (with its own opaque square background) sat
    /// centered in a taller rounded window: a panel inside a frame, gaps above and below. The
    /// popover fits its own window to its content, top edge pinned under the menu bar.
    func testPopoverFitsItsWindowWhenContentIsShorter() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        client.stateResult = .success(try Fixture.decode("state-empty.json"))
        let m = makeAppModel(client)
        await m.refresh()
        let host = NSHostingView(rootView: PopoverView(model: m, openNewOrchestrator: {}, openSettings: {}))
        host.sizingOptions = [] // like the macOS 27 MenuBarExtraWindow: the window does not follow the content
        let window = NSWindow(contentRect: NSRect(x: 100, y: 100, width: 360, height: 850),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host
        let top = window.frame.maxY
        for _ in 0..<10 {
            host.layoutSubtreeIfNeeded()
            try await Task.sleep(for: .milliseconds(20))
        }
        let content = NSHostingView(rootView: PopoverView(model: m, openNewOrchestrator: {}, openSettings: {})).fittingSize.height
        XCTAssertLessThan(content, 700, "sparse content is shorter than the stale window")
        XCTAssertEqual(window.frame.height, content, accuracy: 1, "window fits the content: no gaps above or below")
        XCTAssertEqual(window.frame.maxY, top, accuracy: 0.5, "top edge stays under the status item")
    }
}
