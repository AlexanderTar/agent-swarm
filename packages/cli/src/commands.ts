import { execSync, spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync, mkdirSync, symlinkSync, unlinkSync, cpSync, renameSync } from "node:fs";
import { randomUUID } from "node:crypto";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import * as p from "@clack/prompts";
import { DEFAULT_CONFIG, ensureSwarmDirs, getSwarmPaths, loadConfig, saveConfig, SwarmDatabase, SqliteVectorIndex, OllamaClient, KbStore, readOrCreateToken, type AgentKind, type TaskStatus } from "@swarm/core";
import {
  type CodexHooksFile,
  inspectCodexUserHooks,
  materializeCodexUserHooks,
  spliceSwarmTomlBlock,
} from "./codexHooks.js";
import { buildAntigravityHooks, inspectAntigravityHooks } from "./antigravityHooks.js";

const SWARM_HOME = process.env.SWARM_HOME ?? join(homedir(), ".swarm");
const ALL_AGENT_IDS = ["cursor", "claude", "codex", "antigravity", "opencode"] as const;

export interface InstallOptions {
  fromBootstrap?: boolean;
  yes?: boolean;
  agents?: string[];
  port?: number;
  autoUpdate?: boolean;
  pullModels?: boolean;
}

function isInteractive(): boolean {
  return Boolean(process.stdin.isTTY && process.stdout.isTTY);
}

function defaultAgentIds(): string[] {
  const detected = detectAgents().filter((a) => a.detected).map((a) => a.id);
  return detected.length > 0 ? detected : [...ALL_AGENT_IDS];
}

async function ollamaReachable(): Promise<boolean> {
  try {
    const r = await fetch("http://127.0.0.1:11434/api/tags", { signal: AbortSignal.timeout(3000) });
    return r.ok;
  } catch {
    return false;
  }
}

async function listOllamaModelBases(): Promise<Set<string>> {
  const r = await fetch("http://127.0.0.1:11434/api/tags", { signal: AbortSignal.timeout(5000) });
  if (!r.ok) return new Set();
  const data = (await r.json()) as { models?: Array<{ name: string }> };
  return new Set((data.models ?? []).map((m) => m.name.split(":")[0] ?? m.name));
}

function modelPresent(names: Set<string>, model: string): boolean {
  const base = model.split(":")[0] ?? model;
  return [...names].some((n) => n === base || n.startsWith(base));
}

async function ensureOllamaModels(config = loadConfig(), log = console.log): Promise<void> {
  const names = await listOllamaModelBases();
  for (const model of [config.embedModel, config.chatModel]) {
    if (modelPresent(names, model)) {
      log(`  ✓ ${model} already present`);
      continue;
    }
    log(`  Pulling ${model} (this may take a few minutes)...`);
    execSync(`ollama pull ${model}`, { stdio: "inherit" });
  }
}

export function mergeSentinelBlock(filePath: string, name: string, fragment: string): void {
  const start = `<!-- ${name}:start -->`;
  const end = `<!-- ${name}:end -->`;
  const block = fragment.trim().includes(start) ? fragment.trim() : `${start}\n${fragment.trim()}\n${end}`;
  let content = existsSync(filePath) ? readFileSync(filePath, "utf8") : "";
  const re = new RegExp(`${start}[\\s\\S]*?${end}`, "m");
  if (re.test(content)) {
    content = content.replace(re, block);
  } else {
    content = content.trimEnd() + (content.endsWith("\n") || content.length === 0 ? "" : "\n") + `\n${block}\n`;
  }
  mkdirSync(dirname(filePath), { recursive: true });
  writeFileSync(filePath, content);
}

