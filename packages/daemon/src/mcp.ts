import { randomUUID } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import {
  renderHandoffMarkdown,
  renderPickupPrompt,
  type AgentKind,
  type HandoffNote,
  type SessionRecord,
  type TaskStatus,
  TASK_STATUSES,
} from "@swarm/core";
import type { SwarmContext } from "./context.js";

/** Who is on the other end of this MCP connection, from the proxy's x-swarm-* headers. */
export interface McpClientMeta {
  cwd?: string;
  agent?: AgentKind;
  sessionId?: string;
}

const statusEnum = z.enum(TASK_STATUSES as unknown as [TaskStatus, ...TaskStatus[]]);

function text(body: string, isError = false) {
  return { content: [{ type: "text" as const, text: body }], ...(isError ? { isError: true } : {}) };
}

function json(value: unknown) {
  return text(JSON.stringify(value, null, 2));
}

function slugify(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
      .slice(0, 60) || "note"
  );
}

export function createMcpServer(ctx: SwarmContext, meta: McpClientMeta = {}): McpServer {
  const server = new McpServer({ name: "swarm", version: "0.1.0" });

  /**
   * Explicit sessionId wins. Otherwise fall back to the newest live session in the
   * caller's working directory.
   * ponytail: cwd match is ambiguous when two agents share a directory — pass sessionId
   * (the SessionStart briefing hands it to the agent) if that ever matters.
   */
  const resolveSession = (sessionId?: string): SessionRecord | null =>
    ctx.sessions.resolve({ sessionId: sessionId ?? meta.sessionId, cwd: meta.cwd });

  const callerAgent = (agent?: string, session?: SessionRecord | null): AgentKind =>
    (agent as AgentKind) ?? session?.agentKind ?? meta.agent ?? "unknown";

  server.tool(
    "swarm_board",
    "Read the Kanban board. Filter by status, repo, agent or tag.",
    {
      status: statusEnum.optional(),
      repo: z.string().optional(),
      agent: z.string().optional(),
      tag: z.string().optional(),
    },
    async (args) =>
      json(
        ctx.tasks.list({
          status: args.status,
          repo: args.repo,
          agent: args.agent as AgentKind,
          tag: args.tag,
        }),
      ),
  );

  server.tool("swarm_task_get", "Get a board item by key, with its tags, sessions, events and subtasks", { key: z.string() }, async ({ key }) => {
    const task = ctx.tasks.getByKey(key);
    if (!task) return text(`Task not found: ${key}`, true);
    return json({
      ...task,
      tags: ctx.tasks.getTags(task.id),
      sessions: ctx.sessions.labelsForTasks([task.id]).get(task.id) ?? [],
      events: ctx.tasks.getEvents(task.id),
      subtasks: ctx.tasks.getSubtasks(task.id),
    });
  });

  server.tool(
    "swarm_task_create",
    "Create a board item for work worth coordinating. YOU write the title and summary — do not use a generated or templated name. Skip trivial or read-only turns.",
    {
      title: z.string().min(1).describe("Specific, human-readable, <=60 chars. No agent or platform names."),
      summary: z
        .string()
        .optional()
        .describe("2-5 sentences in your own words: goal, current state, next step."),
      tags: z.array(z.string()).optional().describe("Lowercase labels for filtering, e.g. [\"repo-name\", \"backend\"]."),
      status: statusEnum.optional(),
      repoPath: z.string().optional(),
      branch: z.string().optional(),
      sessionId: z.string().optional().describe("Your swarm session id, from the session-start briefing."),
      agent: z.string().optional(),
      model: z.string().optional(),
    },
    async (args) => {
      const session = resolveSession(args.sessionId);
      const task = ctx.tasks.create({
        title: args.title,
        summary: args.summary,
        status: args.status ?? "in_progress",
        tags: args.tags,
        originAgent: callerAgent(args.agent, session),
        originSessionId: session?.id ?? args.sessionId,
        originModel: args.model ?? session?.model ?? undefined,
        originCwd: session?.cwd ?? meta.cwd,
        repoPath: args.repoPath ?? session?.cwd ?? meta.cwd,
        branch: args.branch,
      });
      if (session) ctx.sessions.bind(session.id, task.id);
      void ctx.indexer.indexTask(task.id).catch(() => {});
      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
      return json({ ...task, session: session?.id ?? "none", board: ctx.boardUrl });
    },
  );

  server.tool(
    "swarm_task_update",
    "Update a board item's title, summary, status or tags. Call this whenever the goal, state or next step changes so other agents see the current picture.",
    {
      key: z.string(),
      title: z.string().optional(),
      summary: z.string().optional().describe("Written from your own conversation, not a template."),
      status: statusEnum.optional(),
      tags: z.array(z.string()).optional().describe("Replaces the tag set."),
      addTags: z.array(z.string()).optional(),
      removeTags: z.array(z.string()).optional(),
    },
    async (args) => {
      const task = ctx.tasks.getByKey(args.key);
      if (!task) return text(`Task not found: ${args.key}`, true);
      let updated = ctx.tasks.update(task.id, {
        title: args.title,
        summary: args.summary,
        status: args.status,
        tags: args.tags,
      });
      if (args.addTags?.length) updated = ctx.tasks.addTags(task.id, args.addTags);
      if (args.removeTags?.length) updated = ctx.tasks.removeTags(task.id, args.removeTags);
      void ctx.indexer.indexTask(task.id).catch(() => {});
      ctx.broadcast({ type: "task_updated", task: updated });
      return json({ ...updated, tags: ctx.tasks.getTags(task.id) });
    },
  );

  server.tool(
    "swarm_task_join",
    "Attach this session to an existing board item so several agents can work on it at once. Additive — it does not take the item away from anyone.",
    {
      key: z.string(),
      sessionId: z.string().optional(),
      agent: z.string().optional(),
      model: z.string().optional(),
    },
    async (args) => {
      const task = ctx.tasks.getByKey(args.key);
      if (!task) return text(`Task not found: ${args.key}`, true);
      const session = resolveSession(args.sessionId);
      if (!session) {
        return text("No swarm session for this connection — pass sessionId explicitly.", true);
      }
      if (args.model) ctx.sessions.upsert({ id: session.id, agent: callerAgent(args.agent, session), model: args.model });
      ctx.sessions.bind(session.id, task.id);
      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(task.id) });
      return json({
        joined: task.key,
        title: task.title,
        session: session.id,
        sessions: ctx.sessions.labelsForTasks([task.id]).get(task.id) ?? [],
      });
    },
  );

  server.tool(
    "swarm_task_stage",
    "Move, claim, release, block, complete, fail, heartbeat, or archive a board item",
    {
      key: z.string(),
      action: z.enum(["move", "claim", "release", "block", "complete", "fail", "heartbeat", "archive"]),
      status: statusEnum.optional(),
      agent: z.string().optional(),
      sessionId: z.string().optional(),
      by: z.string().optional(),
    },
    async (args) => {
      if (!ctx.tasks.getByKey(args.key)) return text(`Task not found: ${args.key}`, true);
      const session = resolveSession(args.sessionId);
      const payload = { ...args, sessionId: args.sessionId ?? session?.id ?? "mcp", agent: callerAgent(args.agent, session) };
      const result = ctx.tasks.stage(args.key, args.action, payload, ctx.config.claimLeaseSeconds);
      if (!result.ok) return text(result.error ?? "Failed", true);
      ctx.broadcast({ type: "task_updated", task: result.task });
      return json(result.task);
    },
  );

  server.tool(
    "swarm_handoff",
    "Write a structured handoff note so another agent can pick the work up, and move the item to ready",
    {
      key: z.string(),
      note: z.object({
        goal: z.string(),
        done: z.string(),
        nextSteps: z.array(z.string()),
        decisions: z.array(z.string()),
        gotchas: z.array(z.string()),
        verification: z.array(z.string()),
        files: z.array(z.object({ path: z.string(), reason: z.string() })),
        kbRefs: z.array(z.string()),
        openQuestions: z.array(z.string()),
      }),
    },
    async ({ key, note }) => {
      if (!ctx.tasks.getByKey(key)) return text(`Task not found: ${key}`, true);
      const md = renderHandoffMarkdown(note as HandoffNote, key);
      const path = ctx.kb.writeDoc("handoffs", `${key}-handoff.md`, { task: key, type: "handoff" }, md);
      await ctx.kb.indexFile(path).catch((err) => console.warn(`[swarm] KB index failed for ${path}:`, err));
      const task = ctx.tasks.writeHandoff(key, note as HandoffNote, md);
      ctx.broadcast({ type: "task_updated", task });
      return text(`Handoff written for ${key}. Status: ready.\n\n${md.slice(0, 500)}...`);
    },
  );

  server.tool(
    "swarm_pickup",
    "List open handoffs, or claim one exclusively and get its pickup prompt",
    {
      key: z.string().optional(),
      agent: z.string().optional(),
      sessionId: z.string().optional(),
      by: z.string().optional(),
    },
    async (args) => {
      if (!args.key) {
        return json(ctx.tasks.listHandoffs().map((t) => ({ key: t.key, title: t.title })));
      }
      const session = resolveSession(args.sessionId);
      const result = ctx.tasks.claim(
        args.key,
        {
          agent: callerAgent(args.agent, session),
          sessionId: args.sessionId ?? session?.id ?? "mcp",
          by: args.by ?? args.agent ?? "agent",
        },
        ctx.config.claimLeaseSeconds,
      );
      if (!result.ok || !result.task) return text(result.error ?? "Claim failed", true);
      if (session) ctx.sessions.bind(session.id, result.task.id);
      ctx.broadcast({ type: "task_updated", task: ctx.tasks.getById(result.task.id) });
      return text(renderPickupPrompt(result.task));
    },
  );

  server.tool(
    "swarm_kb_search",
    "Hybrid semantic + keyword search over the shared knowledge base (tasks, handoffs, notes, transcripts, memory)",
    { query: z.string(), limit: z.number().optional(), subdir: z.string().optional() },
    async ({ query, limit, subdir }) => json(await ctx.kb.search(query, limit ?? 10, { subdir })),
  );

  server.tool("swarm_kb_get", "Get a knowledge base document by slug", { slug: z.string() }, async ({ slug }) => {
    const doc = ctx.kb.getDoc(slug);
    return doc ? json(doc) : text(`Not found: ${slug}`, true);
  });

  server.tool(
    "swarm_kb_write",
    "Write a markdown document into the shared knowledge base and index it for search",
    {
      subdir: z.string().optional(),
      filename: z.string(),
      title: z.string().optional(),
      tags: z.array(z.string()).optional(),
      body: z.string(),
    },
    async (args) => {
      const path = ctx.kb.writeDoc(
        args.subdir ?? "notes",
        args.filename,
        { title: args.title ?? args.filename, tags: args.tags ?? [] },
        args.body,
      );
      try {
        await ctx.kb.indexFile(path);
      } catch (err) {
        return text(`Written: ${path} (index deferred: ${err instanceof Error ? err.message : String(err)})`);
      }
      return text(`Written: ${path}`);
    },
  );

  server.tool(
    "swarm_memory_write",
    "Save a durable memory to the shared knowledge base. Use for facts, decisions and gotchas that outlive this session.",
    {
      title: z.string(),
      body: z.string(),
      tags: z.array(z.string()).optional(),
    },
    async (args) => {
      const path = ctx.kb.writeDoc(
        "memory",
        `${slugify(args.title)}.md`,
        { title: args.title, tags: args.tags ?? [], type: "memory" },
        args.body,
      );
      try {
        await ctx.kb.indexFile(path);
      } catch (err) {
        return text(`Saved: ${path} (index deferred: ${err instanceof Error ? err.message : String(err)})`);
      }
      return text(`Saved: ${path}`);
    },
  );

  server.tool(
    "swarm_memory_search",
    "Search durable memories saved by any agent.",
    { query: z.string(), limit: z.number().optional() },
    async ({ query, limit }) => json(await ctx.kb.search(query, limit ?? 10, { subdir: "memory" })),
  );

  server.resource("swarm-board", "swarm://board", async () => ({
    contents: [
      { uri: "swarm://board", mimeType: "application/json", text: JSON.stringify(ctx.tasks.list(), null, 2) },
    ],
  }));

  server.prompt("swarm-handoff", "Template for writing a handoff note", {}, async () => ({
    messages: [
      {
        role: "user",
        content: {
          type: "text",
          text: "Fill all handoff sections from your own conversation: goal, done, next steps, decisions, gotchas, verification commands, relevant files, KB refs, open questions. Then call swarm_handoff.",
        },
      },
    ],
  }));

  server.prompt("swarm-pickup", "Template for picking up a handoff", { key: z.string().optional() }, async (args) => ({
    messages: [
      {
        role: "user",
        content: {
          type: "text",
          text: args.key
            ? `Call swarm_pickup with key=${args.key}, then restate the plan before continuing.`
            : "Call swarm_pickup without a key to list open handoffs, claim one, read the pickup prompt, restate the plan, then continue.",
        },
      },
    ],
  }));

  return server;
}

