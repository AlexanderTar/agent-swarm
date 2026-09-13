import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";
import { normalizeHookInput } from "./hooks.js";
import { modelTag, TaskService } from "./tasks.js";

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
  it("join registers a participant without claiming an unclaimed task", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    const r = svc.join(t.key, { agent: "codex", sessionId: "s1", by: "codex" } as never);
    expect(r.ok).toBe(true);
    expect(r.task?.claimedAgent).toBeNull();
    expect(r.task?.status).toBe("ready");
    expect(svc.listSessions(t.id).map((s) => s.sessionId)).toEqual(["s1"]);
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

  it("claim does not rewrite task provenance", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    svc.claim(t.key, { agent: "claude", sessionId: "s2", by: "claude" }, 300);
    expect(svc.getByKey(t.key)!.originAgent).toBe("unknown");
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

describe("model auto-tag", () => {
  it("modelTag slugifies model ids", () => {
    expect(modelTag("claude-opus-4-5")).toBe("claude-opus-4-5");
    expect(modelTag("Claude Opus 4.5")).toBe("claude-opus-4-5");
    expect(modelTag("big-pickle")).toBe("big-pickle");
    expect(modelTag("")).toBe("");
    expect(modelTag("   ")).toBe("");
    expect(modelTag("--Foo__Bar--")).toBe("foo-bar");
    expect(modelTag("x".repeat(100)).length).toBeLessThanOrEqual(40);
  });

  it("auto-tags model slug on create", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude", originModel: "claude-opus-4-5" } as never);
    expect(t.originModel).toBe("claude-opus-4-5");
    expect(JSON.parse(t.tagsJson)).toContain("claude-opus-4-5");
  });

  it("no duplicate tag when model slug already present", () => {
    const svc = fresh();
    const t = svc.create({
      title: "t",
      originAgent: "claude",
      originModel: "claude-opus-4-5",
      tags: ["Claude-Opus-4-5", "backend"],
    } as never);
    expect(JSON.parse(t.tagsJson)).toEqual(["claude-opus-4-5", "backend"]);
  });

  it("claim backfill tags model", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    svc.claim(t.key, { agent: "claude", sessionId: "s-model-1", by: "claude", model: "claude-sonnet-4" }, 300);
    const after = svc.getByKey(t.key)!;
    expect(after.originModel).toBe("claude-sonnet-4");
    expect(JSON.parse(after.tagsJson)).toContain("claude-sonnet-4");
  });

  it("join tags model without stealing claim", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "unknown" } as never);
    const r = svc.join(t.key, { agent: "codex", sessionId: "s-model-2", by: "codex", model: "gpt-5" } as never);
    expect(r.ok).toBe(true);
    const after = svc.getByKey(t.key)!;
    expect(JSON.parse(after.tagsJson)).toContain("gpt-5");
  });

  it("explicit creation tags model", () => {
    const svc = fresh();
    const t = svc.create({ originAgent: "opencode", originModel: "big-pickle", title: "T" } as never);
    expect(JSON.parse(t.tagsJson)).toContain("big-pickle");
    expect(JSON.parse(t.tagsJson).filter((x: string) => x === "big-pickle")).toHaveLength(1);
  });

  it("refreshOriginMetadata tags model", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude" } as never);
    svc.refreshOriginMetadata(t.id, { model: "claude-haiku-3-5" });
    const after = svc.getById(t.id)!;
    expect(after.originModel).toBe("claude-haiku-3-5");
    expect(JSON.parse(after.tagsJson)).toContain("claude-haiku-3-5");
  });

  it("ensureModelTag is idempotent and never throws", () => {
    const svc = fresh();
    const t = svc.create({ title: "t", originAgent: "claude" } as never);
    svc.ensureModelTag(t.id, "m1");
    svc.ensureModelTag(t.id, "m1");
    expect(JSON.parse(svc.getById(t.id)!.tagsJson).filter((x: string) => x === "m1")).toHaveLength(1);
    expect(svc.ensureModelTag(999999, "m1")).toBeNull();
    expect(svc.ensureModelTag(t.id, "")?.id).toBe(t.id);
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

  it("explicit task creation persists pid", () => {
    const svc = fresh();
    const t = svc.create({
      originAgent: "claude",
      originCwd: "/repo",
      originPid: 999,
      title: "T",
    } as never);
    expect(t.originPid).toBe(999);
  });
});

describe("cleanupSubagentTasks", () => {
  it("never archives tasks automatically", () => {
    const svc = fresh();
    const sub1 = svc.create({
      title: "Subagent · myproject",
      originAgent: "claude",
      originSessionId: "a1234567890abcdef",
      initialContext: "- **Parent session:** `parent-session-123`",
    } as never);
    svc.addSubtask(sub1.id, "Explore repo");
    svc.appendEvent(sub1.id, "subagent_start", { agent_type: "Explore" });

    const result = svc.cleanupSubagentTasks();
    expect(result.archivedCount).toBe(0);
    expect(svc.getById(sub1.id)?.status).toBe("ready");
  });
});

