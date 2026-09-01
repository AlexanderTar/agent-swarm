import { z } from "zod";
import type { NormalizedHookInput } from "./hooks.js";
import type { OllamaClient } from "./ollama.js";
import { buildTranscriptContext } from "./transcripts.js";
import type { TaskRecord, TaskStatus } from "./types.js";
import type { TaskService } from "./tasks.js";

const summarySchema = z.object({
  goal: z.string(),
  done: z.string(),
  nextSteps: z.array(z.string()).max(5),
  openQuestions: z.array(z.string()).max(5),
  status: z.enum(["in_progress", "review", "ready", "blocked", "done"]),
});

export type SessionSummaryResult = z.infer<typeof summarySchema>;

function toSummaryStatus(status: TaskStatus): SessionSummaryResult["status"] {
  if (status === "backlog" || status === "archived") return "ready";
  return status;
}

export function inferStatusFromStop(input: NormalizedHookInput, hookEvent: string): TaskStatus | undefined {
  const rawStatus = typeof input.raw.status === "string" ? input.raw.status.toLowerCase() : "";
  if (hookEvent === "SessionEnd" || hookEvent === "sessionEnd") {
    return "ready";
  }
  if (rawStatus === "error" || rawStatus === "aborted") return "blocked";
  if (hookEvent === "StopFailure") return "blocked";
  if (hookEvent === "Stop" || hookEvent === "stop" || hookEvent === "SubagentStop" || hookEvent === "subagentStop") {
    return "review";
  }
  if (hookEvent === "PostInvocation" || hookEvent === "afterAgentResponse" || hookEvent === "TaskCompleted") {
    return "review";
  }
  return undefined;
}

export function formatSessionSummaryMarkdown(result: SessionSummaryResult, task: Pick<TaskRecord, "key" | "title" | "originAgent">): string {
  const next = result.nextSteps.length ? result.nextSteps.map((s, i) => `${i + 1}. ${s}`).join("\n") : "_None_";
  const open = result.openQuestions.length ? result.openQuestions.map((q) => `- ${q}`).join("\n") : "_None_";
  return [
    `# Summary: ${task.key} — ${task.title}`,
    "",
    `**Agent:** ${task.originAgent}`,
    "",
    "## Goal",
    result.goal,
    "",
    "## Done this turn",
    result.done,
    "",
    "## Next steps",
    next,
    "",
    "## Open questions",
    open,
  ].join("\n");
}

export async function summarizeSessionTurn(
  ollama: OllamaClient,
  params: {
    task: Pick<TaskRecord, "key" | "title" | "originAgent" | "status" | "turnCount">;
    input: NormalizedHookInput;
    boardSessionId: string;
    events: Array<{ eventType: string; payloadJson?: string }>;
    suggestedStatus?: TaskStatus;
  },
): Promise<SessionSummaryResult | null> {
  const transcript = buildTranscriptContext(
    params.input,
    params.boardSessionId,
    params.events,
    params.input.lastAssistantMessage,
  );
  if (!transcript.trim() && !params.input.lastAssistantMessage) return null;

  const suggested = params.suggestedStatus ?? inferStatusFromStop(params.input, params.input.hookEvent) ?? "review";

  try {
    return await ollama.chat({
      system:
        "You summarize coding agent sessions for a Kanban board. Be concise and factual. " +
        "Infer goal, what was accomplished this turn, sensible next steps, and open questions. " +
        "Pick status: in_progress (still actively working), review (turn finished, needs review), " +
        "ready (paused/hand-off point), blocked (errors/user input needed), done (task complete).",
      user: [
        `Task: ${params.task.key} — ${params.task.title}`,
        `Agent: ${params.task.originAgent}`,
        `Current board status: ${params.task.status}`,
        `Turn count: ${params.task.turnCount}`,
        `Hook event: ${params.input.hookEvent}`,
        `Suggested status: ${suggested}`,
        "",
        "Transcript (recent):",
        transcript.slice(0, 20_000),
      ].join("\n"),
      schema: summarySchema,
      schemaDescription: "Return JSON with goal, done, nextSteps, openQuestions, status.",
    });
  } catch {
    return null;
  }
}

export function fallbackSummary(
  task: Pick<TaskRecord, "key" | "title" | "originAgent">,
  input: NormalizedHookInput,
  suggestedStatus: TaskStatus,
): SessionSummaryResult {
  const latest = input.lastAssistantMessage?.trim() || input.prompt?.trim() || "No transcript captured.";
  return {
    goal: task.title,
    done: latest.slice(0, 500),
    nextSteps: [],
    openQuestions: [],
    status: toSummaryStatus(suggestedStatus),
  };
}
