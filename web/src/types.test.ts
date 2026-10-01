import { describe, expect, it } from "vitest";
import { ITEM_STATUSES } from "./types";
import type { ApproveBody, Item, Request } from "./types";

describe("types", () => {
  it("lists statuses in the §16.7 column order", () => {
    expect(ITEM_STATUSES).toEqual([
      "draft", "ready", "in_progress", "blocked", "in_review", "awaiting_approval", "done", "cancelled",
    ]);
  });

  it("runs with the React Flow jsdom stubs", () => {
    expect(typeof ResizeObserver).toBe("function");
    expect(typeof window.matchMedia).toBe("function");
  });

  it("types the agent-option and waiver/override wire fields", () => {
    const body: ApproveBody = { merge: "custom", choice: "Push straight to main", comment: "ok" };
    const req: Pick<Request, "options" | "option_descriptions" | "choice"> = {
      options: ["A", "B"], option_descriptions: ["a", "b"], choice: null,
    };
    const item: Pick<Item, "waivers" | "override"> = {
      waivers: [{ gate: "verify", reason: "r", agent: "orch", at: "2026-10-01T09:00:00Z" }],
      override: { status: "done", reason: "r", agent: "orch", at: "2026-10-01T09:00:00Z" },
    };
    expect(body.merge).toBe("custom");
    expect(req.option_descriptions?.length).toBe(2);
    expect(item.waivers?.[0]?.gate).toBe("verify");
    expect(item.override?.status).toBe("done");
  });
});
