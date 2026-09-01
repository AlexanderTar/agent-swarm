#!/usr/bin/env node
/** @deprecated Use `swarm demo` instead */
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const SWARM_HOME = process.env.SWARM_HOME ?? join(homedir(), ".swarm");
const cli = join(SWARM_HOME, "app/current/packages/cli/dist/index.js");
const swarmBin = join(homedir(), ".local/bin/swarm");

const cmd = existsSync(swarmBin) ? swarmBin : existsSync(cli) ? "node" : null;
const args = cmd === "node" ? [cli, "demo", ...process.argv.slice(2)] : cmd ? [cmd, "demo", ...process.argv.slice(2)] : null;

if (!args) {
  console.error("Run from repo: pnpm demo:task  or install swarm first");
  process.exit(1);
}

const r = spawnSync(args[0]!, args.slice(1), { stdio: "inherit" });
process.exit(r.status ?? 1);