type McpSession = {
  transport: StreamableHTTPServerTransport;
  server: McpServer;
};

const sessions = new Map<string, McpSession>();

function sendJsonRpcError(res: ServerResponse, status: number, code: number, message: string): void {
  if (res.headersSent) return;
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify({ jsonrpc: "2.0", error: { code, message }, id: null }));
}

function readHeader(req: IncomingMessage, name: string): string | undefined {
  const value = req.headers[name];
  const raw = Array.isArray(value) ? value[0] : value;
  return raw?.trim() ? raw.trim() : undefined;
}

function clientMeta(req: IncomingMessage): McpClientMeta {
  return {
    cwd: readHeader(req, "x-swarm-cwd"),
    agent: readHeader(req, "x-swarm-agent") as AgentKind | undefined,
    sessionId: readHeader(req, "x-swarm-session-id"),
  };
}

async function createSession(ctx: SwarmContext, meta: McpClientMeta): Promise<McpSession> {
  const server = createMcpServer(ctx, meta);
  const transport = new StreamableHTTPServerTransport({
    sessionIdGenerator: () => randomUUID(),
    enableJsonResponse: true,
    onsessioninitialized: (id) => {
      sessions.set(id, { transport, server });
    },
  });
  transport.onclose = () => {
    const sid = transport.sessionId;
    if (sid) sessions.delete(sid);
    void server.close();
  };
  await server.connect(transport);
  return { transport, server };
}

export async function handleMcpNodeRequest(
  ctx: SwarmContext,
  req: IncomingMessage,
  res: ServerResponse,
  parsedBody?: unknown,
): Promise<void> {
  const sessionId = req.headers["mcp-session-id"] as string | undefined;
  const existing = sessionId ? sessions.get(sessionId) : undefined;

  if (existing) {
    await existing.transport.handleRequest(req, res, parsedBody);
    return;
  }

  // New initialize — also covers clients that retry initialize still carrying a
  // stale mcp-session-id after a daemon restart (map was wiped).
  if (req.method === "POST" && isInitializeRequest(parsedBody)) {
    const session = await createSession(ctx, clientMeta(req));
    await session.transport.handleRequest(req, res, parsedBody);
    return;
  }

  if (sessionId) {
    // Spec: 404 → client MUST re-initialize without the old session id.
    sendJsonRpcError(res, 404, -32001, "Session not found");
    return;
  }

  sendJsonRpcError(res, 400, -32000, "Bad Request: Server not initialized");
}
