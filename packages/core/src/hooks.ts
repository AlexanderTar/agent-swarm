export type HookPlatform = "claude" | "cursor" | "codex" | "antigravity" | "opencode";

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
  /** OS pid of the agent process when provided (pid / process_pid / agent_pid). */
  pid?: number;
  sessionTitle?: string;
  sessionSource?: string;
  task?: string;
  transcriptPath?: string;
  lastAssistantMessage?: string;
  /** Cursor afterAgentResponse / afterAgentThought `text` field. */
  agentText?: string;
  /** Cursor afterAgentThought `duration_ms`. */
  thoughtDurationMs?: number;
  /** Claude MessageDisplay streaming delta. */
  messageDelta?: string;
  /** Claude MessageDisplay — last chunk in a message. */
  messageFinal?: boolean;
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

function readPid(raw: Record<string, unknown>): number | undefined {
  for (const k of ["pid", "process_pid", "processPid", "agent_pid", "agentPid"]) {
    const v = raw[k];
    if (typeof v === "number" && Number.isFinite(v)) return Math.trunc(v);
    if (typeof v === "string" && v.trim() !== "" && Number.isFinite(Number(v))) return Math.trunc(Number(v));
  }
  return undefined;
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
    readString(raw.sessionID) ??
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
    pid: readPid(raw),
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
    agentText: readString(raw.text),
    thoughtDurationMs: typeof raw.duration_ms === "number" ? raw.duration_ms : undefined,
    messageDelta: readString(raw.delta),
    messageFinal: typeof raw.final === "boolean" ? raw.final : undefined,
    subagentStatus: readString(raw.status),
    subagentSummary: readString(raw.summary),
    taskSubject: readString(raw.task_subject) ?? readString(raw.taskSubject),
    taskDescription: readString(raw.task_description) ?? readString(raw.taskDescription),
    parentSessionId:
      readString(raw.parent_conversation_id) ??
      readString(raw.parentSessionId) ??
      readString(raw.parentConversationId) ??
      readString(raw.parent_session_id),
    raw,
  };
}

export function subagentIdFromTranscriptPath(path: string | undefined): string | undefined {
  if (!path) return undefined;
  const nested = path.match(/subagents[/\\]([^/\\]+)\.jsonl$/i);
  if (nested?.[1]) return nested[1];
  return undefined;
}

/** Check if a hook payload originates from a nested subagent. */
export function isSubagentHook(input: NormalizedHookInput, hookEvent?: string): boolean {
  const event = hookEvent ?? input.hookEvent;
  if (
    event === "SubagentStart" ||
    event === "subagentStart" ||
    event === "SubagentStop" ||
    event === "subagentStop"
  ) {
    return true;
  }
  if (input.parentSessionId?.trim()) {
    return true;
  }
  if (input.transcriptPath && /subagents[/\\][^/\\]+\.jsonl$/i.test(input.transcriptPath)) {
    return true;
  }
  if (input.agentId?.trim()) {
    // When sessionId is present and differs from agentId, agentId is a subagent identifier
    if (input.sessionId?.trim() && input.sessionId.trim() !== input.agentId.trim()) {
      return true;
    }
  }
  const raw = input.raw as Record<string, unknown> | undefined;
  if (raw) {
    if (raw.subagent_id || raw.subagent_type || raw.subagentId || raw.subagentType) {
      return true;
    }
    if (raw.parentConversationId || raw.parent_conversation_id || raw.parentSessionId || raw.parent_session_id) {
      return true;
    }
  }
  return false;
}

/** Check if a title or prompt originates from a synthetic probe / test prompt rather than a genuine user task. */
export function isProbePrompt(promptOrTitle: string | undefined): boolean {
  if (!promptOrTitle) return false;
  const t = promptOrTitle.trim().toLowerCase();
  return t.startsWith("reply with ") || t.startsWith("reply with exactly");
}

/** Resolve the root session ID that owns the task for this hook. */
export function resolveHookRootSessionId(
  input: Pick<NormalizedHookInput, "sessionId" | "parentSessionId" | "agentId">,
): string {
  if (input.parentSessionId?.trim()) return input.parentSessionId.trim();
  if (input.sessionId?.trim()) return input.sessionId.trim();
  return input.agentId?.trim() || "";
}

/** Subagent identifier when present (for subtask tracking and event logging). */
export function resolveSubagentBoardId(input: NormalizedHookInput): string | undefined {
  if (input.agentId?.trim()) return input.agentId.trim();
  const fromPath = subagentIdFromTranscriptPath(input.transcriptPath);
  if (fromPath) return fromPath;
  const raw = input.raw as Record<string, unknown> | undefined;
  const rawSubId = readString(raw?.subagent_id) ?? readString(raw?.subagentId);
  if (rawSubId?.trim()) return rawSubId.trim();
  return undefined;
}

/** Resolve which session id owns the board tile for this hook (always the root session). */
export function resolveHookBoardSessionId(input: NormalizedHookInput, _hookEvent?: string): string {
  return resolveHookRootSessionId(input);
}

/** One board tile per root agent session — subagents do not receive their own tiles. */
export function resolveBoardSessionId(
  input: Pick<NormalizedHookInput, "sessionId" | "agentId" | "parentSessionId">,
): string {
  return resolveHookRootSessionId(input);
}

function repoLabel(cwd: string): string {
  const parts = cwd.replace(/\/+$/, "").split("/").filter(Boolean);
  return parts.at(-1) ?? cwd;
}

export function sessionTaskTitle(input: Pick<NormalizedHookInput, "sessionTitle" | "cwd" | "agentType" | "sessionSource" | "platform" | "agentId">): string {
  if (input.sessionTitle?.trim()) return input.sessionTitle.trim();
  const repo = repoLabel(input.cwd);
  if (input.agentId && input.agentType) return `${input.agentType} · ${repo}`;
  if (input.agentId) return `Subagent · ${repo}`;
  const source = input.sessionSource ? ` (${input.sessionSource})` : "";
  return `${input.platform} · ${repo}${source}`;
}

export function buildSessionContext(input: NormalizedHookInput, parentSessionId?: string): string {
  const lines = [
    "## Session",
    "",
    `- **Agent:** ${input.platform}${input.agentType ? ` (${input.agentType})` : ""}`,
    `- **Working directory:** \`${input.cwd}\``,
  ];
  if (input.model) lines.push(`- **Model:** ${input.model}`);
  if (input.sessionSource) lines.push(`- **Source:** ${input.sessionSource}`);
  if (input.sessionId) lines.push(`- **Session ID:** \`${input.sessionId}\``);
  if (input.agentId) lines.push(`- **Agent ID:** \`${input.agentId}\``);
  if (parentSessionId && parentSessionId !== input.agentId) {
    lines.push(`- **Parent session:** \`${parentSessionId}\``);
  }
  if (input.transcriptPath) lines.push(`- **Transcript:** \`${input.transcriptPath}\``);
  if (input.task) lines.push("", "## Task", "", input.task);
  lines.push("", "_Prompts and tool activity appear in the Timeline tab._");
  return lines.join("\n");
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

export function agentKindFromPlatform(platform: HookPlatform): "claude" | "cursor" | "codex" | "antigravity" | "opencode" {
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
