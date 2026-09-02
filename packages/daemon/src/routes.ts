import type { FastifyInstance } from "fastify";
import {
  agentKindFromPlatform,
  classifyHookEvent,
  formatHookOutput,
  hookSessionKey,
  mapAntigravityTool,
  normalizeHookInput,
  resolveTranscriptPath,
  sessionBriefing,
  shortReference,
  type AgentKind,
  type HookPlatform,
  type NormalizedHookInput,
  type TaskRecord,
} from "@swarm/core";
import type { SwarmContext } from "./context.js";

/** The board item a hook's session is attached to, or null when the agent never created one. */
function boundTask(ctx: SwarmContext, sessionId: string): TaskRecord | null {
  const session = ctx.sessions.get(sessionId);
  if (!session) return null;
  if (session.taskId) return ctx.tasks.getById(session.taskId);
  // A subagent can start before its parent creates the item — adopt it late.
  const parentTaskId = session.parentSessionId ? ctx.sessions.get(session.parentSessionId)?.taskId : null;
  if (!parentTaskId) return null;
  ctx.sessions.bind(session.id, parentTaskId);
  return ctx.tasks.getById(parentTaskId);
}

function briefingFor(ctx: SwarmContext, sessionId: string): string {
  const task = boundTask(ctx, sessionId);
  return sessionBriefing({
    sessionId,
    boardUrl: ctx.boardUrl,
    task: task ? { key: task.key, title: task.title, status: task.status } : null,
  });
}

function transcriptFor(input: NormalizedHookInput, sessionId: string): string | undefined {
  try {
    return input.transcriptPath ?? resolveTranscriptPath(input, sessionId);
  } catch {
    return input.transcriptPath;
  }
}

function editedFilePath(toolInput: Record<string, unknown> | undefined): string | undefined {
  const value = toolInput?.file_path ?? toolInput?.TargetFile ?? toolInput?.path ?? toolInput?.filePath;
  return typeof value === "string" && value.trim() ? value : undefined;
}

function ingest(ctx: SwarmContext, taskId: number, sessionId: string): void {
  void ctx.indexer
    .ingestSession(taskId, sessionId)
    .then(() => ctx.broadcast({ type: "kb_updated" }))
    .catch((err) => console.warn(`[swarm] KB ingest failed for task ${taskId}:`, err));
}

/** Stop fires after every assistant turn — too hot to re-embed a whole transcript. */
function ingestArtifacts(ctx: SwarmContext, taskId: number): void {
  void ctx.indexer
    .indexArtifacts(taskId)
    .then((paths) => paths.length > 0 && ctx.broadcast({ type: "kb_updated" }))
    .catch((err) => console.warn(`[swarm] artifact index failed for task ${taskId}:`, err));
}

