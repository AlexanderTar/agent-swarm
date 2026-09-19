import Foundation
import XCTest

final class InfoPlistTests: XCTestCase {
    func testMenubarAppKeys() throws {
        let url = Fixture.packageDir.appendingPathComponent("Resources/Info.plist")
        let plist = try XCTUnwrap(NSDictionary(contentsOf: url) as? [String: Any])
        XCTAssertEqual(plist["CFBundleIdentifier"] as? String, "dev.swarm.menubar")
        XCTAssertEqual(plist["CFBundleExecutable"] as? String, "Swarm")
        XCTAssertEqual(plist["CFBundleName"] as? String, "Swarm")
        XCTAssertEqual(plist["CFBundlePackageType"] as? String, "APPL")
        XCTAssertEqual(plist["LSMinimumSystemVersion"] as? String, "14.0")
        XCTAssertEqual(plist["LSUIElement"] as? Bool, true)
        XCTAssertEqual(plist["NSAppleEventsUsageDescription"] as? String, "Swarm opens agent terminals in Ghostty.")
    }
}
