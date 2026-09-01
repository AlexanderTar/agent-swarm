import { z } from "zod";
import type { OllamaClient } from "./ollama.js";
import type { KbStore } from "./kb.js";
import type { TaskService } from "./tasks.js";

const ExtractSchema = z.object({
  notes: z.array(
    z.object({
      title: z.string(),
      body: z.string(),
      tags: z.array(z.string()).optional(),
    }),
  ),
});

const ComposeSchema = z.object({
  title: z.string(),
  body: z.string(),
  tags: z.array(z.string()),
  supersedes: z.array(z.string()).optional(),
});

export class MemoryJobs {
  constructor(
    private ollama: OllamaClient,
    private kb: KbStore,
    private tasks: TaskService,
  ) {}

  async extractFromTask(taskId: number): Promise<string[]> {
    const task = this.tasks.getById(taskId);
    if (!task) return [];
    const events = this.tasks.getEvents(taskId, 30);
    const context = [
      `Task: ${task.key} — ${task.title}`,
      task.initialContext ?? "",
      task.handoffNote ?? "",
      events.map((e) => `[${e.eventType}] ${JSON.stringify(e.payload)}`).join("\n"),
    ].join("\n\n");

    try {
      const result = await this.ollama.chat({
        user: `Extract atomic reference notes from this agent session:\n\n${context.slice(0, 8000)}`,
        schema: ExtractSchema,
        schemaDescription: "Extract 1-5 concise notes with title, body, optional tags.",
      });
      const paths: string[] = [];
      for (const note of result.notes) {
        const slug = note.title.toLowerCase().replace(/[^a-z0-9]+/g, "-").slice(0, 40);
        const path = this.kb.writeDoc("inbox", `${slug}.md`, { title: note.title, tags: note.tags ?? [], task: task.key }, note.body);
        await this.kb.indexFile(path);
        paths.push(path);
      }
      return paths;
    } catch {
      return [];
    }
  }

  /** Index session summary into KB and extract atomic notes after summarisation. */
  async syncAfterSummary(taskId: number): Promise<string[]> {
    const task = this.tasks.getById(taskId);
    if (!task?.handoffNote?.trim()) return [];

    const paths: string[] = [];
    const summaryPath = this.kb.writeDoc(
      "handoffs",
      `${task.key}-summary.md`,
      {
        title: `Summary: ${task.title}`,
        task: task.key,
        agent: task.originAgent,
        type: "session-summary",
        sessionId: task.originSessionId,
      },
      task.handoffNote,
    );
    await this.kb.indexFile(summaryPath);
    paths.push(summaryPath);

    const extracted = await this.extractFromTask(taskId);
    paths.push(...extracted);
    return paths;
  }

  async composeInbox(): Promise<number> {
    // Placeholder: in production would cluster inbox notes
    return 0;
  }

  async compactNotes(): Promise<number> {
    // Placeholder: mark superseded near-duplicates
    return 0;
  }
}