export async function registerHookRoutes(app: FastifyInstance, ctx: SwarmContext): Promise<void> {
  app.post<{ Params: { platform: string; event: string }; Body: Record<string, unknown> }>(
    "/hooks/:platform/:event",
    async (req, reply) => {
      const platform = req.params.platform as HookPlatform;
      const event = req.params.event;
      const input = normalizeHookInput({ ...req.body, hook_event_name: event }, platform);
      input.hookEvent = event;

      const agent: AgentKind = agentKindFromPlatform(platform);
      const sessionId = hookSessionKey(input);
      if (!sessionId) return reply.send(formatHookOutput(platform, {}, event));

      const kind = classifyHookEvent(event);

      if (kind === "session_start" || kind === "subagent_start") {
        const parentSessionId =
          kind === "subagent_start" ? (input.parentSessionId ?? input.sessionId) : input.parentSessionId;
        ctx.sessions.upsert({
          id: sessionId,
          agent,
          cwd: input.cwd,
          model: input.model,
          pid: input.raw.pid as number | undefined,
          parentSessionId: parentSessionId && parentSessionId !== sessionId ? parentSessionId : undefined,
          transcriptPath: transcriptFor(input, sessionId),
        });
        // A subagent works on whatever its parent is working on — never its own tile.
        const parentTaskId = parentSessionId ? ctx.sessions.get(parentSessionId)?.taskId : null;
        if (parentTaskId) ctx.sessions.bind(sessionId, parentTaskId);
        return reply.send(
          formatHookOutput(platform, { additionalContext: briefingFor(ctx, sessionId) }, event),
        );
      }

      // Every other event only touches a session that already exists.
      ctx.sessions.touch(sessionId);
      const task = boundTask(ctx, sessionId);

      if (kind === "session_end") {
        ctx.sessions.end(sessionId);
        if (task) {
          ingest(ctx, task.id, sessionId);
          ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
        }
        return reply.send(formatHookOutput(platform, {}, event));
      }

      if (!task) return reply.send(formatHookOutput(platform, {}, event));

      switch (kind) {
        case "prompt": {
          if (input.prompt?.trim()) {
            ctx.tasks.incrementTurn(task.id);
            ctx.tasks.appendEvent(task.id, "prompt", { prompt: input.prompt.slice(0, 500) });
          } else {
            ctx.tasks.touch(task.id);
          }
          break;
        }
        case "tool": {
          const tool = platform === "antigravity" ? mapAntigravityTool(input.toolName) : input.toolName;
          if (tool === "Write" || tool === "Edit" || tool === "WriteToFile" || tool === "write") {
            const file = editedFilePath(input.toolInput);
            if (file) ctx.tasks.addArtifact(task.id, "files", file);
          }
          ctx.tasks.appendEvent(task.id, event, { tool, input: input.toolInput });
          break;
        }
        case "turn_end": {
          ctx.tasks.touch(task.id);
          ingestArtifacts(ctx, task.id);
          break;
        }
        case "notification":
        case "compact": {
          ctx.tasks.appendEvent(task.id, kind, { event });
          break;
        }
        default:
          ctx.tasks.touch(task.id);
      }

      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
      return reply.send(formatHookOutput(platform, {}, event));
    },
  );

  app.post("/hooks/session/register", async (req, reply) => {
    const body = req.body as { sessionId: string; agent?: AgentKind; cwd?: string; pid?: number };
    if (!body?.sessionId?.trim()) return reply.code(400).send({ error: "sessionId required" });
    ctx.sessions.upsert({
      id: body.sessionId,
      agent: body.agent ?? "unknown",
      cwd: body.cwd ?? process.cwd(),
      pid: body.pid,
    });
    return reply.send({
      ok: true,
      sessionId: body.sessionId,
      reference: shortReference("Swarm session registered", ctx.boardUrl),
    });
  });

  app.post("/hooks/session/end", async (req, reply) => {
    const body = req.body as { sessionId: string };
    const session = body?.sessionId ? ctx.sessions.get(body.sessionId) : null;
    ctx.sessions.end(body?.sessionId ?? "");
    if (session?.taskId) {
      ingest(ctx, session.taskId, session.id);
      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(session.taskId) });
    }
    return reply.send({ ok: true });
  });
}

