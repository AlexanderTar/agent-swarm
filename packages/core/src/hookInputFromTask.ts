import type { HookPlatform, NormalizedHookInput } from "./hooks.js";
import type { AgentKind, TaskRecord } from "./types.js";

function platformFromAgent(agent: AgentKind): HookPlatform {
  if (agent === "claude" || agent === "cursor" || agent === "codex" || agent === "antigravity") return agent;
  return "cursor";
}

export function hookInputFromTask(task: TaskRecord, hookEvent = "Stop"): NormalizedHookInput {
  return {
    platform: platformFromAgent(task.originAgent),
    sessionId: task.originSessionId ?? "",
    cwd: task.originCwd ?? task.repoPath ?? process.cwd(),
    hookEvent,
    model: task.originModel ?? undefined,
    raw: {},
  };
}
