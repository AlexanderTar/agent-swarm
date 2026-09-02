import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { SessionService } from "./sessions.js";
import { TaskService, normalizeTags } from "./tasks.js";

describe("normalizeTags", () => {
  it("lowercases, hyphenates and dedupes", () => {
    expect(normalizeTags(["Board", "  MCP  ", "board", "agent swarm"])).toEqual([
      "agent-swarm",
      "board",
      "mcp",
    ]);
  });

  it("drops empties and illegal characters", () => {
    expect(normalizeTags(["", "   ", "!!!", "c++", "api/v2", "a.b_c-d"])).toEqual([
      "a.b_c-d",
      "api/v2",
      "c",
    ]);
  });

  it("caps tags at 40 chars and lists at 20 entries", () => {
    expect(normalizeTags([`${"x".repeat(60)}`])[0]).toHaveLength(40);
    expect(normalizeTags(Array.from({ length: 30 }, (_, i) => `tag${i}`))).toHaveLength(20);
  });
});

describe("task tags and sessions", () => {
  let tasks: TaskService;
  let sessions: SessionService;

  beforeEach(() => {
    const swarm = new SwarmDatabase(join(mkdtempSync(join(tmpdir(), "swarm-tags-")), "swarm.db"));
    sessions = new SessionService(swarm.db);
    tasks = new TaskService(swarm.db, sessions);
  });

  it("stores normalized tags on create and update", () => {
    const task = tasks.create({
      title: "Fix board titles",
      originAgent: "claude",
      tags: ["Board", "MCP"],
    });
    expect(tasks.getTags(task.id)).toEqual(["board", "mcp"]);

    tasks.addTags(task.id, ["Done Ish"]);
    expect(tasks.getTags(task.id)).toEqual(["board", "done-ish", "mcp"]);

    tasks.removeTags(task.id, ["MCP"]);
    expect(tasks.getTags(task.id)).toEqual(["board", "done-ish"]);

    tasks.setTags(task.id, ["only"]);
    expect(tasks.getTags(task.id)).toEqual(["only"]);
    expect(tasks.listAllTags()).toEqual(["only"]);
  });

  it("writes summary into handoffNote", () => {
    const task = tasks.create({
      title: "Migration",
      originAgent: "codex",
      summary: "Schema v3 landed",
    });
    expect(task.handoffNote).toBe("Schema v3 landed");
    expect(tasks.update(task.id, { summary: "Now reviewing" }).handoffNote).toBe("Now reviewing");
  });

  it("filters by tag via json_each", () => {
    tasks.create({ title: "A", originAgent: "claude", tags: ["board"] });
    tasks.create({ title: "B", originAgent: "claude", tags: ["ui"] });
    expect(tasks.list({ tag: "board" }).map((t) => t.title)).toEqual(["A"]);
    expect(tasks.list({ tag: "nope" })).toEqual([]);
    expect(tasks.list().length).toBe(2);
  });

  it("filters by agent across origin, claimer and bound sessions", () => {
    const task = tasks.create({ title: "Shared", originAgent: "claude" });
    sessions.upsert({ id: "s2", agent: "codex", model: "gpt-5.1" });
    sessions.bind("s2", task.id);

    const [byCodex] = tasks.list({ agent: "codex" });
    expect(byCodex?.title).toBe("Shared");
    expect(byCodex?.sessions).toEqual([
      { sessionId: "s2", agent: "codex", model: "gpt-5.1", active: true },
    ]);
    expect(tasks.list({ agent: "claude" }).map((t) => t.title)).toEqual(["Shared"]);
    expect(tasks.list({ agent: "cursor" })).toEqual([]);
  });

  it("hides archived tasks unless asked for them", () => {
    const task = tasks.create({ title: "Old", originAgent: "claude", tags: ["board"] });
    tasks.update(task.id, { status: "archived" });
    expect(tasks.list()).toEqual([]);
    expect(tasks.list({ tag: "board" })).toEqual([]);
    expect(tasks.list({ status: "archived" }).map((t) => t.title)).toEqual(["Old"]);
    expect(tasks.listAllTags()).toEqual([]);
  });
});
