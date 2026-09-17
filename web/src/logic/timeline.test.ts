import { describe, expect, it } from "vitest";
import type { Advice, Checkpoint } from "../types";
import { adviceTitle, adviceTotals, advisorName, gitLine, kindLabel, mergeTimeline, verifyLine } from "./timeline";

const cp = (id: string, at: number) => ({ id, created_at: at }) as Checkpoint;
const adv = (p: Partial<Advice>): Advice => ({
  id: "adv", session_id: "s", item_key: "TASK-1", advisor_kind: "codex", advisor_model: "gpt-6-astra", advisor_effort: "high",
  question: "q", answer: "a", error: null, state: "answered", mode: "simulated", duration_ms: 9_400, input_tokens: 12_000,
  output_tokens: 300, cache_read_tokens: null, cache_write_tokens: null, cost_usd: 0.04, created_at: 5, finished_at: 6, ...p,
});

describe("timeline", () => {
  it("merges newest first", () => {
    const out = mergeTimeline([cp("a", 1), cp("b", 10)], [adv({ id: "x", created_at: 5 })]);
    expect(out.map((e) => (e.kind === "checkpoint" ? e.checkpoint.id : e.advice.id))).toEqual(["b", "x", "a"]);
  });

  it("formats advice rows and totals (§11.6)", () => {
    expect(advisorName(adv({}))).toBe("codex/gpt-6-astra (high)");
    expect(adviceTitle(adv({}))).toBe("Advice · codex/gpt-6-astra (high) · 9s · 12.3k · $0.04");
    expect(adviceTitle(adv({ advisor_effort: null, duration_ms: null, input_tokens: null, output_tokens: null, cost_usd: null })))
      .toBe("Advice · codex/gpt-6-astra");
    expect(adviceTotals([adv({}), adv({ input_tokens: 700, output_tokens: 0, cost_usd: null })])).toBe("Advisor · 13.0k tokens · $0.04");
    expect(adviceTotals([])).toBeNull();
  });

  it("formats git, verification and kind", () => {
    expect(gitLine({ repo: "endurio-chat", branch: "task/x", sha: "9f8e7d6c5b4a", dirty: true })).toBe("endurio-chat task/x 9f8e7d6 dirty");
    expect(gitLine({ repo: "r", branch: "b", sha: "0123456789" })).toBe("r b 0123456");
    expect(verifyLine({ cmd: "go test ./...", phase: "green", ok: true })).toBe("✓ go test ./...");
    expect(verifyLine({ cmd: "pnpm test", phase: "red", ok: false, note: "expected failure" })).toBe("✗ pnpm test — expected failure");
    expect(kindLabel("integrated")).toBe("Integrated");
  });
});
