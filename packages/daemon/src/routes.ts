import type { FastifyInstance } from "fastify";
import {
  agentKindFromPlatform,
  buildSessionContext,
  enrichHookInput,
  inferStatusFromStop,
  isSubagentHook,
  mapAntigravityTool,
  normalizeHookInput,
  resolveBoardSessionId,
  resolveHookBoardSessionId,
  resolveHookRootSessionId,
  resolveSessionTaskTitle,
  resolveSubagentBoardId,
  shortReference,
  summarizeTaskRecord,
  syncTaskSessionMetadata,
  type AgentKind,
  type NormalizedHookInput,
  type TaskRecord,
} from "@swarm/core";
import type { SwarmContext } from "./context.js";
import { scheduleTurnSummary } from "./sessionSummary.js";
import { scheduleTitleRefresh } from "./titleJob.js";

function readString(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

function sessionTitleFields(input: NormalizedHookInput) {
  const { title, fromSession } = resolveSessionTaskTitle(input);
  return { title, titleFromSession: fromSession };
}

function syncSessionFromHook(ctx: SwarmContext, task: TaskRecord, input: NormalizedHookInput): TaskRecord {
  return syncTaskSessionMetadata(ctx.tasks, task, input).task;
}

function markTaskActive(ctx: SwarmContext, task: TaskRecord): void {
  if (task.status === "in_progress") {
    ctx.tasks.touch(task.id);
    return;
  }
  // Session id owns one tile — new activity revives done/archived/ready/review.
  if (task.status === "blocked") return;
  ctx.tasks.update(task.id, { status: "in_progress" });
}

function ensureBoardTask(
  ctx: SwarmContext,
  task: TaskRecord | null,
  input: NormalizedHookInput,
  boardSessionId: string,
  agent: AgentKind,
): TaskRecord | null {
  if (isSubagentHook(input)) return task;
  if (!boardSessionId) return task;
  const { title, titleFromSession } = sessionTitleFields(input);
  // Always upsert by session id so done/archived tiles are revived instead of duplicated.
  const upserted = ctx.tasks.upsertSessionTask({
    sessionId: boardSessionId,
    agent,
    cwd: input.cwd,
    model: input.model,
    pid: input.pid,
    transcriptPath: input.transcriptPath,
    title,
    titleFromSession,
    initialContext: buildSessionContext(input),
  });
  return upserted;
}

export async function registerHookRoutes(app: FastifyInstance, ctx: SwarmContext): Promise<void> {
  app.post<{ Params: { platform: string; event: string }; Body: Record<string, unknown> }>(
    "/hooks/:platform/:event",
    async (req, reply) => {
      const platform = req.params.platform as "claude" | "cursor" | "codex" | "antigravity" | "opencode";
      const event = req.params.event;
      const input = normalizeHookInput({ ...req.body, hook_event_name: event }, platform);
      input.hookEvent = event;
      enrichHookInput(input);

      const agent = agentKindFromPlatform(platform);
      const isSubagent = isSubagentHook(input, event);
      const rootSessionId = resolveHookRootSessionId(input);
      let task = rootSessionId ? ctx.tasks.getBySession(rootSessionId) : null;

      switch (event) {
        case "SessionStart":
        case "sessionStart": {
          if (isSubagent) {
            if (task) {
              const subagentId = resolveSubagentBoardId(input) || input.agentId;
              if (subagentId) {
                ctx.tasks.attachSession(task.id, {
                  sessionId: subagentId,
                  agent,
                  cwd: input.cwd,
                  model: input.model,
                  pid: input.pid,
                  transcriptPath: input.transcriptPath,
                });
              }
            }
            return reply.send({ ok: true, taskKey: task?.key });
          }
          const { title, titleFromSession } = sessionTitleFields(input);
          task = ctx.tasks.upsertSessionTask({
            sessionId: rootSessionId,
            agent,
            cwd: input.cwd,
            model: input.model,
            pid: input.pid,
            transcriptPath: input.transcriptPath,
            title,
            titleFromSession,
            initialContext: buildSessionContext(input),
          });
          ctx.broadcast({ type: "task_updated", task });
          scheduleTitleRefresh(ctx, task);
          return reply.send({ ok: true, taskKey: task.key });
        }
        case "UserPromptSubmit":
        case "beforeSubmitPrompt": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent);
          }
          if (task) {
            markTaskActive(ctx, task);
            if (input.prompt) {
              ctx.tasks.incrementTurn(task.id);
              ctx.tasks.appendEvent(task.id, "prompt", { prompt: input.prompt.slice(0, 500) });
            }
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "PreToolUse":
        case "preToolUse":
        case "PostToolUse":
        case "postToolUse":
        case "afterFileEdit": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent);
          }
          if (task) {
            markTaskActive(ctx, task);
            const tool = platform === "antigravity" ? mapAntigravityTool(input.toolName) : input.toolName;
            if (tool === "Write" || tool === "Edit" || tool === "WriteToFile") {
              const fp = (input.toolInput?.file_path ?? input.toolInput?.TargetFile ?? input.toolInput?.path) as string | undefined;
              if (fp) ctx.tasks.addArtifact(task.id, "files", fp);
            }
            const payload: Record<string, unknown> = { tool, input: input.toolInput };
            const subagentId = resolveSubagentBoardId(input);
            if (subagentId) payload.subagent_id = subagentId;
            ctx.tasks.appendEvent(task.id, event, payload);
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "MessageDisplay": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent);
          }
          if (!task) return reply.send({ ok: true });
          markTaskActive(ctx, task);
          if (input.messageDelta?.trim()) {
            ctx.tasks.appendEvent(task.id, "agent_response_delta", {
              delta: input.messageDelta.slice(0, 2000),
              final: input.messageFinal ?? false,
              message_id: input.raw.message_id,
              turn_id: input.raw.turn_id,
            });
          }
          if (input.messageFinal) {
            ctx.tasks.incrementTurn(task.id);
          }
          ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
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
            task = syncSessionFromHook(ctx, task, input);
            const status = inferStatusFromStop(input, event) ?? "review";
            ctx.tasks.update(task.id, { status });
            if (input.lastAssistantMessage) {
              ctx.tasks.appendEvent(task.id, "stop_summary", { summary: input.lastAssistantMessage.slice(0, 1000) });
            }
            scheduleTurnSummary(ctx, task, input, event);
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "afterAgentResponse":
        case "afterAgentThought": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent);
          }
          if (!task) return reply.send({ ok: true });
          markTaskActive(ctx, task);
          const text = input.agentText?.trim();
          if (text) {
            const payload: Record<string, unknown> = { text: text.slice(0, 2000) };
            if (event === "afterAgentThought" && input.thoughtDurationMs != null) {
              payload.duration_ms = input.thoughtDurationMs;
            }
            ctx.tasks.appendEvent(
              task.id,
              event === "afterAgentThought" ? "agent_thought" : "agent_response",
              payload,
            );
          }
          if (event === "afterAgentResponse") {
            ctx.tasks.incrementTurn(task.id);
            scheduleTurnSummary(ctx, task, input, event);
          }
          ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
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
            scheduleTurnSummary(ctx, task, input, event);
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
          const subagentId = resolveSubagentBoardId(input) || input.agentId;
          if (task) {
            markTaskActive(ctx, task);
            const subject = input.task ?? input.agentType ?? "Subagent";
            ctx.tasks.addSubtask(task.id, subject, input.task);
            ctx.tasks.appendEvent(task.id, "subagent_start", {
              agent_type: input.agentType,
              agent_id: subagentId,
              task: input.task,
            });
            if (subagentId) {
              ctx.tasks.attachSession(task.id, {
                sessionId: subagentId,
                agent,
                cwd: input.cwd,
                model: input.model,
                pid: input.pid,
                transcriptPath: input.transcriptPath,
              });
            }
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
            return reply.send({ ok: true, taskKey: task.key });
          }
          return reply.send({ ok: true });
        }
        case "SubagentStop":
        case "subagentStop": {
          if (task) {
            const subject = input.task ?? input.agentType ?? "Subagent";
            ctx.tasks.completeSubtask(task.id, subject);
            const summary = input.subagentSummary ?? input.lastAssistantMessage;
            if (summary?.trim()) {
              ctx.tasks.appendEvent(task.id, "subagent_stop", {
                status: input.subagentStatus,
                summary: summary.slice(0, 2000),
                agent_id: resolveSubagentBoardId(input) || input.agentId,
              });
            }
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "TaskCreated": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent) ?? task;
          }
          if (task) {
            const subject = input.taskSubject ?? (input.raw.task_subject as string) ?? "Subtask";
            const description = input.taskDescription ?? (input.raw.task_description as string | undefined);
            ctx.tasks.addSubtask(task.id, subject, description);
            ctx.tasks.appendEvent(task.id, "task_created", { subject, description });
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "TaskCompleted": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent) ?? task;
          }
          if (task) {
            const subject = input.taskSubject ?? (input.raw.task_subject as string) ?? "";
            if (subject) ctx.tasks.completeSubtask(task.id, subject);
            ctx.tasks.appendEvent(task.id, "task_completed", { subject, description: input.taskDescription });
            scheduleTurnSummary(ctx, task, input, event);
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "PreInvocation": {
          if (isSubagent) {
            if (task) {
              const subagentId = resolveSubagentBoardId(input) || input.sessionId;
              if (subagentId && subagentId !== task.originSessionId) {
                ctx.tasks.attachSession(task.id, {
                  sessionId: subagentId,
                  agent,
                  cwd: input.cwd,
                  model: input.model,
                  pid: input.pid,
                  transcriptPath: input.transcriptPath,
                });
              }
              ctx.tasks.touch(task.id);
            }
            return reply.send({ decision: "allow" });
          }
          const sid = input.sessionId || readString(input.raw.conversationId as unknown) || "";
          if (sid) {
            task = ctx.tasks.getBySession(sid) ?? task;
            if (!task) {
              task = ctx.tasks.upsertSessionTask({
                sessionId: sid,
                agent,
                cwd: input.cwd,
                model: input.model,
                pid: input.pid,
                transcriptPath: input.transcriptPath,
                ...sessionTitleFields(input),
                initialContext: buildSessionContext(input),
              });
              ctx.broadcast({ type: "task_updated", task });
            } else {
              ctx.tasks.touch(task.id);
            }
          }
          return reply.send({ decision: "allow" });
        }
        case "PostInvocation": {
          if (task) {
            task = syncSessionFromHook(ctx, task, input);
            ctx.tasks.touch(task.id);
            scheduleTurnSummary(ctx, task, input, event);
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "PermissionRequest": {
          if (!isSubagent) {
            task = ensureBoardTask(ctx, task, input, rootSessionId, agent);
          }
          if (task) {
            ctx.tasks.appendEvent(task.id, "permission_request", {
              tool: input.toolName,
              input: input.toolInput,
            });
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        case "PostCompact": {
          if (task) {
            ctx.tasks.appendEvent(task.id, "post_compact", { trigger: input.raw.trigger ?? "compact" });
            ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
          }
          return reply.send({ ok: true });
        }
        default:
          return reply.send({ ok: true });
      }
    },
  );

  app.post("/hooks/session/register", async (req, reply) => {
    const body = req.body as { sessionId: string; agent: AgentKind; cwd?: string; pid?: number; model?: string; transcriptPath?: string; title?: string };
    const agent = body.agent ?? "unknown";
    const cwd = body.cwd ?? process.cwd();
    const repo = cwd.replace(/\/+$/, "").split("/").filter(Boolean).at(-1) ?? cwd;
    const task = ctx.tasks.upsertSessionTask({
      sessionId: body.sessionId,
      agent,
      cwd,
      pid: body.pid,
      model: body.model,
      transcriptPath: body.transcriptPath,
      title: body.title?.trim() || `${agent} · ${repo}`,
      titleFromSession: false,
    });
    ctx.broadcast({ type: "task_updated", task });
    return reply.send({ ok: true, taskKey: task.key, reference: shortReference(`Session registered as ${task.key}`, `http://${ctx.config.host}:${ctx.config.port}/`) });
  });

  app.post("/hooks/session/end", async (req, reply) => {
    const body = req.body as { sessionId: string };
    const task = ctx.tasks.getBySession(body.sessionId);
    if (task) {
      ctx.tasks.update(task.id, { status: "ready" });
      if (task.handoffNote?.trim()) {
        void ctx.memory.syncAfterSummary(task.id).catch((err) => {
          console.warn(`[swarm] memory sync failed for ${task.key}:`, err);
        });
      }
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
    const tasks = ctx.tasks.list({
      status: q.status as never,
      repo: q.repo,
      agent: q.agent as AgentKind,
      stale: q.stale === "true",
    });
    return tasks.map((t) => {
      try {
        return { ...t, sessions: ctx.tasks.listSessions(t.id) };
      } catch {
        return t;
      }
    });
  });

  app.post("/api/tasks", async (req, reply) => {
    const body = req.body as {
      title?: string;
      status?: string;
      initialContext?: string;
      agent?: AgentKind;
      sessionId?: string;
      cwd?: string;
      model?: string;
      tags?: string[];
    };
    const task = ctx.tasks.create({
      title: body.title ?? "Demo task",
      status: (body.status as never) ?? "ready",
      initialContext: body.initialContext,
      originAgent: body.agent ?? "unknown",
      originSessionId: body.sessionId,
      originCwd: body.cwd ?? process.cwd(),
      originModel: body.model,
      repoPath: body.cwd,
      tags: body.tags,
    });
    ctx.broadcast({ type: "task_updated", task });
    return reply.code(201).send(task);
  });

  app.post("/api/tasks/cleanup", async () => {
    const result = ctx.tasks.cleanupSubagentTasks();
    ctx.broadcast({ type: "board_refreshed" });
    return { ok: true, ...result };
  });

  app.get("/api/tasks/:key", async (req, reply) => {
    const { key } = req.params as { key: string };
    const task = ctx.tasks.getByKey(key);
    if (!task) return reply.code(404).send({ error: "Not found" });
    return {
      ...task,
      sessions: ctx.tasks.listSessions(task.id),
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
      tags: body.tags as string[] | undefined,
      addTags: body.addTags as string[] | undefined,
      removeTags: body.removeTags as string[] | undefined,
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
      const data = await res.json();
      return data;
    } catch (e) {
      return reply.code(503).send({ error: String(e) });
    }
  });

  app.post("/api/summaries/backfill", async (req, reply) => {
    const body = (req.body ?? {}) as { force?: boolean; limit?: number };
    const force = body.force === true;
    const limit = Math.min(Math.max(body.limit ?? 50, 1), 200);
    const pending = ctx.tasks.listNeedingBackfill(force).slice(0, limit);

    void (async () => {
      for (const task of pending) {
        try {
          const { task: updated } = await summarizeTaskRecord(ctx.ollama, ctx.tasks, task, {
            hookEvent: "Stop",
            skipIfPresent: !force,
            memory: ctx.memory,
          });
          ctx.broadcast({ type: "task_updated", task: updated });
        } catch (err) {
          console.warn(`[swarm] backfill failed for ${task.key}:`, err);
        }
      }
    })();

    return reply.send({ started: true, count: pending.length });
  });
}
