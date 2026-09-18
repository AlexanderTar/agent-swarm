import Foundation
import XCTest
@testable import SwarmBarKit

final class RepoPickerTests: XCTestCase {
    let home = "/Users/alex"
    var repos = ReposResponse()

    override func setUpWithError() throws {
        repos = try Fixture.decode("repos.json")
    }

    func testFoldersAndSubtitles() {
        XCTAssertEqual(RepoPicker.parentFolder("/Users/alex/GitHub/endurio-chat", home: home), "~/GitHub")
        XCTAssertEqual(RepoPicker.parentFolder("/Users/alex/notes", home: home), "~")
        XCTAssertEqual(RepoPicker.parentFolder("/opt/src/x", home: home), "/opt/src")
        XCTAssertEqual(RepoPicker.parentFolder("x", home: home), "/")
        XCTAssertEqual(RepoPicker.subtitle(repos.recent[0], home: home), "~/GitHub · EndurioApp")
        XCTAssertEqual(RepoPicker.subtitle(repos.all[4], home: home), "~/Code")
        XCTAssertEqual(RepoPicker.note(repos.all[4]), "Repository is unavailable. Choose another location.")
        XCTAssertEqual(RepoPicker.note(repos.all[1]), "Has uncommitted changes. The orchestrator works in its own worktree.")
        XCTAssertNil(RepoPicker.note(repos.all[0]))
    }

    func testSectionOrder() {
        let s = RepoPicker.sections(repos)
        XCTAssertEqual(s.map(\.title), ["Recent", "endurio", "AlexanderTar", "EndurioApp", "All"])
        XCTAssertEqual(s[1].repos.map(\.name), ["endurio-chat", "endurio-app", "endurio-landing"])
        XCTAssertEqual(s[1].groupName, "endurio")
        XCTAssertNil(s[0].groupName)
        XCTAssertEqual(RepoPicker.sections(ReposResponse()), [])

        var merged = repos
        merged.groups.append(RepoGroup(name: "endurio", source: "code_workspace", repos: [repos.all[0]]))
        merged.groups.append(RepoGroup(name: "Zeta", source: "remote_owner", repos: [repos.all[0], repos.all[0]]))
        let m = RepoPicker.sections(merged)
        XCTAssertEqual(m.map(\.title), ["Recent", "endurio", "AlexanderTar", "EndurioApp", "Zeta", "All"])
        XCTAssertEqual(m[1].repos.map(\.name), ["endurio-chat", "endurio-app", "endurio-landing", "agent-swarm"])
        XCTAssertEqual(m[4].repos.count, 1)
    }

    func testSelection() {
        let known = RepoPicker.known(repos)
        XCTAssertEqual(known.map(\.id), ["repo_chat", "repo_landing", "repo_swarm", "repo_app", "repo_gone"])
        XCTAssertEqual(RepoPicker.toggle([], "repo_chat"), ["repo_chat"])
        XCTAssertEqual(RepoPicker.toggle(["repo_chat", "repo_app"], "repo_chat"), ["repo_app"])
        XCTAssertEqual(RepoPicker.selectAll(["repo_app"], [repos.all[2], repos.all[1], repos.all[4]]), ["repo_app", "repo_chat"])
        XCTAssertEqual(RepoPicker.selectedLine(["repo_chat", "repo_landing", "repo_x"], known: known),
                       "Selected: endurio-chat, endurio-landing, repo_x")
        XCTAssertEqual(RepoPicker.selectedLine([], known: known), "")
    }

    func testScanLine() {
        let format = Format(now: fixtureNow)
        XCTAssertEqual(RepoPicker.scanLine(repos, format: format), "Scanned 2h ago")
        // No scan yet: scanned_at is 0 (repos/service.go ScannedAt is zero), which is not an age.
        repos.scannedAt = Timestamp(ms: 0)
        XCTAssertEqual(RepoPicker.scanLine(repos, format: format), "Never scanned")
        repos.scanning = true
        XCTAssertEqual(RepoPicker.scanLine(repos, format: format), "Scanning your home folder…")
    }
}
