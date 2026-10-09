import Foundation

/// The pane preview header's context segment: "120K / 1M" plus, when the window is known, a bar fraction
/// and tint. nil without a sample, so the segment is omitted.
public struct ContextReadout: Sendable, Equatable {
    public enum Tint: Sendable, Equatable { case secondary, orange, red }

    public let text: String
    /// 0...1, nil when the window is unknown (no bar).
    public let fraction: Double?
    public let a11y: String

    public init?(tokens: Int?, window: Int?) {
        guard let tokens else { return nil }
        let window = window.flatMap { $0 > 0 ? $0 : nil }
        text = Format.contextUsage(tokens, window)
        fraction = window.map { min(1, max(0, Double(tokens) / Double($0))) }
        a11y = window.map { "context \(Self.spoken(tokens)) of \(Self.spoken($0)) tokens" }
            ?? "context \(Self.spoken(tokens)) tokens"
    }

    /// `.secondary` below 60%, `.orange` from 60% to 85%, `.red` above.
    public var tint: Tint {
        guard let fraction else { return .secondary }
        return fraction > 0.85 ? .red : fraction >= 0.6 ? .orange : .secondary
    }

    /// "120 thousand", "1 million", "1.2 million": the same rounding as `Format.contextUsage`, in words.
    private static func spoken(_ n: Int) -> String {
        Format.contextUsage(n, nil)
            .replacingOccurrences(of: "K", with: " thousand")
            .replacingOccurrences(of: "M", with: " million")
    }
}
