import { execSync, spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync, mkdirSync, symlinkSync, unlinkSync, cpSync, renameSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import * as p from "@clack/prompts";
import { DEFAULT_CONFIG, ensureSwarmDirs, getSwarmPaths, loadConfig, saveConfig, SwarmDatabase } from "@swarm/core";

const SWARM_HOME = process.env.SWARM_HOME ?? join(homedir(), ".swarm");

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

export async function runInstall(fromBootstrap = false): Promise<void> {
  p.intro("Agent Swarm setup");

  const nodeOk = Number(process.version.slice(1).split(".")[0]) >= 20;
  if (!nodeOk) {
    p.cancel("Node 20+ required");
    process.exit(1);
  }

  let ollamaOk = false;
  try {
    const r = await fetch("http://127.0.0.1:11434/api/tags", { signal: AbortSignal.timeout(3000) });
    ollamaOk = r.ok;
  } catch {
    ollamaOk = false;
  }
  if (!ollamaOk) {
    p.cancel("Ollama is required. Run: ollama serve && ollama pull nomic-embed-text && ollama pull qwen3:4b");
    process.exit(1);
  }

  const detected = detectAgents().filter((a) => a.detected);
  const selected = await p.multiselect({
    message: "Wire which agents?",
    options: detected.map((a) => ({ value: a.id, label: a.name })),
    initialValues: detected.map((a) => a.id),
  });
  if (p.isCancel(selected)) process.exit(0);

  const port = await p.text({ message: "Daemon port", initialValue: "7777" });
  if (p.isCancel(port)) process.exit(0);

  const autoUpdate = await p.confirm({ message: "Enable hourly auto-update?", initialValue: true });
  if (p.isCancel(autoUpdate)) process.exit(0);

  const paths = ensureSwarmDirs();
  const config = loadConfig();
  config.port = Number(port);
  config.autoUpdate = Boolean(autoUpdate);
  saveConfig(config);

  if (!fromBootstrap) {
    const repoRoot = join(process.cwd());
    mkdirSync(join(SWARM_HOME, "app/releases/dev"), { recursive: true });
    cpSync(repoRoot, join(SWARM_HOME, "app/releases/dev"), { recursive: true });
    const tmp = join(SWARM_HOME, "app/current.new");
    symlinkForce(join(SWARM_HOME, "app/releases/dev"), tmp);
    renameSync(tmp, join(SWARM_HOME, "app/current"));
  }

  writeStartScript();
  writeDaemonPlist();
  writeUpdaterPlist(Boolean(autoUpdate));
  pluginSync(selected as string[]);
  bootstrapLaunchd();

  const codexCheck = checkCodexHooks();
  if (selected.includes("codex") && !codexCheck.ok) {
    p.log.warn(codexCheck.detail ?? "Codex hooks not configured");
  } else if (selected.includes("codex")) {
    p.log.info(codexCheck.detail ?? "Codex hooks merged");
  }

  p.outro(`Agent Swarm ready at http://127.0.0.1:${port}`);
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
