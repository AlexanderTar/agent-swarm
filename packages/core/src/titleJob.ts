import { enrichHookInput } from "./hookEnrichment.js";
import { hookInputFromTask } from "./hookInputFromTask.js";
import type { OllamaClient } from "./ollama.js";
import {
  findClaudeTranscriptPath,
  isFallbackSessionTitle,
  readFirstPromptFromTranscript,
  resolveSessionTaskTitle,
} from "./sessionTitles.js";
import { resolveCursorSession } from "./cursorSessions.js";
import { resolveAntigravitySession } from "./antigravitySessions.js";
import { resolveOpencodeSession } from "./opencodeSessions.js";
import type { TaskService } from "./tasks.js";
import type { TaskRecord } from "./types.js";

const TITLE_MAX_WORDS = 8;

function resolveTranscriptForTask(task: TaskRecord): string | undefined {
  const sessionId = task.originSessionId?.trim();
  if (!sessionId) return undefined;
  const cwd = task.originCwd ?? undefined;
  const agent = task.originAgent;

  if (agent === "claude" || agent === "unknown") {
    const path = findClaudeTranscriptPath(sessionId, cwd);
    if (path) return path;
  }
  if (agent === "cursor" || agent === "unknown") {
    const ref = resolveCursorSession(sessionId, cwd);
    if (ref?.transcriptPath) return ref.transcriptPath;
  }
  if (agent === "antigravity" || agent === "unknown") {
    const ref = resolveAntigravitySession(sessionId);
    if (ref?.transcriptPath) return ref.transcriptPath;
  }
  // Last resort: try Claude layout even for unknown agent ids
  return findClaudeTranscriptPath(sessionId, cwd);
}

function collectTitleSource(task: TaskRecord): string | undefined {
  const transcriptPath = resolveTranscriptForTask(task);
  if (transcriptPath) {
    const prompt = readFirstPromptFromTranscript(transcriptPath);
    if (prompt?.trim()) return prompt.trim();
  }

  if ((task.originAgent === "opencode" || task.originAgent === "unknown") && task.originSessionId?.trim()) {
    try {
      const title = resolveOpencodeSession(task.originSessionId.trim())?.title;
      if (title?.trim()) return title.trim();
    } catch {
      /* best-effort only */
    }
  }

  const input = enrichHookInput(hookInputFromTask(task));
  if (input.task?.trim()) return input.task.trim();
  if (input.prompt?.trim()) return input.prompt.trim();
  if (input.sessionTitle?.trim() && !isFallbackSessionTitle(input.sessionTitle)) {
    return input.sessionTitle.trim();
  }

  const resolved = resolveSessionTaskTitle(input);
  if (resolved.fromSession && !isFallbackSessionTitle(resolved.title)) return resolved.title;
  return undefined;
}

export function needsTitleRefresh(task: TaskRecord): boolean {
  return isFallbackSessionTitle(task.title ?? "");
}

/** Heuristic + Ollama short title from first transcript prompt / task description. */
export async function summarizeTaskTitle(
  ollama: OllamaClient,
  tasks: TaskService,
  task: TaskRecord,
  options?: { force?: boolean },
): Promise<{ task: TaskRecord; titleUpdated: boolean; source?: string }> {
  if (!options?.force && !needsTitleRefresh(task)) {
    return { task, titleUpdated: false };
  }

  const source = collectTitleSource(task);
  if (!source?.trim()) {
    // Still try hook enrichment (may pick up ai-title / cursor meta)
    const input = enrichHookInput(hookInputFromTask(task));
    const { title, fromSession } = resolveSessionTaskTitle(input);
    if (fromSession && title && !isFallbackSessionTitle(title)) {
      const updated = tasks.maybeRefreshTitle(task.id, title, true) ?? task;
      return { task: updated, titleUpdated: updated.title !== task.title, source: title };
    }
    return { task, titleUpdated: false };
  }

  let title: string;
  try {
    title = await ollama.summarizeTaskTitle(source, TITLE_MAX_WORDS);
  } catch {
    title = source.replace(/\s+/g, " ").trim().slice(0, 80);
  }

  title = title.replace(/^["']|["']$/g, "").trim();
  if (!title || isFallbackSessionTitle(title)) {
    return { task, titleUpdated: false, source };
  }

  const updated = tasks.maybeRefreshTitle(task.id, title, true) ?? task;
  return { task: updated, titleUpdated: updated.title !== task.title, source };
}

export function listTasksNeedingTitles(tasks: TaskService, limit = 200): TaskRecord[] {
  return tasks
    .list()
    .filter((t) => t.status !== "archived" && needsTitleRefresh(t))
    .slice(0, limit);
}
