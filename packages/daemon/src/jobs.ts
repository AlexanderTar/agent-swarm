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

  intervals.push(
    setInterval(() => {
      const archived = ctx.tasks.janitorArchive({
        idleMinutes: ctx.config.janitorIdleMinutes,
        minTurns: ctx.config.janitorMinTurns,
      });
      if (archived > 0) ctx.broadcast({ type: "board_updated" });
    }, 5 * 60_000),
  );

  intervals.push(
    setInterval(() => {
      void ctx.memory.composeInbox();
      void ctx.memory.compactNotes();
    }, 60 * 60_000),
  );

  return () => {
    for (const id of intervals) clearInterval(id);
  };
}
