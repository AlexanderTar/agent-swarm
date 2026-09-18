import { describe, expect, it } from "vitest";
import { seed } from "../mock/fixtures";
import { BRIEF_MAX, PARENT_TYPES, TITLE_MAX, canCreate, initialParent, newItemPayload, parentOptions } from "./newItem";

const items = seed().items;
const base = { type: "task" as const, parentKey: "STORY-40", title: "Add tests", brief: "", acceptance: ["", " A works ", "B works"] };

describe("new item rules (§16.5, I15)", () => {
  it("lists eligible parents", () => {
    expect(PARENT_TYPES).toEqual({ epic: [], bug: [], story: ["epic"], task: ["story", "bug", "spike"] });
    expect(parentOptions(items, "story").map((i) => i.key)).toEqual(["EPIC-12", "EPIC-20", "EPIC-30"]);
    expect(parentOptions(items, "task").map((i) => i.type)).not.toContain("epic");
    expect(parentOptions(items, "task").map((i) => i.key)).toContain("SPIKE-3");
    expect(parentOptions(items, "epic")).toEqual([]);
  });

  it("prefills the parent only when eligible", () => {
    expect(initialParent(items, "story", "EPIC-12")).toBe("EPIC-12");
    expect(initialParent(items, "story", "BUG-7")).toBe("");
    expect(initialParent(items, "task", "BUG-7")).toBe("BUG-7");
    expect(initialParent(items, "epic", "EPIC-12")).toBe("");
    expect(initialParent(items, "task")).toBe("");
  });

  it("validates and builds the payload", () => {
    expect(TITLE_MAX).toBe(200);
    expect(BRIEF_MAX).toBe(600);
    expect(canCreate(base)).toBe(true);
    expect(canCreate({ ...base, title: "  " })).toBe(false);
    expect(canCreate({ ...base, parentKey: "" })).toBe(false);
    expect(canCreate({ ...base, type: "epic", parentKey: "" })).toBe(true);
    expect(canCreate({ ...base, brief: "x".repeat(601) })).toBe(false);
    expect(newItemPayload(base, "r1")).toEqual({
      request_id: "r1", type: "task", title: "Add tests", brief: "", acceptance: ["A works", "B works"], parent_key: "STORY-40",
    });
    expect(newItemPayload({ ...base, type: "bug" }, "r2")).not.toHaveProperty("parent_key");
  });
});
