import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { TaskService } from "./tasks.js";

describe("session id dedup", () => {
  const dirs: string[] = [];

  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  function fresh(): TaskService {
    const dir = mkdtempSync(join(tmpdir(), "swarm-dedup-"));
    dirs.push(dir);
    const db = new SwarmDatabase(join(dir, "test.db"));
    return new TaskService(db.db);
  }

  it("reuses the same tile when a done session gets new activity", () => {
    const tasks = fresh();
    const a = tasks.upsertSessionTask({
      sessionId: "sess-1",
      agent: "cursor",
      cwd: "/repo",
      title: "Apple Sign In",
      titleFromSession: true,
    });
    tasks.update(a.id, { status: "done" });

    const b = tasks.upsertSessionTask({
      sessionId: "sess-1",
      agent: "cursor",
      cwd: "/repo",
      title: "Apple Sign In",
      titleFromSession: true,
    });

    expect(b.id).toBe(a.id);
    expect(b.key).toBe(a.key);
    expect(b.status).toBe("in_progress");
  });

  it("getBySession finds done tiles", () => {
    const tasks = fresh();
    const a = tasks.upsertSessionTask({ sessionId: "sess-2", agent: "claude", cwd: "/r", title: "T" });
    tasks.update(a.id, { status: "done" });
    expect(tasks.getBySession("sess-2")?.key).toBe(a.key);
  });

  it("consolidateDuplicateSessions archives extras", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-dedup-"));
    dirs.push(dir);
    const swarmDb = new SwarmDatabase(join(dir, "test.db"));
    // Allow inserting duplicates to simulate pre-v2 data.
    swarmDb.db.exec("DROP INDEX IF EXISTS idx_tasks_session_unique");
    const tasks = new TaskService(swarmDb.db);

    const a = tasks.create({ title: "A", originAgent: "cursor", originSessionId: "dup-1", originCwd: "/r" });
    const b = tasks.create({ title: "B", originAgent: "cursor", originSessionId: "dup-1", originCwd: "/r" });
    const c = tasks.create({ title: "C", originAgent: "cursor", originSessionId: "dup-1", originCwd: "/r" });
    tasks.update(a.id, { status: "done", handoffNote: "# Summary" });

    const archived = tasks.consolidateDuplicateSessions();
    expect(archived).toBe(2);
    const keeper = tasks.getBySession("dup-1");
    expect(keeper?.id).toBe(a.id);
    expect(keeper?.handoffNote).toContain("Summary");
    expect(tasks.getById(b.id)?.status).toBe("archived");
    expect(tasks.getById(c.id)?.originSessionId).toBeNull();
  });
});
