import Foundation
import XCTest
@testable import SwarmBarKit

final class FormatTests: XCTestCase {
    let f = Format(now: fixtureNow, timeZone: TimeZone(identifier: "UTC")!)

    func testAges() {
        XCTAssertEqual(f.ageCompact(fixtureNow.addingTimeInterval(-30)), "1m")
        XCTAssertEqual(f.ageCompact(fixtureNow.addingTimeInterval(-90)), "1m", "floor, not round: an age must never overstate")
        XCTAssertEqual(f.ageCompact(fixtureNow.addingTimeInterval(-12 * 60)), "12m")
        XCTAssertEqual(f.ageCompact(fixtureNow.addingTimeInterval(-125 * 60)), "2h")
        XCTAssertEqual(f.ageCompact(fixtureNow.addingTimeInterval(-3 * 86400)), "3d")
        XCTAssertEqual(f.ago(fixtureNow.addingTimeInterval(-12 * 60)), "12 min ago")
        XCTAssertEqual(f.ago(fixtureNow.addingTimeInterval(10)), "1 min ago")
        XCTAssertEqual(f.ago(fixtureNow.addingTimeInterval(-61 * 60)), "1 h ago")
        XCTAssertEqual(f.ago(fixtureNow.addingTimeInterval(-50 * 3600)), "2 d ago")
    }

    /// A timestamp that was never set is 0 (contracts W2), which is 1970 — an age from it reads "20700d".
    func testAgeLineFallsBackForATimestampThatWasNeverSet() {
        XCTAssertEqual(f.ageLine(Timestamp(ms: 0), never: "Never scanned") { "Scanned \($0) ago" }, "Never scanned")
        XCTAssertEqual(f.ageLine(Timestamp(fixtureNow.addingTimeInterval(-7200)), never: "Never scanned") { "Scanned \($0) ago" },
                       "Scanned 2h ago")
        XCTAssertEqual(f.ageLine(Timestamp(ms: -1), never: "Never scanned") { "Scanned \($0) ago" }, "Never scanned",
                       "the wire is a trust boundary: anything <= 0 is never")
        XCTAssertEqual(f.ageLine(Timestamp(ms: .min), never: "Never scanned") { "Scanned \($0) ago" }, "Never scanned")
        XCTAssertEqual(f.ageLine(Timestamp(ms: 1), never: "Never scanned") { "Scanned \($0) ago" }, "Scanned 20713d ago",
                       "a positive timestamp is still an age, however old")
        XCTAssertEqual(f.agoLine(Timestamp(ms: 0), never: "Never updated") { "Updated \($0)" }, "Never updated")
        XCTAssertEqual(f.agoLine(Timestamp(ms: -1), never: "Never updated") { "Updated \($0)" }, "Never updated")
        XCTAssertEqual(f.agoLine(Timestamp(fixtureNow.addingTimeInterval(-720)), never: "Never updated") { "Updated \($0)" },
                       "Updated 12 min ago")
    }

    func testPercent() {
        XCTAssertEqual(Format.percent(42.4), "42%")
        XCTAssertEqual(Format.percent(99.5), "100%")
        XCTAssertEqual(Format.percent(130), "100%")
        XCTAssertEqual(Format.percent(-3), "0%")
    }

    func testResetsAndDates() {
        XCTAssertEqual(f.resets(fixtureNow.addingTimeInterval(130 * 60)), "Resets in 2h 10m")
        XCTAssertEqual(f.resets(fixtureNow.addingTimeInterval(45 * 60)), "Resets in 45m")
        XCTAssertEqual(f.resets(fixtureNow.addingTimeInterval(-60)), "Resets in 0m")
        // Monday 2026-09-21 08:00 UTC
        XCTAssertEqual(f.resets(Date(timeIntervalSince1970: 1_789_977_600)), "Resets Mon 08:00")
        // 2026-10-01 00:00 UTC
        XCTAssertEqual(f.resets(Date(timeIntervalSince1970: 1_790_812_800)), "Resets 1 Oct")
        XCTAssertEqual(f.dayMonth(Date(timeIntervalSince1970: 1_790_812_800)), "1 Oct")
        XCTAssertEqual(f.clock(fixtureNow), "13:32")
        let london = Format(now: fixtureNow, timeZone: TimeZone(identifier: "Europe/London")!)
        XCTAssertEqual(london.clock(fixtureNow), "14:32")
    }
}
