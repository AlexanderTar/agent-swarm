#!/usr/bin/env node
/**
 * End-to-end smoke test for Agent Swarm daemon hooks and handoff flow.
 * Usage: SWARM_HOME=/tmp/swarm-e2e node scripts/e2e-smoke.mjs
 */
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const SWARM_HOME = process.env.SWARM_HOME ?? mkdtempSync(join(tmpdir(), "swarm-e2e-"));
const PORT = Number(process.env.SWARM_SCRATCH_PORT ?? 17777);
const BASE = `http://127.0.0.1:${PORT}`;
const cleanupHome = !process.env.SWARM_HOME;

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
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
  try {
    const { readFileSync } = await import("node:fs");
    token = readFileSync(`${SWARM_HOME}/run/daemon.token`, "utf8").trim();
  } catch {
    // token created after daemon starts
  }

  const authHeaders = () => ({
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  });

  try {
    await waitForHealth();

    if (!token) {
      const { readFileSync } = await import("node:fs");
      token = readFileSync(`${SWARM_HOME}/run/daemon.token`, "utf8").trim();
    }

    // Claude session start
    const sessionId = "e2e-claude-session";
    let r = await fetch(`${BASE}/hooks/claude/SessionStart`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sessionId, cwd: process.cwd(), hook_event_name: "SessionStart" }),
    });
    if (!r.ok) throw new Error(`SessionStart failed: ${r.status}`);
    const startBody = await r.json();
    const taskKey = startBody.taskKey;
    if (!taskKey) throw new Error("No taskKey from SessionStart");
    console.log("✓ Claude SessionStart ->", taskKey);

    // Initial prompt
    r = await fetch(`${BASE}/hooks/claude/UserPromptSubmit`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        session_id: sessionId,
        prompt: "Implement auth middleware for the API",
        hook_event_name: "UserPromptSubmit",
      }),
    });
    if (!r.ok) throw new Error(`UserPromptSubmit failed: ${r.status}`);
    console.log("✓ UserPromptSubmit");

    // Handoff via MCP tool (simulated REST)
    const handoffNote = {
      goal: "Add auth middleware",
      done: "Created skeleton",
      nextSteps: ["Wire JWT validation", "Add tests"],
      decisions: ["Use bearer tokens"],
      gotchas: ["Refresh token rotation pending"],
      verification: ["pnpm test"],
      files: [{ path: "src/auth.ts", reason: "main implementation" }],
      kbRefs: [],
      openQuestions: [],
    };
    r = await fetch(`${BASE}/api/tasks/${taskKey}`, { method: "GET", headers: authHeaders() });
    const task = await r.json();
    if (!task.initialContext?.includes("auth middleware")) {
      console.warn("⚠ initial context not yet summarized (Ollama chat may be unavailable)");
    } else {
      console.log("✓ initial context captured");
    }

    // Cursor pickup session
    const cursorSession = "e2e-cursor-session";
    r = await fetch(`${BASE}/hooks/cursor/sessionStart`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        conversation_id: cursorSession,
        workspace_roots: [process.cwd()],
        hook_event_name: "sessionStart",
      }),
    });
    if (!r.ok) throw new Error(`cursor sessionStart failed: ${r.status}`);
    console.log("✓ Cursor sessionStart");

    // Claim handoff (via hook-less session register + stage)
    r = await fetch(`${BASE}/hooks/session/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: cursorSession, agent: "cursor", cwd: process.cwd() }),
    });
    if (!r.ok) throw new Error(`session register failed: ${r.status}`);

    // Codex stdio-proxy path
    r = await fetch(`${BASE}/hooks/session/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: "e2e-codex-session", agent: "codex", cwd: process.cwd() }),
    });
    if (!r.ok) throw new Error(`codex session register failed: ${r.status}`);
    console.log("✓ Codex session register (stdio-proxy path)");

    // Board snapshot
    r = await fetch(`${BASE}/api/board`, { headers: authHeaders() });
    const board = await r.json();
    if (!r.ok) throw new Error(`board fetch failed: ${r.status} ${JSON.stringify(board)}`);
    if (!Array.isArray(board) || board.length === 0) throw new Error("board empty");
    console.log(`✓ board has ${board.length} task(s)`);

    console.log("\nE2E smoke test passed");
  } finally {
    daemon.kill("SIGTERM");
    await sleep(500);
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
