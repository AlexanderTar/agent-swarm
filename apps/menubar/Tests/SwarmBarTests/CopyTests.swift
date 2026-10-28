import XCTest
@testable import SwarmBarKit

final class CopyTests: XCTestCase {
    func testAppTitle() {
        XCTAssertEqual(Copy.appTitle, "Swarm")
    }
}
