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

function isSwarmCodexHook(command: string | undefined): boolean {
  return Boolean(command?.includes("post-hook.mjs") && /(?:^|\s)codex(?:\s|$)/.test(command));
}

/** Remove legacy Swarm hooks without disturbing hooks installed by the user. */
export function removeSwarmCodexHooks(existing: CodexHooksFile): CodexHooksFile {
  const hooks: Record<string, CodexHookMatcher[]> = {};
  for (const [event, matchers] of Object.entries(existing.hooks ?? {})) {
    const kept = matchers.flatMap((matcher) => {
      if (!matcher.hooks) return [matcher];
      const commands = matcher.hooks.filter((hook) => !isSwarmCodexHook(hook.command));
      return commands.length > 0 ? [{ ...matcher, hooks: commands }] : [];
    });
    if (kept.length > 0) hooks[event] = kept;
  }
  return { ...existing, hooks };
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
