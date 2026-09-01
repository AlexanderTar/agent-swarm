import { existsSync } from "node:fs";

export interface AntigravityHookCommand {
  type?: string;
  command?: string;
  timeout?: number;
  matcher?: string;
  hooks?: AntigravityHookCommand[];
}

export type AntigravityNamedHook = {
  enabled?: boolean;
  PreToolUse?: AntigravityHookCommand[];
  PostToolUse?: AntigravityHookCommand[];
  PreInvocation?: AntigravityHookCommand[];
  PostInvocation?: AntigravityHookCommand[];
  Stop?: AntigravityHookCommand[];
};

export type AntigravityHooksFile = Record<string, AntigravityNamedHook>;

const TOOL_MATCHER =
  "run_command|view_file|write_to_file|replace_file_content|multi_replace_file_content";

function hookCommand(pluginPath: string, event: string): string {
  const script = `${pluginPath.replace(/\\/g, "/")}/hooks/post-hook.mjs`;
  return `node "${script}" antigravity ${event}`;
}

export function buildAntigravityHooks(pluginPath: string): AntigravityHooksFile {
  const cmd = (event: string) => hookCommand(pluginPath, event);
  return {
    "swarm-sync": {
      PreToolUse: [
        {
          matcher: TOOL_MATCHER,
          hooks: [{ type: "command", command: cmd("PreToolUse"), timeout: 5 }],
        },
      ],
      PostToolUse: [
        {
          matcher: "*",
          hooks: [{ type: "command", command: cmd("PostToolUse"), timeout: 5 }],
        },
      ],
      PreInvocation: [{ type: "command", command: cmd("PreInvocation"), timeout: 5 }],
      PostInvocation: [{ type: "command", command: cmd("PostInvocation"), timeout: 5 }],
      Stop: [{ type: "command", command: cmd("Stop"), timeout: 5 }],
    },
  };
}

function firstCommand(hooksJson: string): string | undefined {
  try {
    const parsed = JSON.parse(hooksJson) as AntigravityHooksFile;
    const named = parsed["swarm-sync"];
    const flat = named?.PreInvocation?.[0];
    if (typeof flat?.command === "string") return flat.command;
    const nested = named?.PreInvocation?.[0]?.hooks?.[0];
    if (typeof nested?.command === "string") return nested.command;
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

export function inspectAntigravityHooks(hooksJson: string): { ok: boolean; detail?: string } {
  if (hooksJson.includes("${PLUGIN_ROOT}")) {
    return {
      ok: false,
      detail: "Antigravity hooks still use ${PLUGIN_ROOT} — run `swarm plugin sync`",
    };
  }
  let parsed: AntigravityHooksFile;
  try {
    parsed = JSON.parse(hooksJson) as AntigravityHooksFile;
  } catch {
    return { ok: false, detail: "Invalid Antigravity hooks.json" };
  }
  const named = parsed["swarm-sync"];
  if (!named?.PreInvocation?.length) {
    return { ok: false, detail: "Antigravity hooks missing PreInvocation" };
  }
  const pre = named.PreInvocation[0];
  if (!pre?.command) {
    return {
      ok: false,
      detail:
        "Antigravity PreInvocation must be a flat { command } handler — nested hooks arrays fail to parse",
    };
  }
  const command = firstCommand(hooksJson);
  const script = command ? scriptPathFromCommand(command) : undefined;
  if (!script) {
    return { ok: false, detail: "Antigravity PreInvocation hook not configured" };
  }
  if (!existsSync(script)) {
    return { ok: false, detail: `Antigravity hook script missing: ${script}` };
  }
  return { ok: true };
}
