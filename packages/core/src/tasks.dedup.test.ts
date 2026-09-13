import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { TaskService } from "./tasks.js";

describe("explicit session membership", () => {
  const dirs: string[] = [];

  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  function fresh(): TaskService {
    const dir = mkdtempSync(join(tmpdir(), "swarm-membership-"));
    dirs.push(dir);
    const db = new SwarmDatabase(join(dir, "test.db"));
    return new TaskService(db.db);
  }

  it("allows a session to explicitly participate in more than one task", () => {
    const tasks = fresh();
    const first = tasks.create({ title: "First", originAgent: "claude" });
    const second = tasks.create({ title: "Second", originAgent: "claude" });

    tasks.join(first.key, { agent: "claude", sessionId: "planner", by: "planner" });
    tasks.join(second.key, { agent: "claude", sessionId: "planner", by: "planner" });

    expect(tasks.listSessions(first.id).map((s) => s.sessionId)).toEqual(["planner"]);
    expect(tasks.listSessions(second.id).map((s) => s.sessionId)).toEqual(["planner"]);
  });

  it("does not merge separately created tasks that share provenance", () => {
    const tasks = fresh();
    const a = tasks.create({ title: "A", originAgent: "cursor", originSessionId: "planner" });
    const b = tasks.create({ title: "B", originAgent: "cursor", originSessionId: "planner" });

    expect(tasks.consolidateDuplicateSessions()).toBe(0);
    expect(tasks.getById(a.id)?.status).toBe("ready");
    expect(tasks.getById(b.id)?.status).toBe("ready");
  });
});
