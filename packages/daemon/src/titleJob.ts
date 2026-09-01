import { summarizeTaskTitle, needsTitleRefresh } from "@swarm/core";
import type { TaskRecord } from "@swarm/core";
import type { SwarmContext } from "./context.js";

const RETRY_MS = [0, 1500, 4000, 10000];

/** Resolve a short Ollama title from the subagent/session transcript (with retries while the file appears). */
export function scheduleTitleRefresh(ctx: SwarmContext, task: TaskRecord, options?: { force?: boolean }): void {
  if (!options?.force && !needsTitleRefresh(task)) return;

  void (async () => {
    let current = task;
    for (let i = 0; i < RETRY_MS.length; i++) {
      const wait = RETRY_MS[i]!;
      if (wait > 0) await new Promise((r) => setTimeout(r, wait));
      try {
        const latest = ctx.tasks.getById(current.id) ?? current;
        const { task: updated, titleUpdated } = await summarizeTaskTitle(ctx.ollama, ctx.tasks, latest, {
          force: options?.force || needsTitleRefresh(latest),
        });
        current = updated;
        if (titleUpdated) {
          ctx.broadcast({ type: "task_updated", task: updated });
          return;
        }
        // Transcript may not exist yet on SubagentStart — keep retrying.
        if (!needsTitleRefresh(updated)) return;
      } catch (err) {
        console.warn(`[swarm] title refresh failed for ${current.key}:`, err);
      }
    }
  })();
}
