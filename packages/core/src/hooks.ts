export type HookPlatform = "claude" | "cursor" | "codex" | "antigravity";

export interface NormalizedHookInput {
  platform: HookPlatform;
  sessionId: string;
  cwd: string;
  hookEvent: string;
  toolName?: string;
  toolInput?: Record<string, unknown>;
  toolOutput?: string;
  prompt?: string;
  model?: string;
  agentType?: string;
  lastAssistantMessage?: string;
  raw: Record<string, unknown>;
}

export interface HookOutput {
  additionalContext?: string;
  permission?: "allow" | "deny" | "ask";
  userMessage?: string;
  agentMessage?: string;
  env?: Record<string, string>;
  followupMessage?: string;
  continue?: boolean;
  stopReason?: string;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function readString(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

function detectPlatform(raw: Record<string, unknown>): HookPlatform {
  if ("conversationId" in raw || ("toolCall" in raw && "workspacePaths" in raw)) return "antigravity";
  if ("conversation_id" in raw || "workspace_roots" in raw || "cursor_version" in raw) return "cursor";
  if ("turn_id" in raw && "hook_event_name" in raw) return "codex";
  return "claude";
}

function readWorkspaceRoot(raw: Record<string, unknown>): string | undefined {
  if (Array.isArray(raw.workspace_roots) && typeof raw.workspace_roots[0] === "string") {
    return raw.workspace_roots[0];
  }
  if (Array.isArray(raw.workspacePaths) && typeof raw.workspacePaths[0] === "string") {
    return raw.workspacePaths[0];
  }
  return undefined;
}

function normalizeToolOutput(raw: Record<string, unknown>): string | undefined {
  const v = raw.tool_output ?? raw.tool_response ?? raw.toolOutput;
  if (typeof v === "undefined") return undefined;
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

export function normalizeHookInput(raw: unknown, platformHint?: HookPlatform): NormalizedHookInput {
  if (!isRecord(raw)) {
    return {
      platform: platformHint ?? "claude",
      sessionId: "",
      cwd: process.cwd(),
      hookEvent: "",
      raw: {},
    };
  }

  const platform = platformHint ?? detectPlatform(raw);
  let hookEvent = readString(raw.hook_event_name) ?? readString(raw.hookEventName) ?? "";
  let toolName = readString(raw.tool_name) ?? readString(raw.toolName);
  let toolInput = isRecord(raw.tool_input) ? raw.tool_input : isRecord(raw.toolInput) ? raw.toolInput : undefined;

  if (platform === "antigravity") {
    hookEvent = hookEvent || readString(raw.hookEvent) || "";
    const tc = isRecord(raw.toolCall) ? raw.toolCall : undefined;
    if (tc) {
      toolName = readString(tc.name);
      toolInput = isRecord(tc.args) ? (tc.args as Record<string, unknown>) : undefined;
    }
  }

  const sessionId =
    readString(raw.session_id) ??
    readString(raw.conversation_id) ??
    readString(raw.conversationId) ??
    "";

  const cwd =
    readString(raw.cwd) ??
    readWorkspaceRoot(raw) ??
    process.env.CURSOR_PROJECT_DIR ??
    process.env.CLAUDE_PROJECT_DIR ??
    process.cwd();

  return {
    platform,
    sessionId,
    cwd,
    hookEvent,
    toolName,
    toolInput,
    toolOutput: normalizeToolOutput(raw),
    prompt: readString(raw.prompt),
    model: readString(raw.model) ?? readString(raw.modelName),
    agentType: readString(raw.agent_type) ?? readString(raw.agentType) ?? readString(raw.subagent_type),
    lastAssistantMessage: readString(raw.last_assistant_message) ?? readString(raw.lastAssistantMessage),
    raw,
  };
}

export function formatHookOutput(platform: HookPlatform, output: HookOutput, hookEvent?: string): Record<string, unknown> {
  if (platform === "cursor") {
    const result: Record<string, unknown> = {};
    if (output.additionalContext) result.additional_context = output.additionalContext;
    if (output.permission) result.permission = output.permission;
    if (output.userMessage) result.user_message = output.userMessage;
    if (output.agentMessage) result.agent_message = output.agentMessage;
    if (output.env) result.env = output.env;
    if (output.followupMessage) result.followup_message = output.followupMessage;
    return result;
  }

  if (platform === "antigravity") {
    const result: Record<string, unknown> = {};
    if (output.permission === "deny") {
      result.decision = "deny";
      if (output.userMessage) result.reason = output.userMessage;
    } else if (output.permission === "ask") {
      result.decision = "ask";
    } else if (output.followupMessage) {
      result.decision = "continue";
      result.reason = output.followupMessage;
    } else {
      result.decision = "allow";
    }
    return result;
  }

  // Claude Code + Codex
  const result: Record<string, unknown> = {};
  if (typeof output.continue === "boolean") result.continue = output.continue;
  if (output.stopReason) result.stopReason = output.stopReason;
  if (output.userMessage) result.systemMessage = output.userMessage;

  if (output.additionalContext || output.permission) {
    result.hookSpecificOutput = {
      hookEventName: hookEvent,
      ...(output.additionalContext ? { additionalContext: output.additionalContext } : {}),
      ...(output.permission
        ? { permissionDecision: output.permission, permissionDecisionReason: output.userMessage }
        : {}),
    };
  }

  if (output.followupMessage && (hookEvent === "Stop" || hookEvent === "SubagentStop" || hookEvent === "stop")) {
    result.decision = "block";
    result.reason = output.followupMessage;
  }

  return result;
}

export function agentKindFromPlatform(platform: HookPlatform): "claude" | "cursor" | "codex" | "antigravity" {
  return platform;
}

export function mapAntigravityTool(toolName?: string): string | undefined {
  if (!toolName) return undefined;
  const map: Record<string, string> = {
    run_command: "Bash",
    view_file: "Read",
    write_to_file: "Write",
    replace_file_content: "Edit",
    multi_replace_file_content: "Edit",
  };
  return map[toolName] ?? toolName;
}

export const SHORT_REFERENCE_MAX = 500;

export function shortReference(message: string, url?: string): string {
  const ref = message.slice(0, SHORT_REFERENCE_MAX);
  return url ? `${ref}\n\nDetails: ${url}` : ref;
}
