import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { SessionService } from "./sessions.js";

function freshDb(): SwarmDatabase {
  return new SwarmDatabase(join(mkdtempSync(join(tmpdir(), "swarm-sessions-")), "swarm.db"));
}

describe("SessionService", () => {
  let swarm: SwarmDatabase;
  let sessions: SessionService;
  let taskId: number;

  beforeEach(() => {
    swarm = freshDb();
    sessions = new SessionService(swarm.db);
    taskId = Number(
      swarm.db
        .prepare(
          "INSERT INTO tasks (key, title, origin_agent) VALUES ('SW-1', 'Fix board', 'claude')",
        )
        .run().lastInsertRowid,
    );
  });

  it("upserts idempotently and keeps known fields", () => {
    const first = sessions.upsert({ id: "s1", agent: "claude", cwd: "/repo", model: "opus-5" });
    const second = sessions.upsert({ id: "s1", agent: "unknown" });
    expect(second.id).toBe(first.id);
    expect(second.agentKind).toBe("claude");
    expect(second.cwd).toBe("/repo");
    expect(second.model).toBe("opus-5");
    expect(swarm.db.prepare("SELECT COUNT(*) AS c FROM sessions").get()).toEqual({ c: 1 });
  });

  it("resolves an explicit session id ahead of the cwd match", () => {
    sessions.upsert({ id: "s1", agent: "claude", cwd: "/repo" });
    sessions.upsert({ id: "s2", agent: "codex", cwd: "/repo" });
    expect(sessions.resolve({ sessionId: "s1", cwd: "/repo" })?.id).toBe("s1");
    // An explicit id that is not on the board never falls back to a sibling in the same cwd.
    expect(sessions.resolve({ sessionId: "nope", cwd: "/repo" })).toBeNull();
  });

  it("resolves by cwd to the newest live session", () => {
    sessions.upsert({ id: "old", agent: "claude", cwd: "/repo" });
    sessions.upsert({ id: "new", agent: "codex", cwd: "/repo" });
    sessions.upsert({ id: "ended", agent: "cursor", cwd: "/repo" });
    swarm.db
      .prepare("UPDATE sessions SET last_seen_at = datetime('now', '-10 minutes') WHERE id = 'old'")
      .run();
    swarm.db
      .prepare(
        "UPDATE sessions SET last_seen_at = datetime('now', '+10 minutes') WHERE id = 'ended'",
      )
      .run();
    sessions.end("ended");

    expect(sessions.resolve({ cwd: "/repo" })?.id).toBe("new");
    expect(sessions.resolve({ cwd: "/elsewhere" })).toBeNull();
    expect(sessions.resolve({})).toBeNull();
  });

  it("binds many sessions to one task and labels them", () => {
    sessions.upsert({ id: "s1", agent: "claude", model: "opus-5" });
    sessions.upsert({ id: "s2", agent: "codex", model: "gpt-5.1" });
    expect(sessions.bind("s1", taskId)).toBe(true);
    expect(sessions.bind("s2", taskId)).toBe(true);
    expect(sessions.bind("ghost", taskId)).toBe(false);

    expect(sessions.listByTask(taskId).map((s) => s.id)).toEqual(["s1", "s2"]);
    const labels = sessions.labelsForTasks([taskId]).get(taskId)!;
    expect(labels).toEqual([
      { sessionId: "s1", agent: "claude", model: "opus-5", active: true },
      { sessionId: "s2", agent: "codex", model: "gpt-5.1", active: true },
    ]);
    expect(sessions.labelsForTasks([]).size).toBe(0);
  });

  it("clears active on end", () => {
    sessions.upsert({ id: "s1", agent: "claude" });
    sessions.bind("s1", taskId);
    sessions.end("s1");
    expect(sessions.labelsForTasks([taskId]).get(taskId)![0]!.active).toBe(false);
    expect(sessions.get("s1")?.endedAt).toBeTruthy();
  });

  it("records subagents against their parent", () => {
    sessions.upsert({ id: "s1", agent: "claude" });
    const sub = sessions.upsert({ id: "a1", agent: "claude", parentSessionId: "s1" });
    expect(sub.parentSessionId).toBe("s1");
    expect(sessions.get("nobody")).toBeNull();
  });
});