export function checkCodexHooks(): { ok: boolean; detail?: string } {
  const configPath = join(homedir(), ".codex/config.toml");
  const hooksPath = join(homedir(), ".codex/hooks.json");
  if (!existsSync(join(homedir(), ".codex"))) {
    return { ok: true, detail: "Codex not installed" };
  }
  const config = existsSync(configPath) ? readFileSync(configPath, "utf8") : "";
  if (!config.includes("[mcp_servers.swarm]") && !config.includes("# swarm:start")) {
    return { ok: false, detail: "Run `swarm plugin sync` to add [mcp_servers.swarm]" };
  }
  // Bare HTTP url without auth gets 401 from the daemon; prefer stdio launcher.
  const swarmBlock = config.match(/# swarm:start[\s\S]*?# swarm:end/)?.[0] ?? "";
  const usesStdio = /command\s*=/.test(swarmBlock) || /command\s*=/.test(config.split("[mcp_servers.swarm]")[1]?.slice(0, 400) ?? "");
  const hasBearer =
    /bearer_token_env_var\s*=/.test(swarmBlock) ||
    /http_headers[\s\S]*Authorization/.test(swarmBlock) ||
    /bearer_token_env_var\s*=/.test(config);
  if (!usesStdio && !hasBearer && /url\s*=\s*"http:\/\/127\.0\.0\.1:\d+\/mcp"/.test(config)) {
    return { ok: false, detail: "Codex MCP uses unauthenticated HTTP — run `swarm plugin sync`" };
  }
  if (!existsSync(hooksPath)) {
    return { ok: false, detail: "Missing ~/.codex/hooks.json — run `swarm plugin sync`" };
  }
  const inspected = inspectCodexUserHooks(readFileSync(hooksPath, "utf8"));
  if (!inspected.ok) return inspected;
  return {
    ok: true,
    detail: "Approve swarm hooks in Codex /hooks TUI or sessions won't appear on the board",
  };
}

export function checkCursorHooks(): { ok: boolean; detail?: string } {
  if (!existsSync(join(homedir(), ".cursor"))) {
    return { ok: true, detail: "Cursor not installed" };
  }
  const hooksPath = join(homedir(), ".cursor/hooks.json");
  if (!existsSync(hooksPath)) {
    return { ok: false, detail: "Missing ~/.cursor/hooks.json — run `swarm plugin sync`" };
  }
  const content = readFileSync(hooksPath, "utf8");
  if (!content.includes("post-hook.mjs") || !content.includes("sessionStart")) {
    return { ok: false, detail: "Swarm sessionStart hook not merged into ~/.cursor/hooks.json" };
  }
  if (content.includes("post-hook.mjs cursor") && !content.includes("node ")) {
    return { ok: false, detail: "Cursor hooks must invoke node — run `swarm plugin sync`" };
  }
  return { ok: true, detail: "Restart Cursor after sync for hooks to load" };
}

export function checkCursorMcp(pluginPath: string): { ok: boolean; detail?: string } {
  if (!existsSync(join(homedir(), ".cursor"))) {
    return { ok: true, detail: "Cursor not installed" };
  }
  const manifest = join(pluginPath, ".cursor-plugin/plugin.json");
  if (!existsSync(manifest)) {
    return { ok: false, detail: "Missing .cursor-plugin/plugin.json in swarm plugin" };
  }
  try {
    const plugin = JSON.parse(readFileSync(manifest, "utf8")) as { mcpServers?: string };
    if (!plugin.mcpServers) {
      return { ok: false, detail: "Cursor plugin manifest missing mcpServers — run `swarm plugin sync`" };
    }
  } catch {
    return { ok: false, detail: "Invalid .cursor-plugin/plugin.json" };
  }
  const mcpPath = join(homedir(), ".cursor/mcp.json");
  if (!existsSync(mcpPath)) {
    return { ok: false, detail: "Missing ~/.cursor/mcp.json — run `swarm plugin sync`" };
  }
  try {
    const mcp = JSON.parse(readFileSync(mcpPath, "utf8")) as { mcpServers?: Record<string, unknown> };
    if (!mcp.mcpServers?.swarm) {
      return { ok: false, detail: "Swarm MCP not merged into ~/.cursor/mcp.json — run `swarm plugin sync`" };
    }
  } catch {
    return { ok: false, detail: "Invalid ~/.cursor/mcp.json" };
  }
  return { ok: true, detail: "Enable the swarm plugin in Cursor Settings → Plugins" };
}

export function checkAntigravityPlugin(pluginPath: string): { ok: boolean; detail?: string } {
  if (!existsSync(join(homedir(), ".gemini"))) {
    return { ok: true, detail: "Antigravity not installed" };
  }
  const link = join(homedir(), ".gemini/config/plugins/swarm");
  if (!existsSync(link)) {
    return { ok: false, detail: "Missing ~/.gemini/config/plugins/swarm symlink" };
  }
  const mcpPath = join(homedir(), ".gemini/antigravity/mcp_config.json");
  if (!existsSync(mcpPath)) {
    return { ok: false, detail: "Missing ~/.gemini/antigravity/mcp_config.json" };
  }
  const gemini = readFileSync(join(homedir(), ".gemini/GEMINI.md"), "utf8");
  if (!gemini.includes("<!-- swarm:start -->")) {
    return { ok: false, detail: "GEMINI.md missing swarm sentinel block" };
  }
  if (!existsSync(join(pluginPath, "hooks.json"))) {
    return { ok: false, detail: "plugin/hooks.json missing" };
  }
  const inspected = inspectAntigravityHooks(readFileSync(join(pluginPath, "hooks.json"), "utf8"));
  if (!inspected.ok) return inspected;
  const pluginMcp = join(link, "mcp.json");
  if (existsSync(pluginMcp)) {
    try {
      const mcp = JSON.parse(readFileSync(pluginMcp, "utf8")) as { mcpServers?: { swarm?: { args?: string[] } } };
      const arg = mcp.mcpServers?.swarm?.args?.[0] ?? "";
      if (arg && !arg.startsWith("/")) {
        return { ok: false, detail: "Plugin MCP uses relative path — run `swarm plugin sync`" };
      }
    } catch {
      return { ok: false, detail: "Invalid ~/.gemini/config/plugins/swarm/mcp.json" };
    }
  }
  return { ok: true };
}

export function checkOpencodePlugin(): { ok: boolean; detail?: string } {
  const root = join(homedir(), ".config/opencode");
  if (!existsSync(root)) return { ok: true, detail: "not installed" };
  if (!existsSync(join(root, "plugins/swarm.js"))) {
    return { ok: false, detail: "missing ~/.config/opencode/plugins/swarm.js — run: swarm plugin sync" };
  }
  const configPath = join(root, "opencode.json");
  if (!existsSync(configPath)) return { ok: false, detail: "missing ~/.config/opencode/opencode.json" };
  try {
    const config = JSON.parse(readFileSync(configPath, "utf8")) as { mcp?: Record<string, unknown> };
    if (!config.mcp?.swarm) return { ok: false, detail: "opencode.json has no mcp.swarm entry" };
  } catch {
    return { ok: false, detail: "Invalid ~/.config/opencode/opencode.json" };
  }
  return { ok: true };
}

export function detectAgents(): Array<{ id: string; name: string; detected: boolean }> {
  return [
    { id: "cursor", name: "Cursor", detected: existsSync(join(homedir(), ".cursor")) },
    { id: "claude", name: "Claude Code", detected: existsSync(join(homedir(), ".claude")) },
    { id: "codex", name: "Codex CLI", detected: existsSync(join(homedir(), ".codex")) },
    { id: "antigravity", name: "Antigravity", detected: existsSync(join(homedir(), ".gemini")) },
    { id: "opencode", name: "OpenCode", detected: existsSync(join(homedir(), ".config/opencode")) },
  ];
}

export function resolveNode(): string {
  const candidates = [
    join(homedir(), ".local/share/fnm/aliases/default/bin/node"),
    join(homedir(), ".volta/bin/node"),
    "/opt/homebrew/bin/node",
    "/usr/local/bin/node",
  ];
  for (const c of candidates) {
    if (existsSync(c)) return c;
  }
  return "node";
}

export function writeStartScript(): void {
  const bin = join(SWARM_HOME, "bin");
  mkdirSync(bin, { recursive: true });
  const script = `#!/bin/bash
set -euo pipefail
eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || true)"
export PATH="$HOME/.local/share/fnm/aliases/default/bin:$HOME/.volta/bin:$HOME/.local/bin:$PATH"
exec "$(command -v node)" "$HOME/.swarm/app/current/packages/daemon/dist/index.js"
`;
  writeFileSync(join(bin, "swarmd-start.sh"), script, { mode: 0o755 });
}

export function writeSwarmCli(): string {
  const bin = join(SWARM_HOME, "bin");
  mkdirSync(bin, { recursive: true });
  const cliPath = join(bin, "swarm");
  const script = `#!/bin/bash
set -euo pipefail
eval "$(/opt/homebrew/bin/brew shellenv 2>/dev/null || true)"
export PATH="$HOME/.local/share/fnm/aliases/default/bin:$HOME/.volta/bin:$HOME/.local/bin:$PATH"
export SWARM_HOME="\${SWARM_HOME:-$HOME/.swarm}"
exec "$(command -v node)" "\$SWARM_HOME/app/current/packages/cli/dist/index.js" "$@"
`;
  writeFileSync(cliPath, script, { mode: 0o755 });
  return cliPath;
}

export function linkSwarmCli(): { linked: string; pathHint?: string } {
  const cliPath = writeSwarmCli();
  const localBin = join(homedir(), ".local/bin");
  mkdirSync(localBin, { recursive: true });
  const dest = join(localBin, "swarm");
  symlinkForce(cliPath, dest);

  const pathEntries = (process.env.PATH ?? "").split(":");
  const onPath = pathEntries.some((p) => p === localBin || p === join(homedir(), ".local/bin"));
  return {
    linked: dest,
    pathHint: onPath ? undefined : `Add to ~/.zshrc:  export PATH="$HOME/.local/bin:$PATH"`,
  };
}

export function unlinkSwarmCli(): void {
  const dest = join(homedir(), ".local/bin/swarm");
  try {
    unlinkSync(dest);
  } catch {
    // ignore
  }
}

export function writeDaemonPlist(): void {
  const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.swarm.daemon</string>
  <key>ProgramArguments</key>
  <array><string>${join(SWARM_HOME, "bin/swarmd-start.sh")}</string></array>
  <key>WorkingDirectory</key><string>${SWARM_HOME}</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
    <key>Crashed</key><true/>
  </dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>${join(SWARM_HOME, "logs/daemon.out.log")}</string>
  <key>StandardErrorPath</key><string>${join(SWARM_HOME, "logs/daemon.err.log")}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SWARM_HOME</key><string>${SWARM_HOME}</string>
    <key>NODE_ENV</key><string>production</string>
  </dict>
</dict>
</plist>`;
  writeFileSync(join(homedir(), "Library/LaunchAgents/dev.swarm.daemon.plist"), plist);
}

export function writeUpdaterPlist(enabled: boolean): void {
  if (!enabled) {
    const p = join(homedir(), "Library/LaunchAgents/dev.swarm.updater.plist");
    if (existsSync(p)) unlinkSync(p);
    return;
  }
  const script = join(SWARM_HOME, "bin/swarm-update.sh");
  writeFileSync(
    script,
    `#!/bin/bash
set -euo pipefail
export PATH="$HOME/.local/share/fnm/aliases/default/bin:$HOME/.volta/bin:$PATH"
exec "$(command -v node)" "$HOME/.swarm/app/current/packages/cli/dist/index.js" update --quiet
`,
    { mode: 0o755 },
  );
  const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.swarm.updater</string>
  <key>ProgramArguments</key>
  <array><string>${script}</string></array>
  <key>StartInterval</key><integer>3600</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>${join(SWARM_HOME, "logs/updater.out.log")}</string>
  <key>StandardErrorPath</key><string>${join(SWARM_HOME, "logs/updater.err.log")}</string>
</dict>
</plist>`;
  writeFileSync(join(homedir(), "Library/LaunchAgents/dev.swarm.updater.plist"), plist);
}

export function bootstrapLaunchd(): void {
  const uid = process.getuid?.() ?? "";
  for (const label of ["dev.swarm.daemon", "dev.swarm.updater"]) {
    const plist = join(homedir(), "Library/LaunchAgents", `${label}.plist`);
    if (!existsSync(plist)) continue;
    spawnSync("launchctl", ["bootout", `gui/${uid}/${label}`], { stdio: "ignore" });
    spawnSync("launchctl", ["bootstrap", `gui/${uid}`, plist], { stdio: "inherit" });
  }
  spawnSync("launchctl", ["kickstart", "-k", `gui/${uid}/dev.swarm.daemon`], { stdio: "inherit" });
}

async function checkDaemonHealth(port = loadConfig().port): Promise<boolean> {
  try {
    const r = await fetch(`http://127.0.0.1:${port}/api/health`, { signal: AbortSignal.timeout(2000) });
    return r.ok;
  } catch {
    return false;
  }
}

function symlinkForce(target: string, link: string): void {
  mkdirSync(join(link, ".."), { recursive: true });
  try {
    unlinkSync(link);
  } catch {
    // ignore
  }
  symlinkSync(target, link);
}

function swarmMcpLauncher(pluginPath: string): string {
  return join(pluginPath, "bin/swarm-mcp.mjs");
}

function writePluginMcpConfig(pluginPath: string): void {
  const port = loadConfig().port;
  const launcher = swarmMcpLauncher(pluginPath);
  const config = {
    $schema: "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
    mcpServers: {
      swarm: {
        type: "stdio",
        command: "node",
        args: [launcher],
        env: {
          SWARM_URL: `http://127.0.0.1:${port}`,
          SWARM_REGISTER_SESSION: "0",
        },
      },
    },
  };
  const json = `${JSON.stringify(config, null, 2)}\n`;
  for (const name of ["mcp.json", ".mcp.json", "mcp_config.json"]) {
    writeFileSync(join(pluginPath, name), json);
  }
  const cursorPluginMcp = join(pluginPath, ".cursor-plugin/mcp.json");
  mkdirSync(dirname(cursorPluginMcp), { recursive: true });
  writeFileSync(cursorPluginMcp, json);
}

export function pluginSync(agents: string[]): void {
  const pluginPath = join(SWARM_HOME, "app/current/plugin");
  if (!existsSync(pluginPath)) {
    // dev: use repo plugin
    const devPlugin = join(process.cwd(), "plugin");
    if (existsSync(devPlugin)) symlinkForce(devPlugin, pluginPath);
  }

  writePluginMcpConfig(pluginPath);
  if (agents.includes("cursor")) {
    const dest = join(homedir(), ".cursor/plugins/local/swarm");
    mkdirSync(join(dest, ".."), { recursive: true });
    symlinkForce(pluginPath, dest);
    mergeCursorHooks(pluginPath);
    mergeCursorMcp(dest);
  }
  if (agents.includes("claude")) {
    symlinkForce(pluginPath, join(homedir(), ".claude/skills/swarm"));
  }
  if (agents.includes("antigravity")) {
    mkdirSync(join(homedir(), ".gemini/config/plugins"), { recursive: true });
    symlinkForce(pluginPath, join(homedir(), ".gemini/config/plugins/swarm"));
    mergeAntigravityConfig(pluginPath);
  }
  if (agents.includes("codex")) {
    mergeCodexConfig(pluginPath);
  }
  if (agents.includes("opencode")) {
    mergeOpencodeConfig(pluginPath);
  }
}

/** OpenCode loads plugins from ~/.config/opencode/plugins and MCP servers from opencode.json. */
function mergeOpencodeConfig(pluginPath: string): void {
  const root = join(homedir(), ".config/opencode");
  const pluginSource = join(pluginPath, "opencode/swarm.js");
  if (existsSync(pluginSource)) {
    mkdirSync(join(root, "plugins"), { recursive: true });
    symlinkForce(pluginSource, join(root, "plugins/swarm.js"));
  }

  const configPath = join(root, "opencode.json");
  mkdirSync(root, { recursive: true });
  let existing: Record<string, unknown> = {};
  if (existsSync(configPath)) {
    try {
      existing = JSON.parse(readFileSync(configPath, "utf8")) as Record<string, unknown>;
    } catch {
      existing = {};
    }
  }

  const port = loadConfig().port;
  const mcp = { ...((existing.mcp as Record<string, unknown>) ?? {}) };
  mcp.swarm = {
    type: "local",
    command: ["node", swarmMcpLauncher(pluginPath)],
    enabled: true,
    environment: {
      SWARM_URL: `http://127.0.0.1:${port}`,
      SWARM_AGENT: "opencode",
      SWARM_REGISTER_SESSION: "0",
    },
  };

  const agentsMd = join(pluginPath, "AGENTS.md");
  const instructions = new Set(
    Array.isArray(existing.instructions) ? (existing.instructions as string[]) : [],
  );
  if (existsSync(agentsMd)) instructions.add(agentsMd);

  const merged = {
    $schema: "https://opencode.ai/config.json",
    ...existing,
    mcp,
    instructions: [...instructions],
  };
  writeFileSync(configPath, `${JSON.stringify(merged, null, 2)}\n`);
}

function mergeCodexConfig(pluginPath: string): void {
  const configPath = join(homedir(), ".codex/config.toml");
  const hooksPath = join(homedir(), ".codex/hooks.json");
  const launcher = swarmMcpLauncher(pluginPath);
  const port = loadConfig().port;
  // Prefer stdio (reads ~/.swarm/run/daemon.token) — bare HTTP url gets 401.
  const mcpBlock = `# swarm:start
[mcp_servers.swarm]
command = "node"
args = ["${launcher.replace(/\\/g, "/")}"]

[mcp_servers.swarm.env]
SWARM_URL = "http://127.0.0.1:${port}"
SWARM_AGENT = "codex"
SWARM_REGISTER_SESSION = "0"
# swarm:end`;

  const content = existsSync(configPath) ? readFileSync(configPath, "utf8") : "";
  writeFileSync(configPath, spliceSwarmTomlBlock(content, mcpBlock));

  const pluginHooks = join(pluginPath, ".codex-plugin/hooks.json");
  if (existsSync(pluginHooks)) {
    const incoming = JSON.parse(readFileSync(pluginHooks, "utf8")) as CodexHooksFile;
    let existing: CodexHooksFile = {};
    if (existsSync(hooksPath)) {
      try {
        existing = JSON.parse(readFileSync(hooksPath, "utf8")) as CodexHooksFile;
      } catch {
        existing = {};
      }
    }
    const materialized = materializeCodexUserHooks(incoming, pluginPath);
    const merged = { hooks: { ...(existing.hooks ?? {}), ...(materialized.hooks ?? {}) } };
    writeFileSync(hooksPath, `${JSON.stringify(merged, null, 2)}\n`);
  }

  p.log.warn("Codex: approve plugin hooks in the /hooks TUI or sessions won't appear on the board.");
}

type CursorHookDef = { command: string; timeout?: number };

function isSwarmCursorHook(command: string): boolean {
  return command.includes("post-hook.mjs") && command.includes(" cursor ");
}

function buildSwarmCursorHooks(pluginPath: string): Record<string, CursorHookDef[]> {
  const script = join(pluginPath, "hooks/post-hook.mjs");
  const defs = (event: string, timeout?: number): CursorHookDef[] => [
    { command: `node "${script}" cursor ${event}`, ...(timeout ? { timeout } : {}) },
  ];
  return {
    sessionStart: defs("sessionStart"),
    sessionEnd: defs("sessionEnd", 2),
    beforeSubmitPrompt: defs("beforeSubmitPrompt"),
    preToolUse: defs("preToolUse", 2),
    postToolUse: defs("postToolUse", 2),
    afterFileEdit: defs("afterFileEdit", 2),
    subagentStart: defs("subagentStart", 2),
    subagentStop: defs("subagentStop", 2),
    preCompact: defs("preCompact"),
    stop: defs("stop"),
  };
}

function mergeCursorHooks(pluginPath: string): void {
  const hooksPath = join(homedir(), ".cursor/hooks.json");
  mkdirSync(dirname(hooksPath), { recursive: true });
  const swarmHooks = buildSwarmCursorHooks(pluginPath);

  let existing: { version?: number; hooks?: Record<string, CursorHookDef[]> } = {};
  if (existsSync(hooksPath)) {
    try {
      existing = JSON.parse(readFileSync(hooksPath, "utf8")) as { version?: number; hooks?: Record<string, CursorHookDef[]> };
    } catch {
      existing = {};
    }
  }

  const mergedHooks: Record<string, CursorHookDef[]> = { ...(existing.hooks ?? {}) };
  for (const [event, defs] of Object.entries(swarmHooks)) {
    const kept = (mergedHooks[event] ?? []).filter((d) => !isSwarmCursorHook(d.command));
    mergedHooks[event] = [...kept, ...defs];
  }

  writeFileSync(hooksPath, `${JSON.stringify({ version: 1, hooks: mergedHooks }, null, 2)}\n`);
}

function mergeCursorMcp(pluginPath: string): void {
  const launcher = swarmMcpLauncher(pluginPath);
  if (!existsSync(launcher)) return;

  const mcpPath = join(homedir(), ".cursor/mcp.json");
  mkdirSync(dirname(mcpPath), { recursive: true });
  const port = loadConfig().port;

  let existing: { mcpServers?: Record<string, unknown>; _comments?: Record<string, unknown> } = {};
  if (existsSync(mcpPath)) {
    try {
      existing = JSON.parse(readFileSync(mcpPath, "utf8")) as typeof existing;
    } catch {
      existing = {};
    }
  }

  const merged = {
    ...existing,
    mcpServers: {
      ...(existing.mcpServers ?? {}),
      swarm: {
        type: "stdio",
        command: "node",
        args: [launcher],
        env: {
          SWARM_URL: `http://127.0.0.1:${port}`,
          SWARM_REGISTER_SESSION: "0",
        },
      },
    },
  };
  writeFileSync(mcpPath, `${JSON.stringify(merged, null, 2)}\n`);
}

function mergeAntigravityConfig(pluginPath: string): void {
  const port = loadConfig().port;
  const launcher = swarmMcpLauncher(pluginPath);
  const mcpPath = join(homedir(), ".gemini/antigravity/mcp_config.json");
  mkdirSync(dirname(mcpPath), { recursive: true });

  let existing: { mcpServers?: Record<string, unknown> } = {};
  if (existsSync(mcpPath)) {
    try {
      existing = JSON.parse(readFileSync(mcpPath, "utf8")) as { mcpServers?: Record<string, unknown> };
    } catch {
      existing = {};
    }
  }

  const swarmServer = {
    command: "node",
    args: [launcher],
    env: {
      SWARM_URL: `http://127.0.0.1:${port}`,
      SWARM_AGENT: "antigravity",
      SWARM_REGISTER_SESSION: "0",
    },
  };

  const merged = {
    mcpServers: {
      ...(existing.mcpServers ?? {}),
      swarm: swarmServer,
    },
  };
  writeFileSync(mcpPath, `${JSON.stringify(merged, null, 2)}\n`);

  writeFileSync(join(pluginPath, "hooks.json"), `${JSON.stringify(buildAntigravityHooks(pluginPath), null, 2)}\n`);

  const fragmentPath = join(pluginPath, "GEMINI.md");
  if (existsSync(fragmentPath)) {
    mergeSentinelBlock(join(homedir(), ".gemini/GEMINI.md"), "swarm", readFileSync(fragmentPath, "utf8"));
  }
}

export interface DemoOptions {
  title?: string;
  status?: TaskStatus;
  agent?: AgentKind;
  context?: string;
}

function swarmBaseUrl(): string {
  const paths = getSwarmPaths(SWARM_HOME);
  const config = loadConfig(SWARM_HOME);
  let port = config.port;
  if (existsSync(join(paths.run, "daemon.port"))) {
    port = Number(readFileSync(join(paths.run, "daemon.port"), "utf8").trim()) || port;
  }
  if (process.env.SWARM_URL) return process.env.SWARM_URL.replace(/\/$/, "");
  return `http://127.0.0.1:${port}`;
}

function swarmAuthHeaders(): Record<string, string> {
  const paths = getSwarmPaths(SWARM_HOME);
  const token = process.env.SWARM_TOKEN ?? (existsSync(join(paths.run, "daemon.token"))
    ? readFileSync(join(paths.run, "daemon.token"), "utf8").trim()
    : readOrCreateToken(paths));
  return {
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
}

export async function runDemo(opts: DemoOptions = {}): Promise<void> {
  const base = swarmBaseUrl();
  const title = opts.title ?? "Demo: explore Agent Swarm board";
  const status = opts.status ?? "ready";
  const agent = opts.agent ?? "cursor";
  const context = opts.context ?? "Demo task from `swarm demo`. Open the board and drag this card between columns.";

  const health = await fetch(`${base}/api/health`, { signal: AbortSignal.timeout(3000) });
  if (!health.ok) {
    console.error(`Daemon not healthy at ${base} (${health.status})`);
    const uid = process.getuid?.() ?? "";
    console.error(`Try: launchctl kickstart -k gui/${uid}/dev.swarm.daemon`);
    process.exit(1);
  }

  const res = await fetch(`${base}/api/tasks`, {
    method: "POST",
    headers: swarmAuthHeaders(),
    body: JSON.stringify({
      title,
      status,
      initialContext: context,
      agent,
      sessionId: `demo-${randomUUID()}`,
      cwd: process.cwd(),
      model: "demo-probe",
    }),
  });

  const body = await res.json() as { key?: string; title?: string; status?: string; error?: string };
  if (!res.ok) {
    console.error("Failed to create demo task:", res.status, body);
    process.exit(1);
  }

  console.log("Created demo task:");
  console.log(`  Key:     ${body.key}`);
  console.log(`  Title:   ${body.title}`);
  console.log(`  Status:  ${body.status}`);
  console.log(`  Board:   ${base}/`);
}

export async function runKbReindex(): Promise<void> {
  const config = loadConfig(SWARM_HOME);
  const paths = getSwarmPaths(SWARM_HOME);
  const db = new SwarmDatabase(paths.db, config.embedDimensions);
  const kb = new KbStore(db.db, new SqliteVectorIndex(db.db), new OllamaClient(config), paths);
  const count = await kb.reindexAll();
  console.log(`Re-embedded ${count} markdown file(s) under ${paths.kb}`);
  db.close();
}

function printInstallGuide(port: number, agents: string[], autoUpdate: boolean, pathHint?: string): void {
  const boardUrl = `http://127.0.0.1:${port}`;
  const uid = process.getuid?.() ?? "$(id -u)";

  console.log(`
══════════════════════════════════════════════════════════════
  Agent Swarm — installed
══════════════════════════════════════════════════════════════

  Dashboard (Kanban board)
    ${boardUrl}

  CLI (new shell, or: export PATH="$HOME/.local/bin:$PATH")
    swarm open                       Open the board
    swarm status                     Daemon PID, port, paths
    swarm doctor                     Health check (--hooks to test hooks)
    swarm demo                       Create a demo task on the board
    swarm plugin sync                Re-link plugins after manual edits
    swarm kb reindex                 Re-embed ~/.swarm/kb
${pathHint ? `\n  ⚠ ${pathHint}\n` : ""}\
  Wired agents
    ${agents.join(", ")}

  MCP tools (call from any wired agent)
    swarm_board          View the Kanban board
    swarm_task_create    Create a board item you name and summarise yourself
    swarm_task_update    Keep its title, summary, status and tags current
    swarm_task_join      Put a second agent on the same item
    swarm_handoff        Write a handoff note and move the item to ready
    swarm_pickup         Claim a handoff and get a pickup prompt
    swarm_memory_write   Save a durable fact to the knowledge base
    swarm_kb_search      Semantic search over ~/.swarm/kb

  Workflow
    1. Hooks track your session but never create a board item
    2. Call swarm_task_create when work is worth coordinating
    3. swarm_task_update as the picture changes; swarm_handoff before switching agents
    4. In the next agent: swarm_pickup or /pickup to claim and continue

  Files & data
    ~/.swarm/                        Home (config, DB, KB, logs)
    ~/.swarm/kb/                     Markdown knowledge base
    ~/.swarm/run/daemon.token        API bearer token (loopback only)

  Daemon management (launchd)
    launchctl kickstart -k gui/${uid}/dev.swarm.daemon   Restart daemon
    tail -f ~/.swarm/logs/daemon.err.log               Error log
${autoUpdate ? `    Auto-update: enabled (hourly via dev.swarm.updater)\n` : "    Auto-update: disabled\n"}\
${agents.includes("codex") ? `\
  Codex (required manual step)
    Open Codex and run /hooks → approve swarm plugin hooks
    Without this, Codex sessions will NOT appear on the board.
` : ""}\
  Requirements
    Ollama must stay running: ollama serve
    Models: nomic-embed-text (embeddings), qwen3:4b (board Console chat)

══════════════════════════════════════════════════════════════
`);
}

export function rebuildNativeModules(cwd = join(SWARM_HOME, "app/current")): void {
  const coreDir = join(cwd, "packages/core");
  if (!existsSync(join(coreDir, "package.json"))) return;
  console.log("Building native modules (better-sqlite3)...");
  execSync("npm rebuild better-sqlite3", { cwd: coreDir, stdio: "inherit" });
}

export function verifyNativeModules(cwd = join(SWARM_HOME, "app/current")): boolean {
  try {
    const coreDir = join(cwd, "packages/core");
    execSync('node -e "require(\\"better-sqlite3\\")"', { cwd: coreDir, stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

export async function runInstall(options: InstallOptions = {}): Promise<void> {
  const fromBootstrap = options.fromBootstrap ?? false;
  const nonInteractive = options.yes ?? (fromBootstrap && !isInteractive());

  if (nonInteractive) {
    console.log("Agent Swarm setup");
  } else {
    p.intro("Agent Swarm setup");
  }

  const nodeOk = Number(process.version.slice(1).split(".")[0]) >= 20;
  if (!nodeOk) {
    const msg = "Node 20+ required";
    if (nonInteractive) {
      console.error(msg);
    } else {
      p.cancel(msg);
    }
    process.exit(1);
  }

  if (!(await ollamaReachable())) {
    const msg = "Ollama is required. Run: ollama serve && ollama pull nomic-embed-text && ollama pull qwen3:4b";
    if (nonInteractive) {
      console.error(msg);
    } else {
      p.cancel(msg);
    }
    process.exit(1);
  }

  let selected: string[];
  let port: number;
  let autoUpdate: boolean;

  if (nonInteractive) {
    selected = options.agents ?? defaultAgentIds();
    port = options.port ?? DEFAULT_CONFIG.port;
    autoUpdate = options.autoUpdate ?? DEFAULT_CONFIG.autoUpdate;
    console.log(`  Agents: ${selected.join(", ")}`);
    console.log(`  Port: ${port}`);
    console.log(`  Auto-update: ${autoUpdate ? "on" : "off"}`);
    if (options.pullModels !== false) {
      console.log("Checking Ollama models...");
      await ensureOllamaModels();
    }
  } else {
    const detected = detectAgents().filter((a) => a.detected);
    const agentOptions = detected.length > 0
      ? detected.map((a) => ({ value: a.id, label: a.name }))
      : ALL_AGENT_IDS.map((id) => ({ value: id, label: id }));

    const picked = await p.multiselect({
      message: "Wire which agents?",
      options: agentOptions,
      initialValues: agentOptions.map((a) => a.value),
      required: true,
    });
    if (p.isCancel(picked)) process.exit(0);
    selected = picked as string[];

    const portInput = await p.text({ message: "Daemon port", initialValue: String(DEFAULT_CONFIG.port) });
    if (p.isCancel(portInput)) process.exit(0);
    port = Number(portInput);

    const autoUpdateInput = await p.confirm({ message: "Enable hourly auto-update?", initialValue: true });
    if (p.isCancel(autoUpdateInput)) process.exit(0);
    autoUpdate = Boolean(autoUpdateInput);

    const config = loadConfig();
    const names = await listOllamaModelBases();
    const missing = [config.embedModel, config.chatModel].filter((m) => !modelPresent(names, m));
    if (missing.length > 0) {
      const pull = await p.confirm({
        message: `Pull missing models (${missing.join(", ")})?`,
        initialValue: true,
      });
      if (p.isCancel(pull)) process.exit(0);
      if (pull) await ensureOllamaModels(config, (line) => p.log.info(line));
    }
  }

  ensureSwarmDirs();
  const config = loadConfig();
  config.port = port;
  config.autoUpdate = autoUpdate;
  saveConfig(config);

  if (!fromBootstrap) {
    const repoRoot = join(process.cwd());
    mkdirSync(join(SWARM_HOME, "app/releases/dev"), { recursive: true });
    cpSync(repoRoot, join(SWARM_HOME, "app/releases/dev"), { recursive: true });
    const tmp = join(SWARM_HOME, "app/current.new");
    symlinkForce(join(SWARM_HOME, "app/releases/dev"), tmp);
    renameSync(tmp, join(SWARM_HOME, "app/current"));
  }

  if (nonInteractive) console.log("Writing launchd jobs and syncing plugins...");
  const releaseRoot = fromBootstrap ? join(SWARM_HOME, "app/current") : join(SWARM_HOME, "app/releases/dev");
  if (!verifyNativeModules(releaseRoot)) {
    rebuildNativeModules(releaseRoot);
  }
  writeStartScript();
  writeDaemonPlist();
  writeUpdaterPlist(autoUpdate);
  const { pathHint } = linkSwarmCli();
  pluginSync(selected);
  bootstrapLaunchd();

  const codexCheck = checkCodexHooks();
  if (selected.includes("codex")) {
    const msg = codexCheck.detail ?? (codexCheck.ok ? "Codex hooks merged" : "Codex hooks not configured");
    if (nonInteractive) {
      console.log(codexCheck.ok ? `  ✓ ${msg}` : `  ⚠ ${msg}`);
    } else if (!codexCheck.ok) {
      p.log.warn(msg);
    } else {
      p.log.info(msg);
    }
  }

  const outro = `Agent Swarm ready at http://127.0.0.1:${port}`;
  if (nonInteractive) {
    console.log(`\n${outro}`);
  } else {
    p.outro(outro);
  }
  printInstallGuide(port, selected, autoUpdate, pathHint);
}

export async function runDoctor(hooks = false): Promise<number> {
  let errors = 0;
  const paths = getSwarmPaths();

  const checks: Array<[string, boolean, string?]> = [];

  checks.push(["~/.swarm exists", existsSync(paths.home)]);
  checks.push(["current symlink", existsSync(paths.current)]);
  checks.push(["daemon token", existsSync(join(paths.run, "daemon.token"))]);

  try {
    let daemonOk = await checkDaemonHealth();
    if (!daemonOk && existsSync(paths.current)) {
      if (!verifyNativeModules(paths.current)) {
        rebuildNativeModules(paths.current);
      }
      bootstrapLaunchd();
      await new Promise((resolve) => setTimeout(resolve, 2000));
      daemonOk = await checkDaemonHealth();
    }
    checks.push(["daemon health", daemonOk, daemonOk ? undefined : "not reachable"]);
  } catch {
    checks.push(["daemon health", false, "not reachable"]);
  }

  try {
    const db = new SwarmDatabase(join(paths.home, "doctor-test.db"), 256);
    db.close();
    unlinkSync(join(paths.home, "doctor-test.db"));
    checks.push(["sqlite-vec ABI", true]);
  } catch (e) {
    checks.push(["sqlite-vec ABI", false, String(e)]);
  }

  const current = paths.current;
  if (existsSync(current) && !verifyNativeModules(current)) {
    checks.push(["better-sqlite3 native", false, "Run: npm rebuild better-sqlite3 --prefix ~/.swarm/app/current/packages/core"]);
  } else if (existsSync(current)) {
    checks.push(["better-sqlite3 native", true]);
  }

  try {
    const r = await fetch("http://127.0.0.1:11434/api/tags", { signal: AbortSignal.timeout(2000) });
    checks.push(["ollama", r.ok]);
  } catch {
    checks.push(["ollama", false]);
  }

  const pluginPath = existsSync(join(paths.home, "app/current/plugin"))
    ? join(paths.home, "app/current/plugin")
    : join(process.cwd(), "plugin");
  const codex = checkCodexHooks();
  checks.push(["codex hooks", codex.ok, codex.detail]);
  const cursor = checkCursorHooks();
  checks.push(["cursor hooks", cursor.ok, cursor.detail]);
  const cursorMcp = checkCursorMcp(pluginPath);
  checks.push(["cursor mcp", cursorMcp.ok, cursorMcp.detail]);
  const ag = checkAntigravityPlugin(pluginPath);
  checks.push(["antigravity plugin", ag.ok, ag.detail]);
  const opencode = checkOpencodePlugin();
  checks.push(["opencode plugin", opencode.ok, opencode.detail]);

  for (const [name, ok, detail] of checks) {
    console.log(`${ok ? "✓" : "✗"} ${name}${detail ? `: ${detail}` : ""}`);
    if (!ok) errors++;
  }

  if (hooks) {
    const claudePayload = { session_id: "doctor-test", cwd: process.cwd(), hook_event_name: "SessionStart" };
    const cursorPayload = { conversation_id: "doctor-cursor", workspace_roots: [process.cwd()], hook_event_name: "sessionStart" };
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/claude/SessionStart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(claudePayload),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay Claude SessionStart`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay Claude SessionStart");
      errors++;
    }
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/cursor/sessionStart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(cursorPayload),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay Cursor sessionStart`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay Cursor sessionStart");
      errors++;
    }
    const codexPayload = {
      session_id: "doctor-codex",
      cwd: process.cwd(),
      hook_event_name: "SessionStart",
      turn_id: "doctor-turn",
    };
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/codex/SessionStart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(codexPayload),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay Codex SessionStart`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay Codex SessionStart");
      errors++;
    }
    const antigravityPayload = {
      conversationId: "doctor-antigravity",
      workspacePaths: [process.cwd()],
      hook_event_name: "PreInvocation",
      invocationNum: 0,
    };
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/antigravity/PreInvocation", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(antigravityPayload),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay Antigravity PreInvocation`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay Antigravity PreInvocation");
      errors++;
    }
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/opencode/SessionStart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_id: "doctor-opencode", cwd: process.cwd(), opencode: true }),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay OpenCode SessionStart`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay OpenCode SessionStart");
      errors++;
    }
  }

  return errors;
}

export async function runUpdate(opts: { to?: string; quiet?: boolean }): Promise<void> {
  const config = loadConfig();
  const repo = config.repoUrl;
  const releasesDir = join(SWARM_HOME, "app/releases");

  let tag = opts.to;
  if (!tag) {
    const out = execSync(`git ls-remote --tags --refs ${repo}`, { encoding: "utf8" });
    const tags = out
      .split("\n")
      .map((l) => l.split("/").pop())
      .filter((t): t is string => !!t && /^v\d+\.\d+\.\d+$/.test(t))
      .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    tag = tags.at(-1) ?? "main";
  }

  const current = existsSync(join(SWARM_HOME, "app/current"))
    ? readFileSync(join(SWARM_HOME, "app/current/package.json"), "utf8")
    : "";
  if (current.includes(tag) && !opts.to) {
    if (!opts.quiet) console.log("Already on latest tag");
    return;
  }

  const dest = join(releasesDir, tag);
  execSync(`git clone --depth 1 --branch ${tag} ${repo} ${dest}`, { stdio: "inherit" });
  execSync("pnpm install && pnpm build", { cwd: dest, stdio: "inherit" });
  if (!verifyNativeModules(dest)) {
    rebuildNativeModules(dest);
  }

  const tmp = join(SWARM_HOME, "app/current.new");
  symlinkForce(dest, tmp);
  renameSync(tmp, join(SWARM_HOME, "app/current"));

  bootstrapLaunchd();
  linkSwarmCli();
  if (!opts.quiet) console.log(`Updated to ${tag}`);
}

export function runStatus(): void {
  const paths = getSwarmPaths();
  console.log("SWARM_HOME:", paths.home);
  if (existsSync(join(paths.run, "daemon.port"))) {
    console.log("Port:", readFileSync(join(paths.run, "daemon.port"), "utf8").trim());
  }
  if (existsSync(join(paths.run, "daemon.pid"))) {
    console.log("PID:", readFileSync(join(paths.run, "daemon.pid"), "utf8").trim());
  }
}

export async function runUninstall(): Promise<void> {
  const uid = process.getuid?.() ?? "";
  for (const label of ["dev.swarm.daemon", "dev.swarm.updater"]) {
    spawnSync("launchctl", ["bootout", `gui/${uid}/${label}`], { stdio: "ignore" });
  }
  unlinkSwarmCli();
  p.log.success("LaunchAgents removed. Data in ~/.swarm preserved.");
}
