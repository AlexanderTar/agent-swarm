import Foundation
import XCTest
@testable import SwarmBarKit

final class MenuLabelTests: XCTestCase {
    let format = Format(now: fixtureNow, timeZone: TimeZone(identifier: "UTC")!)
    var usage: [UsageSnapshot] = []

    override func setUpWithError() throws {
        usage = try Fixture.decode("usage.json")
    }

    private func texts(_ l: MenuLabel) -> [String] { l.segments.map { "\($0.agent.rawValue) \($0.text)\($0.dimmed ? " dim" : "")" } }

    func testZeroToFourAgents() {
        let zero = MenuLabel.make(activeCount: 0, connected: true, enabled: [], usage: usage, compact: false, format: format)
        XCTAssertEqual(zero.count, "0")
        XCTAssertEqual(zero.segments, [])
        let one = MenuLabel.make(activeCount: 6, connected: true, enabled: [.codex], usage: usage, compact: false, format: format)
        XCTAssertEqual(texts(one), ["codex 18%"])
        let all = MenuLabel.make(activeCount: 6, connected: true, enabled: [.cursor, .agy, .codex, .claude],
                                 usage: usage, compact: false, format: format)
        XCTAssertEqual(all.count, "6")
        XCTAssertEqual(texts(all), ["claude 42% dim", "codex 18%", "agy 6%", "cursor 27%"])
        XCTAssertEqual(MenuLabel.widestValue, "100%")
        XCTAssertEqual(MenuLabel.widestMonthlyValue, "100%", "no more bare \"M\" suffix on monthly meters")
    }

    func testNoDataAndDaemonDown() {
        let l = MenuLabel.make(activeCount: 3, connected: false, enabled: [.claude, .fake], usage: [], compact: false, format: format)
        XCTAssertEqual(l.count, "", "no bare \"?\" next to the icon while the daemon is down")
        XCTAssertEqual(texts(l), ["claude --"])
        XCTAssertEqual(l.segments[0].tooltip, "Usage unavailable.")
        var empty = usage[1]
        empty.meters = []
        XCTAssertEqual(texts(MenuLabel.make(activeCount: 0, connected: true, enabled: [.codex], usage: [empty], compact: false, format: format)),
                       ["codex --"])
    }

    func testTooltips() {
        let l = MenuLabel.make(activeCount: 6, connected: true, enabled: [.claude, .codex, .agy, .cursor],
                               usage: usage, compact: false, format: format)
        XCTAssertEqual(l.segments.map(\.tooltip), [
            "Last updated 12 min ago.",
            "5h 18%",
            "Gemini 5h 6%",
            "Monthly Auto usage · resets 1 Oct",
        ])
        // A snapshot that never fetched is stale with fetched_at 0, which is not an age.
        var never = usage[0]
        never.fetchedAt = Timestamp(ms: 0)
        let unfetched = MenuLabel.make(activeCount: 0, connected: true, enabled: [.claude], usage: [never],
                                       compact: false, format: format)
        XCTAssertEqual(unfetched.segments.map(\.tooltip), ["Never updated."], "the tooltip slot is a sentence")
        XCTAssertEqual(UsageSection.rows(never, format: format).map(\.trailing), Array(repeating: "Never updated", count: 3))

        var noReset = usage[3]
        noReset.meters[0].resetsAt = nil
        XCTAssertEqual(MenuLabel.make(activeCount: 0, connected: true, enabled: [.cursor], usage: [noReset], compact: false, format: format)
            .segments[0].tooltip, "Monthly Auto usage")
    }

    func testCompactShowsIconsOnly() {
        let l = MenuLabel.make(activeCount: 6, connected: true, enabled: [.claude, .codex, .agy, .cursor],
                               usage: usage, compact: true, format: format)
        XCTAssertTrue(l.compact)
        XCTAssertEqual(l.segments.map(\.text), ["", "", "", ""])
        XCTAssertEqual(MenuLabel.make(activeCount: 0, connected: true, enabled: [.claude], usage: [], compact: true, format: format)
            .segments.map(\.text), [""])
    }

