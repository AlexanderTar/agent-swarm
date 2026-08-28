import { execSync, spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync, mkdirSync, symlinkSync, unlinkSync, cpSync, renameSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import * as p from "@clack/prompts";
import { DEFAULT_CONFIG, ensureSwarmDirs, getSwarmPaths, loadConfig, saveConfig, SwarmDatabase } from "@swarm/core";

const SWARM_HOME = process.env.SWARM_HOME ?? join(homedir(), ".swarm");
const ALL_AGENT_IDS = ["cursor", "claude", "codex", "antigravity"] as const;

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
  if (!existsSync(hooksPath)) {
    return { ok: false, detail: "Missing ~/.codex/hooks.json — run `swarm plugin sync`" };
  }
  try {
    const hooks = JSON.parse(readFileSync(hooksPath, "utf8")) as { hooks?: Record<string, unknown> };
    if (!hooks.hooks?.SessionStart) {
      return { ok: false, detail: "Swarm SessionStart hook not merged into ~/.codex/hooks.json" };
    }
  } catch {
    return { ok: false, detail: "Invalid ~/.codex/hooks.json" };
  }
  return {
    ok: true,
    detail: "Approve swarm hooks in Codex /hooks TUI or sessions won't appear on the board",
  };
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
  const hooks = readFileSync(join(pluginPath, "hooks.json"), "utf8");
  if (!hooks.includes("PostInvocation")) {
    return { ok: false, detail: "Antigravity hooks missing PostInvocation" };
  }
  return { ok: true };
}

