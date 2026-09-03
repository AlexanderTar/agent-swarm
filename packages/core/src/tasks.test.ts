import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { normalizeHookInput } from "./hooks.js";
import { TaskService } from "./tasks.js";

const dirs: string[] = [];

afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

function fresh(): TaskService {
  const dir = mkdtempSync(join(tmpdir(), "swarm-tasks-"));
  dirs.push(dir);
  const db = new SwarmDatabase(join(dir, "test.db"));
  return new TaskService(db.db);
}

describe("tags", () => {
  it("persists tags on create (normalized lowercase, deduped)", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude", tags: ["agent-swarm", "Backend", "agent-swarm"] } as never);
    expect(JSON.parse(t.tagsJson)).toEqual(["agent-swarm", "backend"]);
  });

  it("replaces tags via update, merges addTags/removeTags", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude", tags: ["a", "b"] } as never);
    const replaced = svc.update(t.id, { tags: ["X", "y"] } as never);
    expect(JSON.parse(replaced.tagsJson)).toEqual(["x", "y"]);
    const merged = svc.update(t.id, { addTags: ["Z", "x"], removeTags: ["y"] } as never);
    expect(JSON.parse(merged.tagsJson)).toEqual(["x", "z"]);
  });

  it("setTags helper normalizes", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude" });
    const updated = svc.setTags(t.id, ["B", "b", "A"]);
    expect(JSON.parse(updated.tagsJson)).toEqual(["b", "a"]);
  });
});

describe("join", () => {
  it("join claims unclaimed task", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    const r = svc.join(t.key, { agent: "codex", sessionId: "s1", by: "codex" } as never);
    expect(r.ok).toBe(true);
    expect(r.task?.claimedAgent).toBe("codex");
  });

  it("join appends session without stealing claim", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    svc.claim(t.key, { agent: "claude", sessionId: "s2", by: "claude" }, 300);
    const r = svc.join(t.key, { agent: "codex", sessionId: "s1", by: "codex" } as never);
    expect(r.ok).toBe(true);
    // Claim must NOT be stolen.
    expect(svc.getByKey(t.key)?.claimedAgent).toBe("claude");
    const events = svc.getEvents(t.id);
    expect(events.some((e) => e.eventType === "join")).toBe(true);
  });

  it("claim backfills unknown origin", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    svc.claim(t.key, { agent: "claude", sessionId: "s2", by: "claude" }, 300);
    expect(svc.getByKey(t.key)!.originAgent).toBe("claude");
  });

  it("join backfills unknown origin without stealing", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    svc.claim(t.key, { agent: "claude", sessionId: "s2", by: "claude" }, 300);
    // Reset origin to unknown to simulate legacy row, then join from another agent.
    svc.join(t.key, { agent: "codex", sessionId: "s3", by: "codex", model: "m1", cwd: "/r", pid: 42 } as never);
    const after = svc.getByKey(t.key)!;
    // Origin already claude (not unknown) so it stays; claim stays with claude.
    expect(after.claimedAgent).toBe("claude");
  });
});

describe("hook pid + transcript", () => {
  it("parses pid from hook payload", () => {
    const n = normalizeHookInput({ session_id: "s", pid: 1234, transcript_path: "/tmp/x.jsonl" }, "claude");
    expect((n as never as { pid: number }).pid).toBe(1234);
  });

  it("parses process_pid / agent_pid variants", () => {
    const a = normalizeHookInput({ session_id: "s", process_pid: 111 }, "claude");
    expect((a as never as { pid: number }).pid).toBe(111);
    const b = normalizeHookInput({ session_id: "s", agent_pid: 222 }, "codex");
    expect((b as never as { pid: number }).pid).toBe(222);
  });

  it("upsertSessionTask persists pid + transcript to artifacts", () => {
    const svc = fresh();
    const t = svc.upsertSessionTask({
      sessionId: "sess-tx",
      agent: "claude",
      cwd: "/repo",
      pid: 999,
      transcriptPath: "/tmp/t.jsonl",
      title: "T",
    } as never);
    expect(t.originPid).toBe(999);
    const artifacts = JSON.parse(svc.getById(t.id)!.artifactsJson) as Record<string, string[]>;
    expect(artifacts.transcript).toContain("/tmp/t.jsonl");
  });
});
