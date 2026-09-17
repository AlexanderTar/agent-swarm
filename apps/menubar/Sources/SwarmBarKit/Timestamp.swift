import Foundation

/// A wire timestamp: integer milliseconds since the epoch (§5, contracts W2). Nothing else is accepted.
public struct Timestamp: Codable, Sendable, Hashable, Comparable {
    public var ms: Int64

    public init(ms: Int64) { self.ms = ms }
    public init(_ date: Date) { ms = Int64((date.timeIntervalSince1970 * 1000).rounded()) }

    public var date: Date { Date(timeIntervalSince1970: Double(ms) / 1000) }

    public init(from decoder: Decoder) throws {
        ms = try decoder.singleValueContainer().decode(Int64.self)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(ms)
    }

    public static func < (a: Timestamp, b: Timestamp) -> Bool { a.ms < b.ms }
}

public enum SwarmJSON {
    public static func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        try JSONDecoder().decode(type, from: data)
    }

    public static func encode<T: Encodable>(_ value: T) throws -> Data {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try e.encode(value)
    }
}
