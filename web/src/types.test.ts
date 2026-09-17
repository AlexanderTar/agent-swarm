import { describe, expect, it } from "vitest";
import { ITEM_STATUSES } from "./types";

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
});