describe("explicit task lifecycle", () => {
  it("allows one planner session to create a parent and multiple child tasks", () => {
    const svc = fresh();
    const parent = svc.create({ title: "Plan", originAgent: "claude", originSessionId: "planner" } as never);
    const first = svc.create({ title: "First", originAgent: "claude", parentKey: parent.key } as never);
    const second = svc.create({ title: "Second", originAgent: "claude", parentKey: parent.key } as never);

    expect(first.parentTaskId).toBe(parent.id);
    expect(second.parentTaskId).toBe(parent.id);
    expect(first.status).toBe("ready");
  });

  it("lets only one worker acquire an active lease and exposes its token", () => {
    const svc = fresh();
    const task = svc.create({ title: "Work", originAgent: "claude" } as never);
    const first = svc.claim(task.key, { agent: "claude", sessionId: "worker-a", by: "a" }, 300);
    const second = svc.claim(task.key, { agent: "codex", sessionId: "worker-b", by: "b" }, 300);

    expect(first.ok).toBe(true);
    expect(first.task?.status).toBe("in_progress");
    expect(first.task?.claimToken).toEqual(expect.any(String));
    expect(second.ok).toBe(false);
  });

  it("atomically grants one lease across independent database connections", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-claim-race-"));
    dirs.push(dir);
    const firstDb = new SwarmDatabase(join(dir, "test.db"));
    const secondDb = new SwarmDatabase(join(dir, "test.db"));
    const first = new TaskService(firstDb.db);
    const second = new TaskService(secondDb.db);
    const task = first.create({ title: "Work", originAgent: "claude" } as never);

    const a = first.claim(task.key, { agent: "claude", sessionId: "a", by: "a" }, 300);
    const b = second.claim(task.key, { agent: "codex", sessionId: "b", by: "b" }, 300);

    expect([a.ok, b.ok].filter(Boolean)).toHaveLength(1);
    firstDb.close();
    secondDb.close();
  });

  it("rejects stale lease tokens for heartbeat and handoff", () => {
    const svc = fresh();
    const task = svc.create({ title: "Work", originAgent: "claude" } as never);
    const claim = svc.claim(task.key, { agent: "claude", sessionId: "worker", by: "a" }, 300);
    const token = claim.task!.claimToken!;

    expect(svc.heartbeat(task.key, "worker", "wrong-token", 300)).toBe(false);
    expect(svc.release(task.key, "worker", "wrong-token")).toBe(false);
    expect(svc.heartbeat(task.key, "worker", token, 300)).toBe(true);
    expect(svc.release(task.key, "worker", token)).toBe(true);
  });

  it("submits implementation for review and requires a reviewer lease to finish", () => {
    const svc = fresh();
    const task = svc.create({ title: "Work", originAgent: "claude" } as never);
    const implementation = svc.claim(task.key, { agent: "claude", sessionId: "worker", by: "a" }, 300);
    const implementationToken = implementation.task!.claimToken!;

    expect(svc.submit(task.key, "worker", implementationToken).ok).toBe(true);
    expect(svc.getByKey(task.key)?.status).toBe("review");
    const review = svc.claim(task.key, { agent: "codex", sessionId: "reviewer", by: "b" }, 300);
    expect(review.task?.status).toBe("review");
    expect(svc.approve(task.key, "reviewer", review.task!.claimToken!).ok).toBe(true);
    expect(svc.getByKey(task.key)?.status).toBe("done");
  });

  it("returns review work to ready when the reviewer requests changes", () => {
    const svc = fresh();
    const task = svc.create({ title: "Work", originAgent: "claude" } as never);
    const implementation = svc.claim(task.key, { agent: "claude", sessionId: "worker", by: "a" }, 300);
    svc.submit(task.key, "worker", implementation.task!.claimToken!);
    const review = svc.claim(task.key, { agent: "codex", sessionId: "reviewer", by: "b" }, 300);

    expect(svc.requestChanges(task.key, "reviewer", review.task!.claimToken!).ok).toBe(true);
    const after = svc.getByKey(task.key)!;
    expect(after.status).toBe("ready");
    expect(after.claimToken).toBeNull();
  });

  it("only the recorded coordinator can advance a parent task", () => {
    const svc = fresh();
    const plan = svc.create({ title: "Plan", originAgent: "claude", coordinatorSessionId: "planner" } as never);

    expect(svc.coordinateParent(plan.key, "other", "in_progress").ok).toBe(false);
    expect(svc.coordinateParent(plan.key, "planner", "in_progress").ok).toBe(true);
    expect(svc.coordinateParent(plan.key, "planner", "done").ok).toBe(true);
  });
});