export function detectAgents(): Array<{ id: string; name: string; detected: boolean }> {
  return [
    { id: "cursor", name: "Cursor", detected: existsSync(join(homedir(), ".cursor")) },
    { id: "claude", name: "Claude Code", detected: existsSync(join(homedir(), ".claude")) },
    { id: "codex", name: "Codex CLI", detected: existsSync(join(homedir(), ".codex")) },
    { id: "antigravity", name: "Antigravity", detected: existsSync(join(homedir(), ".gemini")) },
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

function symlinkForce(target: string, link: string): void {
  mkdirSync(join(link, ".."), { recursive: true });
  try {
    unlinkSync(link);
  } catch {
    // ignore
  }
  symlinkSync(target, link);
}

export function pluginSync(agents: string[]): void {
  const pluginPath = join(SWARM_HOME, "app/current/plugin");
  if (!existsSync(pluginPath)) {
    // dev: use repo plugin
    const devPlugin = join(process.cwd(), "plugin");
    if (existsSync(devPlugin)) symlinkForce(devPlugin, pluginPath);
  }

  if (agents.includes("cursor")) {
    const dest = join(homedir(), ".cursor/plugins/local/swarm");
    mkdirSync(join(dest, ".."), { recursive: true });
    symlinkForce(pluginPath, dest);
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
}

function mergeCodexConfig(pluginPath: string): void {
  const configPath = join(homedir(), ".codex/config.toml");
  const hooksPath = join(homedir(), ".codex/hooks.json");
  const mcpBlock = `
# swarm:start
[mcp_servers.swarm]
url = "http://127.0.0.1:7777/mcp"
# swarm:end
`;
  let content = existsSync(configPath) ? readFileSync(configPath, "utf8") : "";
  if (!content.includes("# swarm:start")) {
    content += mcpBlock;
    writeFileSync(configPath, content);
  }

  const pluginHooks = join(pluginPath, ".codex-plugin/hooks.json");
  if (existsSync(pluginHooks)) {
    const incoming = JSON.parse(readFileSync(pluginHooks, "utf8")) as { hooks: Record<string, unknown> };
    let existing: { hooks?: Record<string, unknown> } = {};
    if (existsSync(hooksPath)) {
      try {
        existing = JSON.parse(readFileSync(hooksPath, "utf8")) as { hooks?: Record<string, unknown> };
      } catch {
        existing = {};
      }
    }
    const merged = { hooks: { ...(existing.hooks ?? {}), ...(incoming.hooks ?? {}) } };
    writeFileSync(hooksPath, `${JSON.stringify(merged, null, 2)}\n`);
  }

  p.log.warn("Codex: approve plugin hooks in the /hooks TUI or sessions won't appear on the board.");
}

function mergeAntigravityConfig(pluginPath: string): void {
  const mcpStdio = join(SWARM_HOME, "app/current/packages/mcp-stdio/dist/index.js");
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

  const merged = {
    mcpServers: {
      ...(existing.mcpServers ?? {}),
      swarm: {
        command: "node",
        args: [mcpStdio],
        env: {
          SWARM_URL: "http://127.0.0.1:7777",
          SWARM_AGENT: "antigravity",
        },
      },
    },
  };
  writeFileSync(mcpPath, `${JSON.stringify(merged, null, 2)}\n`);

  const fragmentPath = join(pluginPath, "GEMINI.md");
  if (existsSync(fragmentPath)) {
    mergeSentinelBlock(join(homedir(), ".gemini/GEMINI.md"), "swarm", readFileSync(fragmentPath, "utf8"));
  }
}

function printInstallGuide(port: number, agents: string[], autoUpdate: boolean): void {
  const boardUrl = `http://127.0.0.1:${port}`;
  const cli = "node ~/.swarm/app/current/packages/cli/dist/index.js";
  const uid = process.getuid?.() ?? "$(id -u)";

  console.log(`
══════════════════════════════════════════════════════════════
  Agent Swarm — installed
══════════════════════════════════════════════════════════════

  Dashboard (Kanban board)
    ${boardUrl}

  Quick commands
    open ${boardUrl}                 Open the board in your browser
    ${cli} open                      Same, via CLI
    ${cli} status                    Daemon PID, port, paths
    ${cli} doctor                    Health check (add --hooks to test hooks)
    ${cli} plugin sync               Re-link plugins after manual edits

  Wired agents
    ${agents.join(", ")}

  MCP tools (call from any wired agent)
    swarm_board          View the Kanban board
    swarm_handoff        Write a handoff note and move task to handoff
    swarm_pickup         Claim a handoff and get a pickup prompt
    swarm_kb_search      Semantic search over ~/.swarm/kb
    swarm_task_stage     Move, claim, release, heartbeat, archive tasks

  Handoff workflow
    1. End a session → tile appears on the board automatically
    2. Call swarm_handoff (or /handoff) before switching agents
    3. In the next agent: swarm_pickup or /pickup to claim and continue

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
    Models: nomic-embed-text (embeddings), qwen3:4b (chat/summarize)

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
  printInstallGuide(port, selected, autoUpdate);
}

export async function runDoctor(hooks = false): Promise<number> {
  let errors = 0;
  const paths = getSwarmPaths();

  const checks: Array<[string, boolean, string?]> = [];

  checks.push(["~/.swarm exists", existsSync(paths.home)]);
  checks.push(["current symlink", existsSync(paths.current)]);
  checks.push(["daemon token", existsSync(join(paths.run, "daemon.token"))]);

  try {
    const r = await fetch(`http://127.0.0.1:7777/api/health`, { signal: AbortSignal.timeout(2000) });
    checks.push(["daemon health", r.ok]);
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
  const ag = checkAntigravityPlugin(pluginPath);
  checks.push(["antigravity plugin", ag.ok, ag.detail]);

  for (const [name, ok, detail] of checks) {
    console.log(`${ok ? "✓" : "✗"} ${name}${detail ? `: ${detail}` : ""}`);
    if (!ok) errors++;
  }

  if (hooks) {
    const payload = { session_id: "doctor-test", cwd: process.cwd(), hook_event_name: "SessionStart" };
    try {
      const r = await fetch("http://127.0.0.1:7777/hooks/claude/SessionStart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      console.log(`${r.ok ? "✓" : "✗"} hook replay SessionStart`);
      if (!r.ok) errors++;
    } catch {
      console.log("✗ hook replay SessionStart");
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

  const tmp = join(SWARM_HOME, "app/current.new");
  symlinkForce(dest, tmp);
  renameSync(tmp, join(SWARM_HOME, "app/current"));

  bootstrapLaunchd();
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
  p.log.success("LaunchAgents removed. Data in ~/.swarm preserved.");
}
