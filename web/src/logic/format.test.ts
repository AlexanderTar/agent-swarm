import { describe, expect, it } from "vitest";
import { ageAgo, ageCompact, ageLine, formatCost, formatDuration, formatTime, formatTokens, sha7, truncate } from "./format";

const NOW = 1_800_000_000_000;
const MIN = 60_000;

describe("format", () => {
  it("formats compact ages", () => {
    expect(ageCompact(NOW - 30_000, NOW)).toBe("1m");
    expect(ageCompact(NOW - 12 * MIN, NOW)).toBe("12m");
    expect(ageCompact(NOW - 125 * MIN, NOW)).toBe("2h");
    expect(ageCompact(NOW - 3 * 24 * 60 * MIN, NOW)).toBe("3d");
    expect(ageCompact(NOW - 59.5 * MIN, NOW)).toBe("59m");
  });

  it("builds an age sentence, or the caller's copy when there is no usable timestamp", () => {
    const scanned = (age: string) => `Scanned ${age} ago`;
    expect(ageLine(NOW - 125 * MIN, "Never scanned", scanned, NOW)).toBe("Scanned 2h ago");
    // Trust boundary: the wire can send 0, a negative, or junk that decodes to NaN.
    for (const ms of [0, -1, -NOW, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY]) {
      expect(ageLine(ms, "Never scanned", scanned, NOW)).toBe("Never scanned");
    }
    // 1 ms past the epoch is still a real (absurd) age, not a missing timestamp.
    expect(ageLine(1, "Never scanned", scanned, NOW)).toBe("Scanned 20833d ago");
  });

  it("formats long ages", () => {
    expect(ageAgo(NOW - 12 * MIN, NOW)).toBe("12 min ago");
    expect(ageAgo(NOW - 10_000, NOW)).toBe("1 min ago");
    expect(ageAgo(NOW - 61 * MIN, NOW)).toBe("1 h ago");
    expect(ageAgo(NOW - 50 * 60 * MIN, NOW)).toBe("2 d ago");
    expect(ageAgo(NOW - 59.5 * MIN, NOW)).toBe("59 min ago");
  });

  it("formats time, duration, tokens, cost, sha and truncation", () => {
    expect(formatTime(new Date(2026, 8, 17, 14, 32).getTime())).toBe("14:32");
    expect(formatDuration(9_400)).toBe("9s");
    expect(formatDuration(126_000)).toBe("2m 6s");
    expect(formatTokens(950)).toBe("950");
    expect(formatTokens(12_345)).toBe("12.3k");
    expect(formatTokens(1_234_567)).toBe("1.2M");
    expect(formatTokens(999_950)).toBe("1.0M");
    expect(formatCost(0.4213)).toBe("$0.42");
    expect(sha7("0123456789abcdef")).toBe("0123456");
    expect(truncate("abcdef", 4)).toBe("abc…");
    expect(truncate("abc", 4)).toBe("abc");
  });
});
