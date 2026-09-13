import type { SwarmContext } from "./context.js";

export function startScheduler(ctx: SwarmContext): () => void {
  const intervals: NodeJS.Timeout[] = [];

  intervals.push(
    setInterval(() => {
      const reclaimed = ctx.tasks.reaperExpiredClaims();
      for (const task of reclaimed) {
        ctx.broadcast({ type: "task_updated", task });
      }
    }, 60_000),
  );

  return () => {
    for (const id of intervals) clearInterval(id);
  };
}
