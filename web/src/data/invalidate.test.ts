import { describe, expect, it } from "vitest";
import { keysForEvent } from "./invalidate";

describe("keysForEvent (§7.1)", () => {
  it.each([
    ["item.changed", { key: "A", root_key: "A" }, ["items", "item:", "graph:"]],
    ["agent.changed", { name: "a", root_key: "A" }, ["agents", "item:", "advice:"]],
    ["checkpoint.created", { item: "TASK-1", agent: "a", kind: "progress" }, ["checkpoints:TASK-1", "item:"]],
    ["checkpoint.created", null, ["checkpoints:", "item:"]],
    ["request.opened", {}, ["requests", "items", "item:"]],
    ["request.resolved", {}, ["requests", "items", "item:"]],
    ["settings.changed", {}, ["settings"]],
    ["catalog.changed", [], ["catalog"]],
    ["repos.changed", { scanning: false, found: 3 }, ["repos:"]],
    ["reset", null, [""]],
    ["usage.changed", {}, []],
    ["notification.created", {}, []],
    ["terminal.open", {}, []],
  ])("%s", (type, data, keys) => {
    expect(keysForEvent({ seq: 1, type, data })).toEqual(keys);
  });
});
