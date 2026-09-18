import Foundation
import XCTest
@testable import SwarmBarKit

/// Paths shared by all tests. Fixtures are read from disk through #filePath,
/// so Package.swift needs no test resources.
enum Fixture {
    static let testsDir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    static let dir = testsDir.appendingPathComponent("Fixtures")
    static let repoRoot = testsDir.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    static let packageDir = testsDir.deletingLastPathComponent()

    static func data(_ name: String) throws -> Data {
        try Data(contentsOf: dir.appendingPathComponent(name))
    }

    static func decode<T: Decodable>(_ name: String, as type: T.Type = T.self) throws -> T {
        try SwarmJSON.decode(type, from: data(name))
    }

    static func json(_ data: Data) throws -> NSObject {
        try JSONSerialization.jsonObject(with: data) as! NSObject
    }
}

/// 2026-09-17 13:32:00 UTC, the "now" every fixture is written against.
let fixtureNow = Date(timeIntervalSince1970: 1_789_651_920)

extension Timestamp {
    static func minutes(_ m: Double, from now: Date = fixtureNow) -> Timestamp {
        Timestamp(now.addingTimeInterval(m * 60))
    }
}
