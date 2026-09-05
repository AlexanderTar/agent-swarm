import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import Fastify from "fastify";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type SwarmContext, createContext } from "./context.js";
import { registerHookRoutes } from "./routes.js";

// Title refresh fires a real Ollama call in the background; stub it out so
// tests stay hermetic and don't leave retry timers running after they finish.
vi.mock("./titleJob.js", () => ({ scheduleTitleRefresh: vi.fn() }));

const dirs: string[] = [];
const contexts: SwarmContext[] = [];

afterEach(() => {
  for (const ctx of contexts.splice(0)) {
    ctx.kb.stopWatching();
    ctx.db.close();
  }
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

async function freshApp() {
  const dir = mkdtempSync(join(tmpdir(), "swarm-daemon-"));
  dirs.push(dir);
  const ctx = createContext(dir);
  contexts.push(ctx);
  const app = Fastify({ logger: false });
  await registerHookRoutes(app, ctx);
  return { app, ctx };
}

describe("SessionStart no longer creates a board card up front", () => {
  it("leaves zero tasks after SessionStart, then creates exactly one on the first prompt", async () => {
    const { app, ctx } = await freshApp();
    const sessionId = "session-idle-check";

    const startRes = await app.inject({
      method: "POST",
      url: "/hooks/claude/SessionStart",
      payload: { session_id: sessionId, cwd: "/tmp/proj" },
    });
    expect(startRes.statusCode).toBe(200);
    const startBody = startRes.json();
    expect(startBody.ok).toBe(true);
    expect(startBody.taskKey).toBeUndefined();
    expect(ctx.tasks.list()).toHaveLength(0);

    const promptRes = await app.inject({
      method: "POST",
      url: "/hooks/claude/UserPromptSubmit",
      payload: { session_id: sessionId, cwd: "/tmp/proj", prompt: "do the thing" },
    });
    expect(promptRes.statusCode).toBe(200);
    const tasks = ctx.tasks.list();
    expect(tasks).toHaveLength(1);
    expect(tasks[0]?.originSessionId).toBe(sessionId);
  });

  it("still syncs an already-existing card on SessionStart (resumed session)", async () => {
    const { app, ctx } = await freshApp();
    const sessionId = "session-resumed";

    // Create real activity first, so a card already exists for this session.
    await app.inject({
      method: "POST",
      url: "/hooks/claude/UserPromptSubmit",
      payload: { session_id: sessionId, cwd: "/tmp/proj", prompt: "first prompt" },
    });
    const seeded = ctx.tasks.list();
    expect(seeded).toHaveLength(1);
    const existingKey = seeded[0]?.key;

    const resumeRes = await app.inject({
      method: "POST",
      url: "/hooks/claude/SessionStart",
      payload: { session_id: sessionId, cwd: "/tmp/proj" },
    });
    expect(resumeRes.statusCode).toBe(200);
    expect(resumeRes.json()).toMatchObject({ ok: true, taskKey: existingKey });
    // No duplicate card was created.
    expect(ctx.tasks.list()).toHaveLength(1);
  });
});
