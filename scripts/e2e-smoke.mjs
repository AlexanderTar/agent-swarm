#!/usr/bin/env node
/**
 * End-to-end smoke test: hooks never create board items, agents do.
 * Usage: SWARM_HOME=/tmp/swarm-e2e node scripts/e2e-smoke.mjs
 */
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const SWARM_HOME = process.env.SWARM_HOME ?? mkdtempSync(join(tmpdir(), "swarm-e2e-"));
const PORT = Number(process.env.SWARM_SCRATCH_PORT ?? 17777);
const BASE = `http://127.0.0.1:${PORT}`;
const cleanupHome = !process.env.SWARM_HOME;
const WORK = mkdtempSync(join(tmpdir(), "swarm-e2e-work-"));

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function waitForHealth(timeoutMs = 30_000) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const r = await fetch(`${BASE}/api/health`, { signal: AbortSignal.timeout(2000) });
      if (r.ok) return;
    } catch {
      // retry
    }
    await sleep(500);
  }
  throw new Error("daemon did not become healthy");
}

async function hook(platform, event, payload) {
  const r = await fetch(`${BASE}/hooks/${platform}/${event}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  assert(r.ok, `${platform}/${event} failed: ${r.status}`);
  return r.json();
}

async function main() {
  console.log(`E2E smoke test (SWARM_HOME=${SWARM_HOME}, port=${PORT})`);

  const daemon = spawn("node", ["packages/daemon/dist/index.js"], {
    cwd: process.cwd(),
    env: { ...process.env, SWARM_HOME, SWARM_SCRATCH_PORT: String(PORT) },
    stdio: ["ignore", "pipe", "pipe"],
  });

  let daemonLog = "";
  daemon.stdout.on("data", (d) => (daemonLog += d.toString()));
  daemon.stderr.on("data", (d) => (daemonLog += d.toString()));

  let token = "";
  const auth = () => ({
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  });
  const board = async (query = "") => {
    const r = await fetch(`${BASE}/api/board${query}`, { headers: auth() });
    assert(r.ok, `board fetch failed: ${r.status}`);
    return r.json();
  };

  try {
    await waitForHealth();
    token = readFileSync(`${SWARM_HOME}/run/daemon.token`, "utf8").trim();

    // 1. A session start registers the session and briefs the agent — no tile.
    const claudeSession = "e2e-claude-session";
    const start = await hook("claude", "SessionStart", {
      session_id: claudeSession,
      cwd: WORK,
      model: "opus-5",
    });
    const briefing = start?.hookSpecificOutput?.additionalContext ?? "";
    assert(briefing.includes(claudeSession), "SessionStart did not brief the agent with its session id");
    assert((await board()).length === 0, "SessionStart created a board item");
    console.log("✓ SessionStart briefs the agent and creates nothing");

    // 2. Tool events on an unbound session are dropped.
    const artifact = join(WORK, "notes.md");
    writeFileSync(artifact, "# Notes\n\nAuth middleware sketch.\n");
    await hook("claude", "PostToolUse", {
      session_id: claudeSession,
      cwd: WORK,
      tool_name: "Write",
      tool_input: { file_path: artifact },
    });
    assert((await board()).length === 0, "an unbound tool event created a board item");
    console.log("✓ unbound tool events are dropped");

    // 3. The agent names its own work.
    let r = await fetch(`${BASE}/api/tasks`, {
      method: "POST",
      headers: auth(),
      body: JSON.stringify({
        title: "Add auth middleware to the API",
        summary: "Goal: bearer-token middleware. Skeleton exists. Next: JWT validation and tests.",
        tags: ["backend", "auth"],
        status: "in_progress",
        agent: "claude",
        sessionId: claudeSession,
        cwd: WORK,
        model: "opus-5",
      }),
    });
    assert(r.status === 201, `task create failed: ${r.status}`);
    const task = await r.json();
    let tiles = await board();
    assert(tiles.length === 1, `expected 1 board item, got ${tiles.length}`);
    assert(tiles[0].tags.join(",") === "auth,backend", `unexpected tags: ${tiles[0].tags}`);
    assert(tiles[0].sessions.some((s) => s.sessionId === claudeSession), "creator session not bound");
    console.log(`✓ agent-created board item ${task.key} with tags and its session`);

    // 4. Now hooks land on it.
    await hook("claude", "PostToolUse", {
      session_id: claudeSession,
      cwd: WORK,
      tool_name: "Write",
      tool_input: { file_path: artifact },
    });
    r = await fetch(`${BASE}/api/tasks/${task.key}`, { headers: auth() });
    const detail = await r.json();
    assert(JSON.parse(detail.artifactsJson).files?.includes(artifact), "artifact not recorded");
    assert(detail.events.length > 0, "tool event not recorded");
    console.log("✓ bound tool events attach to the item");

    // 5. A second agent joins the same item.
    const codexSession = "e2e-codex-session";
    await hook("codex", "SessionStart", { session_id: codexSession, cwd: WORK, turn_id: "t1" });
    r = await fetch(`${BASE}/hooks/session/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: codexSession, agent: "codex", cwd: WORK }),
    });
    assert(r.ok, `session register failed: ${r.status}`);
    assert((await board()).length === 1, "a second session created a second board item");
    console.log("✓ a second session does not fork the board");

    // 6. Subagents share the parent's item.
    await hook("claude", "SubagentStart", {
      session_id: claudeSession,
      agent_id: "e2e-subagent",
      agent_type: "Explore",
      cwd: WORK,
    });
    tiles = await board();
    assert(tiles.length === 1, "a subagent created its own board item");
    assert(
      tiles[0].sessions.some((s) => s.sessionId === "e2e-subagent"),
      "subagent did not join the parent's item",
    );
    console.log("✓ subagents join the parent's item");

    // 7. The agent updates its own summary and tags.
    r = await fetch(`${BASE}/api/tasks/${task.key}`, {
      method: "PATCH",
      headers: auth(),
      body: JSON.stringify({ summary: "JWT validation landed.", status: "review", tags: ["backend", "auth", "jwt"] }),
    });
    assert(r.ok, `task patch failed: ${r.status}`);
    const patched = await r.json();
    assert(patched.handoffNote === "JWT validation landed.", "summary not stored verbatim");
    assert(patched.status === "review", "status not updated");
    console.log("✓ agent-authored summary stored verbatim");

    // 8. Tag filtering.
    assert((await board("?tag=jwt")).length === 1, "tag filter missed a tagged item");
    assert((await board("?tag=nope")).length === 0, "tag filter matched an untagged item");
    console.log("✓ tag filter");

    // 9. Session end unbinds and ingests.
    await hook("claude", "SessionEnd", { session_id: claudeSession, cwd: WORK });
    assert((await board()).length === 1, "SessionEnd removed the board item");
    console.log("✓ SessionEnd keeps the item");

    console.log("\nE2E smoke test passed");
  } finally {
    daemon.kill("SIGTERM");
    await sleep(500);
    rmSync(WORK, { recursive: true, force: true });
    if (cleanupHome) {
      try {
        rmSync(SWARM_HOME, { recursive: true, force: true });
      } catch {
        // ignore
      }
    }
    if (daemon.exitCode && daemon.exitCode !== 0 && !daemonLog.includes("listening")) {
      console.error("Daemon log:\n", daemonLog);
    }
  }
}

main().catch((e) => {
  console.error("E2E failed:", e);
  process.exit(1);
});
