import { resolveCursorSession } from "./cursorSessions.js";
import { resolveAntigravitySession } from "./antigravitySessions.js";
import { resolveBoardSessionId, resolveSubagentBoardId, type NormalizedHookInput } from "./hooks.js";
import { findClaudeTranscriptPath, resolveSessionTaskTitle } from "./sessionTitles.js";
import type { TaskService } from "./tasks.js";
import type { TaskRecord } from "./types.js";
import { resolveTranscriptPath } from "./transcripts.js";

function mergeEventPayloads(
  input: NormalizedHookInput,
  events?: Array<{ eventType: string; payload: unknown }>,
): void {
  if (!events) return;
  for (const event of events) {
    const payload = event.payload as Record<string, unknown>;
    if (typeof payload.task === "string" && payload.task.trim() && !input.task) {
      input.task = payload.task.trim();
    }
    if (typeof payload.description === "string" && payload.description.trim() && !input.task) {
      input.task = payload.description.trim();
    }
    if (typeof payload.prompt === "string" && payload.prompt.trim() && !input.prompt) {
      input.prompt = payload.prompt.trim();
    }
  }
}

/**
 * Enrich normalized hook input from on-disk session stores (Cursor chats,
 * agent-transcripts, Claude jsonl). Safe to call on every hook — prefers
 * values already present on the payload (transcript_path, session_title).
 */
export function enrichHookInput(
  input: NormalizedHookInput,
  options?: { events?: Array<{ eventType: string; payload: unknown }> },
): NormalizedHookInput {
  mergeEventPayloads(input, options?.events);

  const hookTranscript = input.transcriptPath;
  const boardSessionId = resolveBoardSessionId(input) || input.sessionId;

  if (input.platform === "cursor" && boardSessionId) {
    const ref = resolveCursorSession(boardSessionId, input.cwd);
    if (ref) {
      input.sessionId = ref.conversationId;
      if (ref.agentId) input.agentId = ref.agentId;
      if (ref.cwd && (!input.cwd || input.cwd === process.cwd())) input.cwd = ref.cwd;
      if (ref.title && !input.sessionTitle?.trim()) input.sessionTitle = ref.title;
      if (ref.transcriptPath && !hookTranscript) input.transcriptPath = ref.transcriptPath;
    }
  }

  if (input.platform === "claude" && boardSessionId && !input.transcriptPath) {
    input.transcriptPath = findClaudeTranscriptPath(boardSessionId, input.cwd);
  }

  if (input.platform === "antigravity" && boardSessionId) {
    const ref = resolveAntigravitySession(boardSessionId);
    if (ref) {
      input.sessionId = ref.conversationId;
      if (ref.cwd && (!input.cwd || input.cwd === process.cwd())) input.cwd = ref.cwd;
      if (ref.title && !input.sessionTitle?.trim()) input.sessionTitle = ref.title;
      if (ref.transcriptPath && !hookTranscript) input.transcriptPath = ref.transcriptPath;
    }
  }

  if (!input.transcriptPath) {
    const transcriptBoardId = input.agentId?.trim() || boardSessionId;
    const path = resolveTranscriptPath(input, transcriptBoardId);
    if (path) input.transcriptPath = path;
  }

  if (hookTranscript) input.transcriptPath = hookTranscript;

  if (!input.agentId?.trim()) {
    const subagentId = resolveSubagentBoardId(input);
    if (subagentId) input.agentId = subagentId;
  }

  return input;
}

/** Apply resolved session title and cwd to an existing board task. */
export function syncTaskSessionMetadata(
  tasks: TaskService,
  task: TaskRecord,
  input: NormalizedHookInput,
): { task: TaskRecord; titleUpdated: boolean; cwdUpdated: boolean } {
  const { title, fromSession } = resolveSessionTaskTitle(input);
  const beforeTitle = task.title;
  const beforeCwd = task.originCwd;

  let updated = tasks.maybeRefreshTitle(task.id, title, fromSession) ?? task;
  if (input.cwd?.trim() && input.cwd !== updated.originCwd) {
    updated = tasks.maybeRefreshOriginCwd(updated.id, input.cwd) ?? updated;
  }
  // Best-effort enrichment: fill model/pid/transcript when the task lacks them.
  // Never overwrites known values; failures are non-fatal (hooks must exit 0).
  try {
    const fresh = tasks.getById(updated.id);
    if (fresh) {
      tasks.refreshOriginMetadata(fresh.id, { model: input.model, pid: input.pid });
      if (input.transcriptPath?.trim()) {
        tasks.mergeTranscriptArtifact(fresh.id, input.transcriptPath.trim());
      }
      updated = tasks.getById(fresh.id) ?? fresh;
    }
  } catch {
    /* hooks must never fail on enrichment */
  }

  return {
    task: updated,
    titleUpdated: updated.title !== beforeTitle,
    cwdUpdated: updated.originCwd !== beforeCwd,
  };
}
