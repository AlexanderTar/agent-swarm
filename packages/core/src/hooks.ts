import type { AgentKind } from "./types.js";

export type HookPlatform = "claude" | "cursor" | "codex" | "antigravity" | "opencode";

/** Coarse event class, so route handling is one switch instead of thirty cases. */
export type HookEventKind =
  | "session_start"
  | "subagent_start"
  | "session_end"
  | "turn_end"
  | "tool"
  | "prompt"
  | "notification"
  | "compact"
  | "other";

const HOOK_EVENT_KINDS: Record<string, HookEventKind> = {
  sessionstart: "session_start",
  "session.created": "session_start",
  subagentstart: "subagent_start",
  sessionend: "session_end",
  "session.deleted": "session_end",
  stop: "turn_end",
  stopfailure: "turn_end",
  subagentstop: "turn_end",
  "session.idle": "turn_end",
  postinvocation: "turn_end",
  afteragentresponse: "turn_end",
  taskcompleted: "turn_end",
  pretooluse: "tool",
  posttooluse: "tool",
  afterfileedit: "tool",
  permissionrequest: "tool",
  "tool.execute.before": "tool",
  "tool.execute.after": "tool",
  userpromptsubmit: "prompt",
  beforesubmitprompt: "prompt",
  notification: "notification",
  precompact: "compact",
  postcompact: "compact",
};

/** Platforms spell the same lifecycle moment five ways; collapse them here. */
export function classifyHookEvent(event: string): HookEventKind {
  return HOOK_EVENT_KINDS[event.trim().toLowerCase()] ?? "other";
}

export interface NormalizedHookInput {
  platform: HookPlatform;
  sessionId: string;
  /** Subagent / nested agent id when present (unique per spawned agent). */
  agentId?: string;
  cwd: string;
  hookEvent: string;
  toolName?: string;
  toolInput?: Record<string, unknown>;
  toolOutput?: string;
  prompt?: string;
  model?: string;
  agentType?: string;
  sessionTitle?: string;
  sessionSource?: string;
  task?: string;
  transcriptPath?: string;
  lastAssistantMessage?: string;
  /** Subagent / task lifecycle (Claude Task*, Cursor subagentStop). */
  subagentStatus?: string;
  subagentSummary?: string;
  taskSubject?: string;
  taskDescription?: string;
  /** Parent session when this hook is for a subagent. */
  parentSessionId?: string;
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
  if ("opencode" in raw) return "opencode";
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
    readString(raw.parent_conversation_id) ??
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
    agentId: readString(raw.agent_id) ?? readString(raw.agentId) ?? readString(raw.subagent_id),
    cwd,
    hookEvent,
    toolName,
    toolInput,
    toolOutput: normalizeToolOutput(raw),
    prompt: readString(raw.prompt),
    model: readString(raw.model) ?? readString(raw.modelName),
    agentType: readString(raw.agent_type) ?? readString(raw.agentType) ?? readString(raw.subagent_type),
    sessionTitle:
      readString(raw.session_title) ??
      readString(raw.sessionTitle) ??
      readString(raw.conversation_title) ??
      readString(raw.conversationTitle) ??
      readString(raw.chat_title) ??
      readString(raw.chatTitle),
    sessionSource: readString(raw.source),
    task: readString(raw.task) ?? readString(raw.description),
    transcriptPath:
      readString(raw.transcript_path) ??
      readString(raw.transcriptPath) ??
      readString(raw.agent_transcript_path),
    lastAssistantMessage: readString(raw.last_assistant_message) ?? readString(raw.lastAssistantMessage),
    subagentStatus: readString(raw.status),
    subagentSummary: readString(raw.summary),
    taskSubject: readString(raw.task_subject) ?? readString(raw.taskSubject),
    taskDescription: readString(raw.task_description) ?? readString(raw.taskDescription),
    parentSessionId: readString(raw.parent_conversation_id) ?? readString(raw.parentSessionId),
    raw,
  };
}

/** The board's key for this hook: a subagent when there is one, otherwise the host session. */
export function hookSessionKey(input: Pick<NormalizedHookInput, "sessionId" | "agentId">): string {
  return input.agentId?.trim() || input.sessionId;
}

/** The text handed back to the agent on session start. */
export function sessionBriefing(params: {
  sessionId: string;
  boardUrl: string;
  task?: { key: string; title: string; status: string } | null;
}): string {
  const header = `## Agent Swarm\nBoard: ${params.boardUrl} · your swarm session: \`${params.sessionId}\``;
  if (params.task) {
    return `${header}
You are working on **${params.task.key} — ${params.task.title}** (${params.task.status}).

Call \`swarm_task_update\` with a fresh \`summary\` when the goal, state, or next step
changes, and \`swarm_task_stage\` to move it between columns. Add \`tags\` as they become
obvious. Use \`swarm_handoff\` before handing the work to another agent.`;
  }
  return `${header}

This session is not on the board. When you start work worth coordinating with other
agents — a feature, an investigation, a handoff — call \`swarm_task_create\` with:
- \`title\`: a specific, human-readable name you write yourself (max ~60 chars)
- \`summary\`: 2-5 sentences in your own words — goal, current state, next step
- \`tags\`: lowercase labels for filtering, e.g. ["repo-name", "backend", "bugfix"]
- \`sessionId\`: \`${params.sessionId}\`

To help on work already on the board, call \`swarm_task_join\` with its key instead.
Do not create a board item for trivial, throwaway, or read-only turns.
Keep \`summary\` current with \`swarm_task_update\` whenever the picture changes.`;
}

export function formatHookOutput(platform: HookPlatform, output: HookOutput, hookEvent?: string): Record<string, unknown> {
  if (platform === "opencode") {
    // Our own plugin consumes this shape verbatim.
    return output.additionalContext ? { additionalContext: output.additionalContext } : {};
  }

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
    // Harmless to hosts that ignore it; the only context channel Antigravity leaves us.
    if (output.additionalContext) result.additionalContext = output.additionalContext;
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

export function agentKindFromPlatform(platform: HookPlatform): AgentKind {
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
