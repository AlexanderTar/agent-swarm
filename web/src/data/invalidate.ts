import type { BoardEvent } from "../types";

export function keysForEvent(e: BoardEvent): string[] {
  switch (e.type) {
    case "reset":
      return [""];
    case "item.changed":
      return ["items", "item:", "graph:"];
    case "agent.changed":
      return ["agents", "item:", "advice:"];
    case "checkpoint.created": {
      const item = (e.data as { item?: string } | null)?.item;
      return [item ? `checkpoints:${item}` : "checkpoints:", "item:"];
    }
    case "request.opened":
    case "request.resolved":
      return ["requests", "items", "item:"];
    case "settings.changed":
      return ["settings"];
    case "catalog.changed":
      return ["catalog"];
    case "repos.changed": // P1 addition (contracts doc §5)
      return ["repos:"];
    default:
      return [];
  }
}
