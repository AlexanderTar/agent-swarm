import Foundation
import XCTest
@testable import SwarmBarKit

final class KebabTests: XCTestCase {
    struct Golden: Decodable {
        struct Case: Decodable { let input: String; let max: Int; let output: String?; let error: Bool? }
        let error_message: String
        let cases: [Case]
    }

    func testMatchesTheSharedGoldenCases() throws {
        let url = Fixture.repoRoot.appendingPathComponent("testdata/kebab_cases.json")
        let golden = try JSONDecoder().decode(Golden.self, from: Data(contentsOf: url))
        XCTAssertGreaterThanOrEqual(golden.cases.count, 19)
        for c in golden.cases {
            let got = Kebab.make(c.input, max: c.max)
            if c.error == true {
                XCTAssertNil(got, "\(c.input) should be an error")
            } else {
                XCTAssertEqual(got, c.output, "\(c.input) max \(c.max)")
            }
        }
        XCTAssertEqual(golden.error_message, Kebab.emptyNameMessage)
        XCTAssertEqual(Kebab.emptyNameMessage, "Enter a name containing a letter or number.")
    }

    func testDefaultMaxIs48() {
        XCTAssertEqual(Kebab.make(String(repeating: "b", count: 60))?.count, 48)
        XCTAssertEqual(Kebab.make("Investigate login crash"), "investigate-login-crash")
    }
}
