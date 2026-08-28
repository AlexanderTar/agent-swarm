import type { FastifyInstance } from "fastify";
import {
  agentKindFromPlatform,
  mapAntigravityTool,
  normalizeHookInput,
  shortReference,
  type AgentKind,
} from "@swarm/core";
import type { SwarmContext } from "./context.js";

function readString(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

export async function registerHookRoutes(app: FastifyInstance, ctx: SwarmContext): Promise<void> {
  app.post<{ Params: { platform: string; event: string }; Body: Record<string, unknown> }>(
    "/hooks/:platform/:event",
    async (req, reply) => {
      const platform = req.params.platform as "claude" | "cursor" | "codex" | "antigravity";
      const event = req.params.event;
      const input = normalizeHookInput({ ...req.body, hook_event_name: event }, platform);
      input.hookEvent = event;

      const agent = agentKindFromPlatform(platform);
      let task = input.sessionId ? ctx.tasks.getBySession(input.sessionId) : null;

      switch (event) {
        case "SessionStart":
        case "sessionStart": {
          task = ctx.tasks.upsertSessionTask({
            sessionId: input.sessionId,
            agent,
            cwd: input.cwd,
            model: input.model,
          });
          ctx.broadcast({ type: "task_updated", task });
          return reply.send({ ok: true, taskKey: task.key });
        }
        case "UserPromptSubmit":
        case "beforeSubmitPrompt": {
          if (!task && input.sessionId) {
            task = ctx.tasks.upsertSessionTask({ sessionId: input.sessionId, agent, cwd: input.cwd });
          }
          if (task && input.prompt) {
            if (!task.initialContext) {
              ctx.tasks.update(task.id, { initialContext: input.prompt });
              void ctx.ollama.summarize(input.prompt).then((title) => {
                ctx.tasks.update(task!.id, { title });
                ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task!.id) });
              });
            }
            ctx.tasks.incrementTurn(task.id);
            ctx.tasks.appendEvent(task.id, "prompt", { prompt: input.prompt.slice(0, 500) });
          }
          ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task!.id) });
          return reply.send({ ok: true });
        }
        case "PreToolUse":
        case "preToolUse":
        case "PostToolUse":
        case "postToolUse":
        case "afterFileEdit": {
          if (!task && input.sessionId) {
            task = ctx.tasks.upsertSessionTask({ sessionId: input.sessionId, agent, cwd: input.cwd });
          }
          if (task) {
            const tool = platform === "antigravity" ? mapAntigravityTool(input.toolName) : input.toolName;
            if (tool === "Write" || tool === "Edit" || tool === "WriteToFile") {
              const fp = (input.toolInput?.file_path ?? input.toolInput?.TargetFile ?? input.toolInput?.path) as string | undefined;
              if (fp) ctx.tasks.addArtifact(task.id, "files", fp);
            }
            ctx.tasks.appendEvent(task.id, event, { tool, input: input.toolInput });
          }
          return reply.send({ ok: true });
        }
        case "Notification": {
          if (task) {
            ctx.tasks.update(task.id, { status: "blocked" });
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "Stop":
        case "stop": {
          if (task) {
            ctx.tasks.update(task.id, { status: "review" });
            if (input.lastAssistantMessage) {
              ctx.tasks.appendEvent(task.id, "stop_summary", { summary: input.lastAssistantMessage.slice(0, 1000) });
            }
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "PreCompact":
        case "preCompact": {
          if (task) {
            ctx.tasks.appendEvent(task.id, "pre_compact", { trigger: "compact" });
            const note = task.handoffNote ?? task.initialContext ?? "";
            if (note) {
              ctx.kb.writeDoc("handoffs", `${task.key}-compact.md`, { task: task.key, type: "compact" }, note);
            }
          }
          return reply.send({ ok: true });
        }
        case "SessionEnd":
        case "sessionEnd": {
          if (task) {
            const status = task.status === "done" ? "done" : task.status === "review" ? "handoff" : "handoff";
            ctx.tasks.update(task.id, { status: status as "done" | "handoff" });
            void ctx.memory.extractFromTask(task.id);
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "StopFailure": {
          if (task) {
            ctx.tasks.update(task.id, { status: "blocked" });
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "SubagentStart":
        case "subagentStart": {
          if (task && input.agentType) ctx.tasks.addSubtask(task.id, input.agentType);
          return reply.send({ ok: true });
        }
        case "SubagentStop":
        case "subagentStop": {
          if (task && input.agentType) ctx.tasks.completeSubtask(task.id, input.agentType);
          return reply.send({ ok: true });
        }
        case "TaskCreated": {
          if (task) {
            const subject = (input.raw.task_subject as string) ?? "Subtask";
            ctx.tasks.addSubtask(task.id, subject, input.raw.task_description as string | undefined);
          }
          return reply.send({ ok: true });
        }
        case "TaskCompleted": {
          if (task) {
            const subject = (input.raw.task_subject as string) ?? "";
            if (subject) ctx.tasks.completeSubtask(task.id, subject);
          }
          return reply.send({ ok: true });
        }
        case "PreInvocation": {
          const sid = input.sessionId || readString(input.raw.conversationId as unknown) || "";
          if (sid) {
            task = ctx.tasks.getBySession(sid) ?? task;
            if (task) ctx.tasks.touch(task.id);
          }
          return reply.send({ decision: "allow" });
        }
        case "PostInvocation": {
          if (task) ctx.tasks.touch(task.id);
          return reply.send({ ok: true });
        }
        default:
          return reply.send({ ok: true });
      }
    },
  );

  app.post("/hooks/session/register", async (req, reply) => {
    const body = req.body as { sessionId: string; agent: AgentKind; cwd?: string; pid?: number };
    const task = ctx.tasks.upsertSessionTask({
      sessionId: body.sessionId,
      agent: body.agent ?? "unknown",
      cwd: body.cwd,
      pid: body.pid,
    });
    ctx.broadcast({ type: "task_updated", task });
    return reply.send({ ok: true, taskKey: task.key, reference: shortReference(`Session registered as ${task.key}`, `http://${ctx.config.host}:${ctx.config.port}/`) });
  });

  app.post("/hooks/session/end", async (req, reply) => {
    const body = req.body as { sessionId: string };
    const task = ctx.tasks.getBySession(body.sessionId);
    if (task) {
      ctx.tasks.update(task.id, { status: "handoff" });
      void ctx.memory.extractFromTask(task.id);
      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
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
      stale: q.stale === "true",
    });
  });

  app.get("/api/tasks/:key", async (req, reply) => {
    const { key } = req.params as { key: string };
    const task = ctx.tasks.getByKey(key);
    if (!task) return reply.code(404).send({ error: "Not found" });
    return {
      ...task,
      events: ctx.tasks.getEvents(task.id),
      subtasks: ctx.tasks.getSubtasks(task.id),
    };
  });

  app.patch("/api/tasks/:key", async (req, reply) => {
    const { key } = req.params as { key: string };
    const task = ctx.tasks.getByKey(key);
    if (!task) return reply.code(404).send({ error: "Not found" });
    const body = req.body as Record<string, unknown>;
    const updated = ctx.tasks.update(task.id, {
      title: body.title as string | undefined,
      status: body.status as never,
    });
    ctx.broadcast({ type: "task_updated", task: updated });
    return updated;
  });

  app.get("/api/kb/search", async (req) => {
    const q = (req.query as { q?: string }).q ?? "";
    return ctx.kb.search(q, 10);
  });

  app.get("/api/kb/:slug", async (req, reply) => {
    const { slug } = req.params as { slug: string };
    const doc = ctx.kb.getDoc(slug);
    if (!doc) return reply.code(404).send({ error: "Not found" });
    return doc;
  });

  app.post("/api/kb", async (req) => {
    const body = req.body as { subdir?: string; filename: string; frontmatter?: Record<string, unknown>; body: string };
    const path = ctx.kb.writeDoc(body.subdir ?? "notes", body.filename, body.frontmatter ?? {}, body.body);
    await ctx.kb.indexFile(path);
    return { path };
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
      const data = await res.json();
      return data;
    } catch (e) {
      return reply.code(503).send({ error: String(e) });
    }
  });
}
