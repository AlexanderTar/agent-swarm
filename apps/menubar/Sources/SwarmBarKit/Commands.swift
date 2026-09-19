import Foundation

public struct CommandResult: Equatable, Sendable {
    public var status: Int32
    public var output: String
    public init(status: Int32, output: String = "") { self.status = status; self.output = output }
}

/// Runs a program without a shell. Tests inject a fake that records argv.
public protocol CommandRunning: Sendable {
    func run(_ executable: String, _ args: [String]) async -> CommandResult
}

public struct ProcessRunner: CommandRunning {
    public var timeout: TimeInterval

    public init(timeout: TimeInterval = 10) { self.timeout = timeout }

    /// Blocking work runs on a GCD thread, never on the Swift concurrency pool.
    public func run(_ executable: String, _ args: [String]) async -> CommandResult {
        let timeout = self.timeout
        return await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let p = Process()
                p.executableURL = URL(fileURLWithPath: executable)
                p.arguments = args
                let pipe = Pipe()
                p.standardOutput = pipe
                p.standardError = pipe
                p.standardInput = FileHandle.nullDevice
                do {
                    try p.run()
                } catch {
                    continuation.resume(returning: CommandResult(status: 127, output: "\(error)"))
                    return
                }
                DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
                    if p.isRunning { p.terminate() }
                }
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                continuation.resume(returning: CommandResult(status: p.terminationStatus,
                                                             output: String(decoding: data, as: UTF8.self)))
            }
        }
    }
}

/// Runs AppleScript. The app uses NSAppleScript (Automation permission, §19 README); tests use a fake.
public protocol ScriptRunning: Sendable {
    func run(_ source: String) async throws -> String
}
