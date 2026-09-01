import { enrichHookInput, syncTaskSessionMetadata } from "./hookEnrichment.js";
import { listAntigravityBrainSessions } from "./antigravitySessions.js";
import type { OllamaClient } from "./ollama.js";
import { hookInputFromTask } from "./hookInputFromTask.js";
import type { MemoryJobs } from "./memory.js";
import {
  fallbackSummary,
  formatSessionSummaryMarkdown,
  inferStatusFromStop,
  summarizeSessionTurn,
  type SessionSummaryResult,
} from "./sessionSummary.js";
import { needsTitleRefresh, summarizeTaskTitle } from "./titleJob.js";
import type { TaskService } from "./tasks.js";
import type { TaskRecord, TaskStatus } from "./types.js";

export { hookInputFromTask } from "./hookInputFromTask.js";
export { enrichHookInput, syncTaskSessionMetadata } from "./hookEnrichment.js";

export function importAntigravitySessions(tasks: TaskService): TaskRecord[] {
  const imported: TaskRecord[] = [];
  for (const session of listAntigravityBrainSessions()) {
    if (tasks.getBySession(session.conversationId)) continue;
    imported.push(
      tasks.upsertSessionTask({
        sessionId: session.conversationId,
        agent: "antigravity",
        cwd: session.cwd,
        title: session.title,
        titleFromSession: Boolean(session.title),
      }),
    );
  }
  return imported;
}

export async function refreshTaskSessionTitle(
  tasks: TaskService,
  task: TaskRecord,
  ollama?: OllamaClient,
): Promise<{ task: TaskRecord; titleUpdated: boolean }> {
  const events = tasks.getEvents(task.id, 50);
  const input = enrichHookInput(hookInputFromTask(task), { events });
  const { task: refreshed, titleUpdated } = syncTaskSessionMetadata(tasks, task, input);
  if (!ollama) return { task: refreshed, titleUpdated };
  if (!titleUpdated && !needsTitleRefresh(refreshed)) return { task: refreshed, titleUpdated: false };
  const summarized = await summarizeTaskTitle(ollama, tasks, refreshed, {
    force: needsTitleRefresh(refreshed),
  });
  return { task: summarized.task, titleUpdated: titleUpdated || summarized.titleUpdated };
}

export async function summarizeTaskRecord(
  ollama: OllamaClient,
  tasks: TaskService,
  task: TaskRecord,
  options?: {
    hookEvent?: string;
    suggestedStatus?: TaskStatus;
    skipIfPresent?: boolean;
    memory?: MemoryJobs;
    awaitMemory?: boolean;
  },
): Promise<{ ok: boolean; task: TaskRecord; usedFallback: boolean; titleUpdated: boolean; memoryPaths?: string[] }> {
  const { task: titledTask, titleUpdated } = await refreshTaskSessionTitle(tasks, task, ollama);
  task = titledTask;

  if (options?.skipIfPresent && task.handoffNote?.trim()) {
    return { ok: true, task, usedFallback: false, titleUpdated };
  }

  const hookEvent = options?.hookEvent ?? "Stop";
  const events = tasks.getEvents(task.id, 50);
  const input = enrichHookInput(hookInputFromTask(task, hookEvent), { events });
  const boardSessionId = task.originSessionId ?? "";
  const eventsForSummary = events.map((e) => ({
    eventType: e.eventType,
    payloadJson: JSON.stringify(e.payload),
  }));
  const suggestedStatus =
    options?.suggestedStatus ?? inferStatusFromStop(input, hookEvent) ?? task.status;

  let result: SessionSummaryResult | null = await summarizeSessionTurn(ollama, {
    task,
    input,
    boardSessionId,
    events: eventsForSummary,
    suggestedStatus,
  });

  let usedFallback = false;
  if (!result) {
    result = fallbackSummary(task, input, suggestedStatus);
    usedFallback = true;
  }

  const markdown = formatSessionSummaryMarkdown(result, task);
  const status = coerceSummaryStatus(
    result.status === "done" && task.status !== "done" ? "review" : result.status,
  );
  const updated = tasks.applySessionSummary(task.id, markdown, status);

  let memoryPaths: string[] | undefined;
  if (options?.memory) {
    try {
      if (options.awaitMemory) {
        memoryPaths = await options.memory.syncAfterSummary(updated.id);
      } else {
        void options.memory.syncAfterSummary(updated.id).catch((err) => {
          console.warn(`[swarm] memory sync failed for ${updated.key}:`, err);
        });
      }
    } catch (err) {
      console.warn(`[swarm] memory sync failed for ${updated.key}:`, err);
    }
  }

  return { ok: true, task: updated, usedFallback, titleUpdated, memoryPaths };
}

function coerceSummaryStatus(status: TaskStatus): TaskStatus {
  if (status === "backlog" || status === "archived") return "ready";
  return status;
}
