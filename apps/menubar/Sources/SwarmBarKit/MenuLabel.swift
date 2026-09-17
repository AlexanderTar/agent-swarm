import Foundation

/// The menu bar item text (§16.1): `[swarm] 6   ✳ 42%   ◎ 18%   ▲ 63%   ⌘ 27%M`.
public struct MenuLabel: Equatable, Sendable {
    public struct Segment: Equatable, Sendable {
        public var agent: AgentKind
        /// "42%", "27%M", "--", or "" in compact mode.
        public var text: String
        public var dimmed: Bool
        public var tooltip: String
    }

    /// Active sessions, or "?" while the daemon is down.
    public var count: String
    public var segments: [Segment]
    public var compact: Bool

    /// Width reserved for each value, so the item never shifts: "100%" or "100%M".
    public static let widestValue = "100%"
    public static let widestMonthlyValue = "100%M"

    public static func make(activeCount: Int, connected: Bool, enabled: [AgentKind], usage: [UsageSnapshot],
                            compact: Bool, format: Format) -> MenuLabel {
        let agents = AgentKind.selectable.filter(enabled.contains)
        let segments = agents.map { kind -> Segment in
            let snap = usage.first { $0.agent == kind }
            guard let snap, let head = snap.headline else {
                return Segment(agent: kind, text: compact ? "" : "--", dimmed: false, tooltip: Copy.emptyUsage)
            }
            let monthly = head.window == "monthly"
            let text = compact ? "" : Format.percent(head.usedPct) + (monthly ? "M" : "")
            let tooltip: String
            if snap.stale {
                tooltip = "Last updated \(format.ago(snap.fetchedAt.date))."
            } else if monthly {
                tooltip = "\(head.label) usage" + (head.resetsAt.map { " · resets \(format.dayMonth($0.date))" } ?? "")
            } else {
                let same = [head] + snap.meters.filter { $0.window == head.window && $0.id != head.id }
                tooltip = same.map { "\($0.label) \(Format.percent($0.usedPct))" }.joined(separator: " · ")
            }
            return Segment(agent: kind, text: text, dimmed: snap.stale, tooltip: tooltip)
        }
        return MenuLabel(count: connected ? "\(activeCount)" : "?", segments: segments, compact: compact)
    }
}

/// Automatic compact mode when macOS hides the item for lack of space (§16.1 M5).
/// The override lives on this Mac only; the daemon's `menubar_compact` stays the user's choice.
public struct CompactSwitch: Equatable, Sendable, Codable {
    public var autoOverride = false
    /// Set when the user turns Compact off, cleared once the item has been visible again.
    public var suppressed = false
    /// "Switched to compact to fit the menu bar." waits for the next popover.
    public var notePending = false

    public init() {}

    public func effective(setting: Bool) -> Bool { setting || autoOverride }

    public mutating func labelVisible(_ visible: Bool, setting: Bool) {
        if visible {
            suppressed = false
        } else if !setting && !autoOverride && !suppressed {
            autoOverride = true
            notePending = true
        }
    }

    /// The user flipped the Compact checkbox (which shows the effective value).
    public mutating func userSet(_ on: Bool) {
        autoOverride = false
        if !on { suppressed = true }
    }

    public mutating func noteShown() { notePending = false }
}

/// Usage section rows (§16.2).
public enum UsageSection {
    public struct Row: Equatable, Sendable, Identifiable {
        public var id: String
        public var label: String
        /// "42% used"
        public var used: String
        public var fraction: Double
        /// "Resets in 2h 10m", "Resets Mon 09:00", "Cycle ends 1 Oct" or "Updated 12 min ago".
        public var trailing: String
    }

    /// Enabled agents in settings order.
    public static func pickerAgents(enabled: [AgentKind]) -> [AgentKind] {
        AgentKind.selectable.filter(enabled.contains)
    }

    /// Keeps the current choice while it's enabled; otherwise Claude, otherwise the first enabled agent.
    public static func selection(current: AgentKind?, enabled: [AgentKind]) -> AgentKind? {
        let agents = pickerAgents(enabled: enabled)
        if let current, agents.contains(current) { return current }
        return agents.contains(.claude) ? .claude : agents.first
    }

    /// Empty means "Usage unavailable." with [Retry].
    public static func rows(_ snap: UsageSnapshot?, format: Format) -> [Row] {
        guard let snap else { return [] }
        return snap.meters.map { m in
            let trailing: String
            if snap.stale {
                trailing = "Updated \(format.ago(snap.fetchedAt.date))"
            } else if let r = m.resetsAt {
                trailing = m.window == "monthly" ? "Cycle ends \(format.dayMonth(r.date))" : format.resets(r.date)
            } else {
                trailing = ""
            }
            return Row(id: m.id, label: m.label, used: "\(Format.percent(m.usedPct)) used",
                       fraction: min(1, max(0, m.usedPct / 100)), trailing: trailing)
        }
    }
}
