import {
  enrichHookInput,
  inferStatusFromStop,
  summarizeSessionTurn,
  fallbackSummary,
  formatSessionSummaryMarkdown,
  syncTaskSessionMetadata,
  resolveBoardSessionId,
  type HookPlatform,
} from "@swarm/core";
import type { NormalizedHookInput, TaskRecord } from "@swarm/core";
import type { SwarmContext } from "./context.js";

/** Platform-specific hooks that trigger Ollama session summarization. */
const SUMMARY_EVENTS_BY_PLATFORM: Record<HookPlatform, ReadonlySet<string>> = {
  cursor: new Set(["sessionEnd", "subagentStop", "afterAgentResponse", "stop"]),
  claude: new Set(["SessionEnd", "Stop", "SubagentStop", "TaskCompleted"]),
  codex: new Set(["SessionEnd", "SubagentStop", "Stop"]),
  antigravity: new Set(["PostInvocation", "Stop"]),
};

export function shouldScheduleTurnSummary(platform: HookPlatform, hookEvent: string): boolean {
  return SUMMARY_EVENTS_BY_PLATFORM[platform]?.has(hookEvent) ?? false;
}

export function scheduleTurnSummary(
  ctx: SwarmContext,
  task: TaskRecord,
  input: NormalizedHookInput,
  hookEvent: string,
): void {
  if (!shouldScheduleTurnSummary(input.platform, hookEvent)) return;
  void runTurnSummary(ctx, task, input, hookEvent).catch((err) => {
    console.warn(`[swarm] summary failed for ${task.key}:`, err);
  });
}

async function runTurnSummary(
  ctx: SwarmContext,
  task: TaskRecord,
  input: NormalizedHookInput,
  hookEvent: string,
): Promise<void> {
  input.hookEvent = hookEvent;
  enrichHookInput(input, { events: ctx.tasks.getEvents(task.id, 50) });
  const { task: synced } = syncTaskSessionMetadata(ctx.tasks, task, input);
  task = synced;

  const boardSessionId = task.originSessionId ?? resolveBoardSessionId(input) ?? input.sessionId;
  const events = ctx.tasks.getEvents(task.id, 50).map((e) => ({
    eventType: e.eventType,
    payloadJson: JSON.stringify(e.payload),
  }));

  const suggestedStatus = inferStatusFromStop(input, hookEvent);
  let result =
    (await summarizeSessionTurn(ctx.ollama, {
      task,
      input: { ...input, hookEvent },
      boardSessionId,
      events,
      suggestedStatus,
    })) ?? fallbackSummary(task, input, suggestedStatus ?? "review");

  const markdown = formatSessionSummaryMarkdown(result, task);
  const status =
    hookEvent === "SessionEnd" || hookEvent === "sessionEnd"
      ? task.status === "done"
        ? "done"
        : "ready"
      : result.status;

  const updated = ctx.tasks.applySessionSummary(task.id, markdown, status);
  void ctx.memory.syncAfterSummary(updated.id).catch((err) => {
    console.warn(`[swarm] memory sync failed for ${updated.key}:`, err);
  });
  ctx.broadcast({ type: "task_updated", task: updated });
}
