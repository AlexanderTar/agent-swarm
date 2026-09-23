import AppKit
import XCTest
@testable import SwarmBarKit

final class IconAssetsTests: XCTestCase {
    let icons = Fixture.repoRoot.appendingPathComponent("assets/icons")
    let names = ["claude", "codex", "agy", "cursor", "muse", "swarm"]

    func testEveryIconLoadsAt24Points() throws {
        for name in names {
            let url = icons.appendingPathComponent("\(name).svg")
            let image = try XCTUnwrap(NSImage(contentsOf: url), name)
            XCTAssertEqual(image.size, NSSize(width: 24, height: 24), name)
        }
        let licences = try String(contentsOf: icons.appendingPathComponent("LICENSES.md"), encoding: .utf8)
        for needle in ["CC0 1.0 Universal", "MIT, Copyright (c) 2023 LobeHub", "normalize-icons.pl"] {
            XCTAssertTrue(licences.contains(needle), needle)
        }
    }

    func testIconsAreAlreadyNormalised() async throws {
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: tmp, withIntermediateDirectories: true)
        let copies = try names.map { name -> URL in
            let dst = tmp.appendingPathComponent("\(name).svg")
            try FileManager.default.copyItem(at: icons.appendingPathComponent("\(name).svg"), to: dst)
            return dst
        }
        let script = Fixture.packageDir.appendingPathComponent("scripts/normalize-icons.pl").path
        let result = await ProcessRunner().run("/usr/bin/perl", [script] + copies.map(\.path))
        XCTAssertEqual(result.status, 0, result.output)
        for (name, copy) in zip(names, copies) {
            XCTAssertEqual(try Data(contentsOf: copy), try Data(contentsOf: icons.appendingPathComponent("\(name).svg")), name)
        }

        let packed = tmp.appendingPathComponent("packed.svg")
        try Data(#"<svg width="1em" style="x" viewBox="0 0 24 24"><path d="M0 0a6.1 6.1 0 013.04-.41z"/></svg>"#.utf8).write(to: packed)
        _ = await ProcessRunner().run("/usr/bin/perl", [script, packed.path])
        XCTAssertEqual(try String(contentsOf: packed, encoding: .utf8),
                       #"<svg width="24" height="24" viewBox="0 0 24 24"><path d="M0 0a6.1 6.1 0 0 1 3.04 -.41z"/></svg>"#)
    }
}