    func testCompactSwitchesOnItsOwnAndBack() {
        var c = CompactSwitch()
        XCTAssertFalse(c.effective(setting: false))
        c.labelVisible(true, setting: false)
        XCTAssertFalse(c.autoOverride)
        c.labelVisible(false, setting: false)
        XCTAssertTrue(c.effective(setting: false))
        XCTAssertTrue(c.notePending)
        c.noteShown()
        XCTAssertFalse(c.notePending)
        c.labelVisible(false, setting: false)
        XCTAssertFalse(c.notePending)

        c.userSet(false)
        XCTAssertFalse(c.effective(setting: false))
        c.labelVisible(false, setting: false)
        XCTAssertFalse(c.effective(setting: false), "no fight while the user's choice stands")
        c.labelVisible(true, setting: false)
        c.labelVisible(false, setting: false)
        XCTAssertTrue(c.effective(setting: false), "switches again after being visible")

        var user = CompactSwitch()
        user.labelVisible(false, setting: true)
        XCTAssertFalse(user.autoOverride, "already compact by choice")
        user.userSet(true)
        XCTAssertTrue(user.effective(setting: true))
        XCTAssertFalse(user.suppressed)
    }

    func testUsageSectionRows() {
        XCTAssertEqual(UsageSection.pickerAgents(enabled: [.cursor, .claude, .fake]), [.claude, .cursor])
        XCTAssertEqual(UsageSection.selection(current: nil, enabled: [.codex, .claude]), .claude)
        XCTAssertEqual(UsageSection.selection(current: .codex, enabled: [.codex, .claude]), .codex)
        XCTAssertEqual(UsageSection.selection(current: .agy, enabled: [.codex]), .codex)
        XCTAssertNil(UsageSection.selection(current: nil, enabled: []))

        let claude = UsageSection.rows(usage[0], format: format)
        XCTAssertEqual(claude.map(\.label), ["5h", "Weekly (all models)", "Fable weekly"])
        XCTAssertEqual(claude.map(\.used), ["42% used", "31% used", "18% used"])
        XCTAssertEqual(claude.map(\.trailing), Array(repeating: "Updated 12 min ago", count: 3))
        XCTAssertEqual(claude[0].fraction, 0.424, accuracy: 0.0001)

        XCTAssertEqual(UsageSection.rows(usage[1], format: format).map(\.trailing), ["Resets in 3h 0m", "Resets Mon 08:00"])
        XCTAssertEqual(UsageSection.rows(usage[3], format: format).map(\.trailing), ["Resets 1 Oct", "Resets 1 Oct"])
        XCTAssertEqual(UsageSection.rows(nil, format: format), [])
        var bare = usage[1]
        bare.meters = [Meter(id: "x", label: "X", usedPct: 140)]
        XCTAssertEqual(UsageSection.rows(bare, format: format).map(\.trailing), [""])
        XCTAssertEqual(UsageSection.rows(bare, format: format)[0].fraction, 1)
    }

    /// agy's wire carries its native Gemini quota only (2026-09-26): the label
    /// and the usage panel show Gemini, never the Claude & GPT models agy also offers.
    func testAgyShowsNativeGeminiUsageOnly() {
        let agy = usage[2]
        XCTAssertEqual(agy.agent, .agy)
        XCTAssertEqual(UsageSection.rows(agy, format: format).map(\.label), ["Gemini 5h", "Gemini weekly"])
        let l = MenuLabel.make(activeCount: 0, connected: true, enabled: [.agy], usage: usage, compact: false, format: format)
        XCTAssertEqual(texts(l), ["agy 6%"])
        XCTAssertFalse(l.segments[0].tooltip.contains("Claude"), l.segments[0].tooltip)
    }

    func testUsageLevelThresholds() {
        func level(_ f: Double) -> UsageSection.Level {
            UsageSection.Row(id: "x", label: "X", used: "", fraction: f, trailing: "").level
        }
        XCTAssertEqual(level(0), .normal)
        XCTAssertEqual(level(0.69), .normal)
        XCTAssertEqual(level(0.7), .warning)
        XCTAssertEqual(level(0.89), .warning)
        XCTAssertEqual(level(0.9), .critical)
        XCTAssertEqual(level(1), .critical)
    }

    func testActiveBadgeFollowsLiveAgents() {
        func badge(active: Int, connected: Bool, needsYou: Int = 0) -> MenuLabel.Badge {
            MenuLabel.make(activeCount: active, connected: connected, needsYou: needsYou, enabled: [.claude], usage: [],
                           compact: false, format: format).badge
        }
        XCTAssertEqual(badge(active: 1, connected: true), .green)
        XCTAssertEqual(badge(active: 0, connected: true), .none, "nothing running")
        XCTAssertEqual(badge(active: 3, connected: false), .none, "offline shows no dot (user decision 2026-09-25)")
        XCTAssertEqual(badge(active: 0, connected: false), .none)
        XCTAssertEqual(badge(active: 1, connected: true, needsYou: 1), .yellow, "needs you wins over live")
        XCTAssertEqual(badge(active: 0, connected: false, needsYou: 2), .yellow, "cached needs-you still shows")
    }
}
