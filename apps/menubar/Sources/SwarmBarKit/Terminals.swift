import Foundation

/// Opens agent terminals in Ghostty (L17, P0-6) and checks tmux sessions directly,
/// so terminal buttons keep working while the daemon is down (§16.2).
@MainActor
public final class Terminals {
    public enum Outcome: String, Equatable, Sendable {
        case focused, opened, fallback, rejected
    }

    public static let tmuxCandidates = ["/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"]
    public static let socket = "swarm"

    private let runner: CommandRunning
    private let script: ScriptRunning
    private let ghosttyPIDs: @MainActor () -> Set<Int32>
    public let tmuxPath: String
    /// Agent name → Ghostty processes alive when the fallback ran. While any of them lives,
    /// that agent skips the AppleScript lookup (L17).
    public private(set) var fallbackAgents: [String: Set<Int32>] = [:]

    public init(runner: CommandRunning, script: ScriptRunning,
                ghosttyPIDs: @escaping @MainActor () -> Set<Int32>,
                fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }) {
        self.runner = runner
        self.script = script
        self.ghosttyPIDs = ghosttyPIDs
        tmuxPath = Self.tmuxCandidates.first(where: fileExists) ?? Self.tmuxCandidates[0]
    }

    public static func isValidName(_ name: String) -> Bool {
        name.count <= Kebab.maxName && name.range(of: "^[a-z0-9]+(-[a-z0-9]+)*$", options: .regularExpression) != nil
    }

    /// tmux resolves a bare `-t name` by exact match, then prefix, then fnmatch, so `login`
    /// would find `login-2`. `=name` is the exact-match form; a shell reads it literally.
    public static func exactTarget(_ name: String) -> String { "=" + name }

    public static func appleScriptString(_ s: String) -> String {
        "\"" + s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\""
    }

    /// Focus the terminal titled `swarm:<name>`, otherwise open a new window attached to the session.
    /// "ends with" still matches a prefixed title and never mixes up `name` and `name-2`.
    public static func focusOrOpenScript(name: String, tmuxPath: String) -> String {
        """
        set target to \(appleScriptString("swarm:" + name))
        set cmd to \(appleScriptString("\(tmuxPath) -L \(socket) attach -t \(exactTarget(name))"))
        tell application "Ghostty"
            repeat with w in windows
                repeat with t in terminals of w
                    if name of t ends with target then
                        focus t
                        activate
                        return "focused"
                    end if
                end repeat
            end repeat
            set cfg to new surface configuration
            set command of cfg to cmd
            if (count of windows) > 0 then
                new tab in window 1 with configuration cfg
            else
                new window with configuration cfg
            end if
            activate
            return "opened"
        end tell
        """
    }

    public func fallbackArgs(_ name: String) -> [String] {
        ["-na", "Ghostty", "--args", "-e", tmuxPath, "-L", Self.socket, "attach", "-t", Self.exactTarget(name)]
    }

    /// `tmux -L swarm has-session -t =<name>`.
    public func hasSession(_ name: String) async -> Bool {
        guard Self.isValidName(name) else { return false }
        return await runner.run(tmuxPath, ["-L", Self.socket, "has-session", "-t", Self.exactTarget(name)]).status == 0
    }

    public func open(_ name: String) async -> Outcome {
        guard Self.isValidName(name) else { return .rejected }
        if let pids = fallbackAgents[name] {
            if !pids.isDisjoint(with: ghosttyPIDs()) { return await runFallback(name) }
            fallbackAgents[name] = nil
        }
        do {
            let out = try await script.run(Self.focusOrOpenScript(name: name, tmuxPath: tmuxPath))
            return out.trimmingCharacters(in: .whitespacesAndNewlines) == "focused" ? .focused : .opened
        } catch {
            return await runFallback(name)
        }
    }

    private func runFallback(_ name: String) async -> Outcome {
        _ = await runner.run("/usr/bin/open", fallbackArgs(name))
        fallbackAgents[name, default: []].formUnion(ghosttyPIDs())
        return .fallback
    }
}