export async function registerApiRoutes(app: FastifyInstance, ctx: SwarmContext): Promise<void> {
  app.get("/api/health", async () => ({ ok: true, version: "0.1.0" }));

  app.get("/api/bootstrap", async (req, reply) => {
    const ip = req.ip;
    if (ip !== "127.0.0.1" && ip !== "::1" && ip !== "::ffff:127.0.0.1") {
      return reply.code(403).send({ error: "Loopback only" });
    }
    return { token: ctx.token, wsUrl: `/ws` };
  });

  app.get("/api/status", async () => ({
    tasks: ctx.tasks.list().length,
    ollama: await ctx.ollama.preflight(),
  }));

  app.get("/api/board", async (req) => {
    const q = req.query as Record<string, string>;
    return ctx.tasks.list({
      status: q.status as never,
      repo: q.repo,
      agent: q.agent as AgentKind,
      tag: q.tag,
      stale: q.stale === "true",
    });
  });

  app.post("/api/tasks", async (req, reply) => {
    const body = req.body as {
      title?: string;
      summary?: string;
      status?: string;
      tags?: string[];
      initialContext?: string;
      agent?: AgentKind;
      sessionId?: string;
      cwd?: string;
      model?: string;
    };
    const task = ctx.tasks.create({
      title: body.title ?? "Untitled",
      summary: body.summary,
      status: (body.status as never) ?? "ready",
      tags: body.tags,
      initialContext: body.initialContext,
      originAgent: body.agent ?? "unknown",
      originSessionId: body.sessionId,
      originCwd: body.cwd ?? process.cwd(),
      originModel: body.model,
      repoPath: body.cwd,
    });
    if (body.sessionId) ctx.sessions.bind(body.sessionId, task.id);
    void ctx.indexer.indexTask(task.id).catch(() => {});
    ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
    return reply.code(201).send(task);
  });

  app.get("/api/tasks/:key", async (req, reply) => {
    const { key } = req.params as { key: string };
    const task = ctx.tasks.getByKey(key);
    if (!task) return reply.code(404).send({ error: `Task not found: ${key}` });
    return {
      ...task,
      tags: ctx.tasks.getTags(task.id),
      sessions: ctx.sessions.labelsForTasks([task.id]).get(task.id) ?? [],
      events: ctx.tasks.getEvents(task.id),
      subtasks: ctx.tasks.getSubtasks(task.id),
    };
  });

  app.patch("/api/tasks/:key", async (req, reply) => {
    const { key } = req.params as { key: string };
    const task = ctx.tasks.getByKey(key);
    if (!task) return reply.code(404).send({ error: `Task not found: ${key}` });
    const body = req.body as Record<string, unknown>;
    const updated = ctx.tasks.update(task.id, {
      title: body.title as string | undefined,
      status: body.status as never,
      summary: body.summary as string | undefined,
      tags: body.tags as string[] | undefined,
    });
    if (body.title !== undefined || body.summary !== undefined || body.tags !== undefined) {
      void ctx.indexer.indexTask(task.id).catch(() => {});
    }
    ctx.broadcast({ type: "task_updated", task: updated });
    return updated;
  });

  app.get("/api/kb/search", async (req) => {
    const q = req.query as { q?: string; subdir?: string; limit?: string };
    return ctx.kb.search(q.q ?? "", Number(q.limit) || 10, { subdir: q.subdir });
  });

  app.get("/api/kb/:slug", async (req, reply) => {
    const { slug } = req.params as { slug: string };
    const doc = ctx.kb.getDoc(slug);
    if (!doc) return reply.code(404).send({ error: "Not found" });
    return doc;
  });

  app.post("/api/kb", async (req) => {
    const body = req.body as {
      subdir?: string;
      filename: string;
      frontmatter?: Record<string, unknown>;
      body: string;
    };
    const path = ctx.kb.writeDoc(body.subdir ?? "notes", body.filename, body.frontmatter ?? {}, body.body);
    try {
      await ctx.kb.indexFile(path);
      return { path, indexed: true };
    } catch (err) {
      console.warn(`[swarm] KB index failed for ${path}:`, err);
      return { path, indexed: false, indexError: err instanceof Error ? err.message : String(err) };
    }
  });

  app.post("/api/chat", async (req, reply) => {
    const body = req.body as { messages: Array<{ role: string; content: string }> };
    try {
      const res = await fetch(`${ctx.config.ollamaUrl}/api/chat`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          model: ctx.config.chatModel,
          messages: body.messages,
          stream: false,
          keep_alive: "30m",
        }),
      });
      return await res.json();
    } catch (e) {
      return reply.code(503).send({ error: String(e) });
    }
  });
}
