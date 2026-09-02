import type { SwarmContext } from "./context.js";

/** Only job left: hand back tasks whose exclusive claim lease expired. */
export function startScheduler(ctx: SwarmContext): () => void {
  const reaper = setInterval(() => {
    for (const task of ctx.tasks.reaperExpiredClaims()) {
      ctx.broadcast({ type: "task_updated", task });
    }
  }, 60_000);

  return () => clearInterval(reaper);
}
