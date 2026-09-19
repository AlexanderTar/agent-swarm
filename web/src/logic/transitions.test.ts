import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import type { ItemStatus, ItemType } from "../types";
import { checkMove, failureMessage, moveOptions } from "./transitions";

const it_ = (type: ItemType, status: ItemStatus, key = `${type.toUpperCase()}-1`, before: ItemStatus | null = null) =>
  ({ key, type, status, status_before_block: before });

describe("checkMove (user actor, §10.1 + §17.3)", () => {
  it.each([
    ["draft → ready", it_("task", "draft"), "ready"],
    ["open → blocked", it_("task", "in_progress"), "blocked"],
    ["open → cancelled", it_("epic", "in_progress"), "cancelled"],
    ["story → blocked", it_("story", "ready"), "blocked"],
    ["story → cancelled", it_("story", "in_progress"), "cancelled"],
    ["blocked → the saved status", it_("task", "blocked", "TASK-1", "in_progress"), "in_progress"],
    ["blocked story → the saved status", it_("story", "blocked", "STORY-40", "in_review"), "in_review"],
    ["blocked → cancelled", it_("task", "blocked", "TASK-1", "in_progress"), "cancelled"],
    ["reopen done", it_("epic", "done"), "ready"],
    ["reopen cancelled", it_("story", "cancelled"), "ready"],
  ] as const)("allows %s", (_n, item, to) => {
    expect(checkMove(item, to)).toEqual({ ok: true });
  });

  it("routes epic and bug Done to acceptance", () => {
    expect(checkMove(it_("epic", "in_review"), "done")).toEqual({ ok: false, reason: "Accept this epic to mark it Done.", special: "accept" });
    expect(checkMove(it_("bug", "in_progress"), "done")).toEqual({ ok: false, reason: "Accept this fix to mark it Done.", special: "accept" });
  });

  it("explains spike Done", () => {
    expect(checkMove(it_("spike", "awaiting_approval"), "done")).toEqual({
      ok: false, reason: "This spike reaches Done after materialization.", special: "spike",
    });
  });

  it("refuses story and task Done with their copy", () => {
    expect(checkMove(it_("story", "in_review", "STORY-40"), "done")).toEqual({
      ok: false, reason: "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.",
    });
    expect(checkMove(it_("task", "in_progress", "TASK-7"), "done")).toEqual({
      ok: false, reason: "Couldn't move TASK-7 to Done. No agent has reported it complete.",
    });
  });

  it("refuses awaiting approval for non-spikes, whatever the current status", () => {
    expect(checkMove(it_("epic", "ready"), "awaiting_approval")).toEqual({ ok: false, reason: "Only spikes can await approval." });
    expect(checkMove(it_("task", "done"), "awaiting_approval")).toEqual({ ok: false, reason: "Only spikes can await approval." });
    expect(checkMove(it_("bug", "blocked", "BUG-2", "in_progress"), "awaiting_approval")).toEqual({ ok: false, reason: "Only spikes can await approval." });
  });

  it.each([
    ["daemon-only in_progress", it_("task", "ready"), "in_progress", "Ready"],
    ["daemon-only in_review", it_("task", "in_progress"), "in_review", "In progress"],
    ["spike awaiting approval (daemon-only)", it_("spike", "in_progress"), "awaiting_approval", "In progress"],
    ["derived story status", it_("story", "ready"), "in_progress", "Ready"],
    ["done → blocked", it_("task", "done"), "blocked", "Done"],
    ["cancelled epic → done", it_("epic", "cancelled"), "done", "Cancelled"],
    ["same status", it_("task", "ready"), "ready", "Ready"],
    ["in_review → in_progress (orchestrator only)", it_("task", "in_review"), "in_progress", "In review"],
    ["task in review → done (orchestrator only)", it_("task", "in_review", "TASK-7"), "done", "In review"],
    ["blocked task → any other status", it_("task", "blocked", "TASK-1", "in_progress"), "ready", "Blocked"],
    ["blocked task with no saved status", it_("task", "blocked"), "in_progress", "Blocked"],
    ["blocked story → a status that isn't the saved one", it_("story", "blocked", "STORY-40", "in_review"), "ready", "Blocked"],
    ["blocked epic → done (no accept review)", it_("epic", "blocked", "EPIC-1", "in_review"), "done", "Blocked"],
    ["blocked spike → done", it_("spike", "blocked", "SPIKE-3", "in_progress"), "done", "Blocked"],
  ] as const)("uses the generic copy for %s", (_n, item, to, label) => {
    expect(checkMove(item, to)).toEqual({ ok: false, reason: `Couldn't update status. The item remains ${label}.` });
  });
});

describe("moveOptions", () => {
  it("lists every other status in column order with checks", () => {
    const opts = moveOptions(it_("task", "ready"));
    expect(opts.map((o) => o.label)).toEqual(["Draft", "In progress", "Blocked", "In review", "Awaiting approval", "Done", "Cancelled"]);
    expect(opts.find((o) => o.status === "blocked")?.check).toEqual({ ok: true });
  });
});

describe("failureMessage", () => {
  const item = it_("story", "in_review", "STORY-40");
  it("uses the daemon reason for transition_denied", () => {
    expect(failureMessage(new ApiError(422, "transition_denied", "denied", "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first."), item))
      .toBe("Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.");
    expect(failureMessage(new ApiError(422, "transition_denied", "Only spikes can await approval."), item)).toBe("Only spikes can await approval.");
  });
  it("uses the stale-revision copy for conflicts", () => {
    expect(failureMessage(new ApiError(409, "conflict", "stale"), item)).toBe("This item changed elsewhere. Showing its latest status.");
  });
  it("falls back to the generic copy", () => {
    expect(failureMessage(new TypeError("Failed to fetch"), item)).toBe("Couldn't update status. The item remains In review.");
    expect(failureMessage(new ApiError(500, "internal", "x"), item)).toBe("Couldn't update status. The item remains In review.");
  });
});
