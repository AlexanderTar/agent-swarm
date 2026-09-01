#!/usr/bin/env node
/**
 * MCP stdio launcher — lives inside the plugin so agents can spawn it with a
 * stable relative path (bin/swarm-mcp.mjs) instead of reaching outside the bundle.
 */
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pluginRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const swarmHome = process.env.SWARM_HOME ?? join(homedir(), ".swarm");

const candidates = [
  join(pluginRoot, "../packages/mcp-stdio/dist/index.js"),
  join(swarmHome, "app/current/packages/mcp-stdio/dist/index.js"),
];

const entry = candidates.find(existsSync);
if (!entry) {
  console.error("swarm: mcp-stdio not found. Run: swarm install && swarm plugin sync");
  process.exit(1);
}

const child = spawn(process.execPath, [entry], {
  stdio: "inherit",
  env: process.env,
});
child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  process.exit(code ?? 1);
});
