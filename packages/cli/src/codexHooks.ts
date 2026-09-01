import { existsSync } from "node:fs";

export interface CodexHookCommand {
  type?: string;
  command?: string;
  timeout?: number;
  async?: boolean;
  statusMessage?: string;
}

export interface CodexHookMatcher {
  matcher?: string;
  hooks?: CodexHookCommand[];
}

export interface CodexHooksFile {
  hooks?: Record<string, CodexHookMatcher[]>;
}

const SESSION_END_MAX_TIMEOUT_SEC = 3;

export function expandPluginRoot(command: string, pluginPath: string): string {
  return command.replaceAll("${PLUGIN_ROOT}", pluginPath.replace(/\\/g, "/"));
}

function materializeCommand(
  hook: CodexHookCommand,
  pluginPath: string,
  event: string,
): CodexHookCommand {
  const next: CodexHookCommand = { ...hook };
  if (typeof next.command === "string") {
    next.command = expandPluginRoot(next.command, pluginPath);
  }
  if (
    event === "SessionEnd" &&
    typeof next.timeout === "number" &&
    next.timeout > SESSION_END_MAX_TIMEOUT_SEC
  ) {
    next.timeout = SESSION_END_MAX_TIMEOUT_SEC;
  }
  return next;
}

export function materializeCodexUserHooks(
  incoming: CodexHooksFile,
  pluginPath: string,
): CodexHooksFile {
  const hooks: Record<string, CodexHookMatcher[]> = {};
  for (const [event, matchers] of Object.entries(incoming.hooks ?? {})) {
    hooks[event] = matchers.map((matcher) => ({
      ...matcher,
      hooks: (matcher.hooks ?? []).map((hook) => materializeCommand(hook, pluginPath, event)),
    }));
  }
  return { hooks };
}

function firstSessionStartCommand(hooksJson: string): string | undefined {
  try {
    const parsed = JSON.parse(hooksJson) as CodexHooksFile;
    const matchers = parsed.hooks?.SessionStart ?? [];
    for (const matcher of matchers) {
      for (const hook of matcher.hooks ?? []) {
        if (typeof hook.command === "string" && hook.command.includes("post-hook.mjs")) {
          return hook.command;
        }
      }
    }
  } catch {
    return undefined;
  }
  return undefined;
}

function scriptPathFromCommand(command: string): string | undefined {
  const quoted = command.match(/node\s+"([^"]+post-hook\.mjs)"/);
  if (quoted?.[1]) return quoted[1];
  const unquoted = command.match(/node\s+(\S+post-hook\.mjs)/);
  return unquoted?.[1];
}

export function inspectCodexUserHooks(hooksJson: string): { ok: boolean; detail?: string } {
  if (hooksJson.includes("${PLUGIN_ROOT}")) {
    return {
      ok: false,
      detail: "Codex user hooks still use ${PLUGIN_ROOT} — run `swarm plugin sync`",
    };
  }
  let parsed: CodexHooksFile;
  try {
    parsed = JSON.parse(hooksJson) as CodexHooksFile;
  } catch {
    return { ok: false, detail: "Invalid ~/.codex/hooks.json" };
  }
  if (!parsed.hooks?.SessionStart) {
    return { ok: false, detail: "Swarm SessionStart hook not merged into ~/.codex/hooks.json" };
  }
  const command = firstSessionStartCommand(hooksJson);
  const script = command ? scriptPathFromCommand(command) : undefined;
  if (!script) {
    return { ok: false, detail: "Swarm SessionStart hook not merged into ~/.codex/hooks.json" };
  }
  if (!existsSync(script)) {
    return { ok: false, detail: `Codex hook script missing: ${script}` };
  }
  return { ok: true };
}

const SWARM_START = "# swarm:start";
const SWARM_END = "# swarm:end";

function extractNonSwarmMcpTables(inner: string): string {
  const parts = inner.split(/(?=^\[)/m);
  const kept: string[] = [];
  for (const part of parts) {
    const header = part.match(/^\[([^\]]+)\]/)?.[1] ?? "";
    if (!header) continue;
    if (header === "mcp_servers.swarm" || header === "mcp_servers.swarm.env") continue;
    kept.push(part.trimEnd());
  }
  return kept.join("\n").trim();
}

export function spliceSwarmTomlBlock(content: string, mcpBlock: string): string {
  const start = content.indexOf(SWARM_START);
  const end = content.indexOf(SWARM_END);
  if (start !== -1 && end !== -1 && end > start) {
    const inner = content.slice(start + SWARM_START.length, end);
    const preserved = extractNonSwarmMcpTables(inner);
    const before = content.slice(0, start);
    const after = content.slice(end + SWARM_END.length);
    const middle = preserved ? `${mcpBlock}\n\n${preserved}` : mcpBlock;
    return `${before}${middle}${after}`.replace(/\n{3,}/g, "\n\n");
  }

  if (content.includes("[mcp_servers.swarm]")) {
    return content.replace(/\[mcp_servers\.swarm\][\s\S]*?(?=\n\[|\n# |$)/, `${mcpBlock}\n`);
  }

  if (!content.trim()) return `${mcpBlock}\n`;
  return `${content.trimEnd()}\n\n${mcpBlock}\n`;
}
