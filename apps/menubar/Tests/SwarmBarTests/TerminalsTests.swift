import Foundation
import XCTest
@testable import SwarmBarKit

final class FakeRunner: CommandRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [String] = []
    var status: Int32 = 0
    var calls: [String] { lock.withLock { _calls } }

    func run(_ executable: String, _ args: [String]) async -> CommandResult {
        lock.withLock { _calls.append(([executable] + args).joined(separator: " ")) }
        return CommandResult(status: status)
    }
}

final class FakeScript: ScriptRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var _sources: [String] = []
    var reply: Result<String, Error> = .success("opened")
    var sources: [String] { lock.withLock { _sources } }

    func run(_ source: String) async throws -> String {
        lock.withLock { _sources.append(source) }
        return try reply.get()
    }
}

struct Denied: Error {}

@MainActor
final class TerminalsTests: XCTestCase {
    var runner = FakeRunner()
    var script = FakeScript()
    var pids: Set<Int32> = [100]

    private func make(exists: Set<String> = ["/usr/local/bin/tmux"]) -> Terminals {
        Terminals(runner: runner, script: script, ghosttyPIDs: { [unowned self] in self.pids }, fileExists: exists.contains)
    }

    func testScriptTextMatchesPhaseZero() {
        let s = Terminals.focusOrOpenScript(name: "login-form-coder", tmuxPath: "/opt/homebrew/bin/tmux")
        XCTAssertTrue(s.contains(#"set target to " login-form-coder""#))
        XCTAssertTrue(s.contains(#"set cmd to "/opt/homebrew/bin/tmux -L swarm attach -t =login-form-coder""#))
        XCTAssertTrue(s.contains("tell application \"Ghostty\""))
        XCTAssertTrue(s.contains("if name of t ends with target then"))
        XCTAssertTrue(s.contains("focus t"))
        XCTAssertTrue(s.contains("set cfg to new surface configuration"))
        XCTAssertTrue(s.contains("set command of cfg to cmd"))
        XCTAssertTrue(s.contains("new tab in window 1 with configuration cfg"))
        XCTAssertTrue(s.contains("new window with configuration cfg"))
    }

    func testEscapingAndNameValidation() {
        XCTAssertEqual(Terminals.appleScriptString(#"a"b\c"#), #""a\"b\\c""#)
        XCTAssertTrue(Terminals.isValidName("login-form-coder"))
        for bad in ["", "Login", "a\"b", "a b", "-a", "a--b", "a;rm", String(repeating: "a", count: 49)] {
            XCTAssertFalse(Terminals.isValidName(bad), bad)
        }
    }

    func testTmuxPathAndHasSession() async {
        XCTAssertEqual(make().tmuxPath, "/usr/local/bin/tmux")
        XCTAssertEqual(make(exists: []).tmuxPath, "/opt/homebrew/bin/tmux")
        let t = make()
        let alive = await t.hasSession("login-form-coder")
        runner.status = 1
        let dead = await t.hasSession("login-form-coder")
        let invalid = await t.hasSession("bad name")
        XCTAssertEqual([alive, dead, invalid], [true, false, false])
        XCTAssertEqual(runner.calls, [
            "/usr/local/bin/tmux -L swarm has-session -t =login-form-coder",
            "/usr/local/bin/tmux -L swarm has-session -t =login-form-coder",
        ])
    }

    /// tmux resolves `-t name` by exact match, then prefix, then fnmatch, so a finished
    /// `login-form-coder` would otherwise find and attach to a live `login-form-coder-2`.
    func testTmuxTargetsAreExactSoSiblingSessionsNeverMatch() async {
        final class TmuxRunner: CommandRunning, @unchecked Sendable {
            let live: Set<String>
            init(live: Set<String>) { self.live = live }
            func run(_ executable: String, _ args: [String]) async -> CommandResult {
                guard let target = args.last else { return CommandResult(status: 1) }
                if target.hasPrefix("=") { return CommandResult(status: live.contains(String(target.dropFirst())) ? 0 : 1) }
                return CommandResult(status: live.contains(where: { $0.hasPrefix(target) }) ? 0 : 1)
            }
        }
        let t = Terminals(runner: TmuxRunner(live: ["login-form-coder-2"]), script: script,
                          ghosttyPIDs: { [] }, fileExists: { $0 == "/usr/local/bin/tmux" })
        let sibling = await t.hasSession("login-form-coder")
        let itself = await t.hasSession("login-form-coder-2")
        XCTAssertEqual([sibling, itself], [false, true])
        XCTAssertEqual(t.fallbackArgs("login-form-coder").suffix(2).joined(separator: " "), "-t =login-form-coder")
    }

    func testFocusOrOpenThroughAppleScript() async {
        let t = make()
        script.reply = .success("focused\n")
        let focused = await t.open("login-form-coder")
        script.reply = .success("opened")
        let opened = await t.open("login-form-coder")
        let rejected = await t.open("bad\"name")
        XCTAssertEqual([focused, opened, rejected], [.focused, .opened, .rejected])
        XCTAssertEqual(script.sources.count, 2)
        XCTAssertEqual(runner.calls, [])
    }

    func testFallbackRunsWhenTheScriptFailsAndSkipsLookupsUntilGhosttyRestarts() async {
        let t = make()
        script.reply = .failure(Denied())
        let first = await t.open("login-form-coder")
        XCTAssertEqual(first, .fallback)
        XCTAssertEqual(runner.calls, ["/usr/bin/open -na Ghostty --args -e /usr/local/bin/tmux -L swarm attach -t =login-form-coder"])
        XCTAssertEqual(t.fallbackAgents["login-form-coder"], [100])

        script.reply = .success("focused")
        pids = [100, 200]
        let second = await t.open("login-form-coder")
        XCTAssertEqual(second, .fallback, "lookup skipped while the same Ghostty runs")
        XCTAssertEqual(script.sources.count, 1)
        XCTAssertEqual(t.fallbackAgents["login-form-coder"], [100, 200])

        let other = await t.open("other-agent")
        XCTAssertEqual(other, .focused, "other agents still use AppleScript")

        pids = [300]
        let afterRestart = await t.open("login-form-coder")
        XCTAssertEqual(afterRestart, .focused)
        XCTAssertNil(t.fallbackAgents["login-form-coder"])
        XCTAssertEqual(runner.calls.count, 2)
    }

    func testProcessRunner() async {
        let r = ProcessRunner(timeout: 0.3)
        let ok = await r.run("/bin/echo", ["hi"])
        XCTAssertEqual(ok, CommandResult(status: 0, output: "hi\n"))
        let fail = await r.run("/usr/bin/false", [])
        XCTAssertEqual(fail.status, 1)
        let missing = await r.run("/nonexistent/tool", [])
        XCTAssertEqual(missing.status, 127)
        let started = Date()
        let slow = await r.run("/bin/sleep", ["5"])
        XCTAssertNotEqual(slow.status, 0)
        XCTAssertLessThan(Date().timeIntervalSince(started), 3)
    }
}
