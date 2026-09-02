import { existsSync } from "node:fs";
import type { HookPlatform } from "./hooks.js";
import type { KbStore } from "./kb.js";
import type { SessionService } from "./sessions.js";
import type { TaskService } from "./tasks.js";
import { extractTranscriptText, resolveTranscriptPath } from "./transcripts.js";

const HOOK_PLATFORMS = new Set<string>(["claude", "cursor", "codex", "antigravity", "opencode"]);

function reason(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** Embeds a task, its markdown artifacts, and its agent transcripts into the knowledge base. */
export class KbIndexer {
  constructor(
    private kb: KbStore,
    private tasks: TaskService,
    private sessions: SessionService,
  ) {}

  /** kb/tasks/<KEY>.md from title + summary + tags. Called on create and update. */
  async indexTask(taskId: number): Promise<string | null> {
    const task = this.tasks.getById(taskId);
    if (!task) return null;
    const agents = [...new Set(this.sessions.listByTask(taskId).map((s) => s.agentKind))];
    const path = this.kb.writeDoc(
      "tasks",
      `${task.key}.md`,
      {
        title: task.title,
        task: task.key,
        status: task.status,
        tags: this.tasks.getTags(taskId),
        agents: agents.length > 0 ? agents : [task.originAgent],
        type: "task",
      },
      task.handoffNote?.trim() || task.initialContext?.trim() || task.title,
    );
    await this.kb.indexFile(path);
    return path;
  }

  /** Index every *.md path recorded in artifacts_json.files that still exists. */
  async indexArtifacts(taskId: number): Promise<string[]> {
    const task = this.tasks.getById(taskId);
    if (!task) return [];
    let files: unknown;
    try {
      files = (JSON.parse(task.artifactsJson) as { files?: unknown }).files;
    } catch {
      return [];
    }
    if (!Array.isArray(files)) return [];
    const paths = files.filter(
      (file): file is string =>
        typeof file === "string" && file.endsWith(".md") && existsSync(file),
    );
    for (const path of paths) {
      await this.kb.indexFile(path);
    }
    return paths;
  }

  /** kb/transcripts/<KEY>-<sessionId>.md rendered from the agent transcript. */
  async indexTranscript(taskId: number, sessionId: string): Promise<string | null> {
    const task = this.tasks.getById(taskId);
    const session = this.sessions.get(sessionId);
    if (!task || !session || !HOOK_PLATFORMS.has(session.agentKind)) return null;
    const platform = session.agentKind as HookPlatform;

    let transcriptPath = session.transcriptPath;
    if (!transcriptPath) {
      transcriptPath =
        resolveTranscriptPath(
          {
            platform,
            // A subagent's transcript hangs off its parent session's project dir.
            sessionId: session.parentSessionId ?? session.id,
            agentId: session.parentSessionId ? session.id : undefined,
            cwd: session.cwd ?? task.originCwd ?? task.repoPath ?? process.cwd(),
            transcriptPath: undefined,
          },
          session.id,
        ) ?? null;
      if (!transcriptPath) return null;
      this.sessions.upsert({ id: session.id, agent: session.agentKind, transcriptPath });
    }

    const body = extractTranscriptText(platform, transcriptPath);
    if (!body.trim()) return null;
    const path = this.kb.writeDoc(
      "transcripts",
      `${task.key}-${sessionId}.md`,
      {
        title: `Transcript: ${task.title}`,
        task: task.key,
        session: sessionId,
        agent: session.agentKind,
        type: "transcript",
      },
      body,
    );
    await this.kb.indexFile(path);
    return path;
  }

  /** indexTask + indexArtifacts + indexTranscript, never throws. */
  async ingestSession(taskId: number, sessionId: string): Promise<string[]> {
    const paths: string[] = [];
    try {
      const path = await this.indexTask(taskId);
      if (path) paths.push(path);
    } catch (error) {
      console.warn(`swarm: task ${taskId} not indexed: ${reason(error)}`);
    }
    try {
      paths.push(...(await this.indexArtifacts(taskId)));
    } catch (error) {
      console.warn(`swarm: artifacts of task ${taskId} not indexed: ${reason(error)}`);
    }
    try {
      const path = await this.indexTranscript(taskId, sessionId);
      if (path) paths.push(path);
    } catch (error) {
      console.warn(`swarm: transcript of session ${sessionId} not indexed: ${reason(error)}`);
    }
    return paths;
  }
}
