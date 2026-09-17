import Foundation

/// Time and number formatting for the menubar. Callers pass `now` and the time zone,
/// so tests are deterministic. Day and month names are always English (en_US_POSIX).
public struct Format: Sendable {
    public var now: Date
    public var timeZone: TimeZone

    public init(now: Date = Date(), timeZone: TimeZone = .current) {
        self.now = now
        self.timeZone = timeZone
    }

    /// "1m", "12m", "2h", "3d" (Settings ages, "Scanned 2h ago").
    public func ageCompact(_ date: Date) -> String {
        let s = max(0, now.timeIntervalSince(date))
        if s < 3600 { return "\(max(1, Int((s / 60).rounded())))m" }
        if s < 86400 { return "\(Int(s / 3600))h" }
        return "\(Int(s / 86400))d"
    }

    /// "12 min ago", "2 h ago", "3 d ago" (stale usage, tooltips).
    public func ago(_ date: Date) -> String {
        let s = max(0, now.timeIntervalSince(date))
        if s < 3600 { return "\(max(1, Int((s / 60).rounded()))) min ago" }
        if s < 86400 { return "\(Int(s / 3600)) h ago" }
        return "\(Int(s / 86400)) d ago"
    }

    /// "42%": rounded and clamped to 0–100.
    public static func percent(_ used: Double) -> String {
        "\(Int(min(100, max(0, used)).rounded()))%"
    }

    /// Under 24 h: "Resets in 2h 10m" / "Resets in 45m". Under 7 days: "Resets Mon 09:00". Later: "Resets 1 Oct".
    public func resets(_ date: Date) -> String {
        let s = max(0, date.timeIntervalSince(now))
        if s < 86400 {
            let minutes = Int((s / 60).rounded(.up))
            let h = minutes / 60, m = minutes % 60
            return h > 0 ? "Resets in \(h)h \(m)m" : "Resets in \(m)m"
        }
        if s < 7 * 86400 { return "Resets \(pattern("EEE HH:mm", date))" }
        return "Resets \(dayMonth(date))"
    }

    /// "1 Oct".
    public func dayMonth(_ date: Date) -> String { pattern("d MMM", date) }

    /// "14:32".
    public func clock(_ date: Date) -> String { pattern("HH:mm", date) }

    private func pattern(_ format: String, _ date: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = format
        return f.string(from: date)
    }
}
