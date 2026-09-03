import { randomUUID } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import {
  renderHandoffMarkdown,
  renderPickupPrompt,
  type HandoffNote,
  type TaskStatus,
  TASK_STATUSES,
} from "@swarm/core";
import type { SwarmContext } from "./context.js";

export function createMcpServer(ctx: SwarmContext): McpServer {
  const server = new McpServer({ name: "swarm", version: "0.1.0" });

  server.tool("swarm_board", "Read the Kanban board", {
    status: z.enum(TASK_STATUSES as unknown as [TaskStatus, ...TaskStatus[]]).optional(),
    repo: z.string().optional(),
    agent: z.string().optional(),
  }, async (args) => {
    const tasks = ctx.tasks.list({ status: args.status, repo: args.repo, agent: args.agent as never });
    return { content: [{ type: "text", text: JSON.stringify(tasks, null, 2) }] };
  });

  server.tool("swarm_task_get", "Get a task by key", { key: z.string() }, async ({ key }) => {
    const task = ctx.tasks.getByKey(key);
    if (!task) return { content: [{ type: "text", text: "Not found" }], isError: true };
    return {
      content: [{
        type: "text",
        text: JSON.stringify({ ...task, events: ctx.tasks.getEvents(task.id), subtasks: ctx.tasks.getSubtasks(task.id) }, null, 2),
      }],
    };
  });

  server.tool("swarm_task_create", "Create a task", {
    title: z.string(),
    initialContext: z.string().optional(),
    agent: z.string().optional(),
    sessionId: z.string().optional(),
    model: z.string().optional(),
    cwd: z.string().optional(),
    pid: z.number().optional(),
    tags: z.array(z.string()).optional(),
  }, async (args) => {
    const task = ctx.tasks.create({
      title: args.title,
      initialContext: args.initialContext,
      originAgent: (args.agent as never) ?? "unknown",
      originSessionId: args.sessionId,
      originModel: args.model,
      originCwd: args.cwd,
      originPid: args.pid,
      repoPath: args.cwd,
      tags: args.tags,
    });
    ctx.broadcast({ type: "task_updated", task });
    return { content: [{ type: "text", text: JSON.stringify(task, null, 2) }] };
  });

  server.tool("swarm_task_stage", "Move, claim, release, block, complete, fail, heartbeat, or archive a task", {
    key: z.string(),
    action: z.enum(["move", "claim", "release", "block", "complete", "fail", "heartbeat", "archive"]),
    status: z.enum(TASK_STATUSES as unknown as [TaskStatus, ...TaskStatus[]]).optional(),
    agent: z.string().optional(),
    sessionId: z.string().optional(),
    by: z.string().optional(),
    model: z.string().optional(),
    cwd: z.string().optional(),
    pid: z.number().optional(),
    tags: z.array(z.string()).optional(),
    addTags: z.array(z.string()).optional(),
    removeTags: z.array(z.string()).optional(),
  }, async (args) => {
    const result = ctx.tasks.stage(args.key, args.action, args as Record<string, unknown>, ctx.config.claimLeaseSeconds);
    if (!result.ok || !result.task) return { content: [{ type: "text", text: result.error ?? "Failed" }], isError: true };
    let task = result.task;
    if (args.tags !== undefined || args.addTags !== undefined || args.removeTags !== undefined) {
      task = ctx.tasks.update(task.id, { tags: args.tags, addTags: args.addTags, removeTags: args.removeTags });
    }
    ctx.broadcast({ type: "task_updated", task });
    return { content: [{ type: "text", text: JSON.stringify(task, null, 2) }] };
  });

  server.tool("swarm_task_join", "Join a task without stealing an active claim. Claims it if unclaimed, otherwise appends a join event.", {
    key: z.string(),
    agent: z.string().optional(),
    sessionId: z.string().optional(),
    by: z.string().optional(),
    cwd: z.string().optional(),
    model: z.string().optional(),
    pid: z.number().optional(),
    transcriptPath: z.string().optional(),
  }, async (args) => {
    const result = ctx.tasks.join(
      args.key,
      {
        agent: (args.agent as never) ?? "unknown",
        sessionId: args.sessionId ?? "mcp",
        by: args.by ?? args.agent ?? "agent",
        cwd: args.cwd,
        model: args.model,
        pid: args.pid,
        transcriptPath: args.transcriptPath,
      },
      ctx.config.claimLeaseSeconds,
    );
    if (!result.ok || !result.task) return { content: [{ type: "text", text: result.error ?? "Join failed" }], isError: true };
    ctx.broadcast({ type: "task_updated", task: result.task });
    return { content: [{ type: "text", text: JSON.stringify(result.task, null, 2) }] };
  });

  server.tool("swarm_task_update", "Update a task's title, summary/context, or tags", {
    key: z.string(),
    title: z.string().optional(),
    summary: z.string().optional(),
    initialContext: z.string().optional(),
    tags: z.array(z.string()).optional(),
    addTags: z.array(z.string()).optional(),
    removeTags: z.array(z.string()).optional(),
  }, async (args) => {
    const existing = ctx.tasks.getByKey(args.key);
    if (!existing) return { content: [{ type: "text", text: "Not found" }], isError: true };
    const task = ctx.tasks.update(existing.id, {
      title: args.title,
      initialContext: args.summary ?? args.initialContext,
      tags: args.tags,
      addTags: args.addTags,
      removeTags: args.removeTags,
    } as never);
    ctx.broadcast({ type: "task_updated", task });
    return { content: [{ type: "text", text: JSON.stringify(task, null, 2) }] };
  });

  server.tool("swarm_handoff", "Write a structured handoff note and transition to handoff", {
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
  }, async ({ key, note }) => {
    const md = renderHandoffMarkdown(note as HandoffNote, key);
    const path = ctx.kb.writeDoc("handoffs", `${key}-handoff.md`, { task: key }, md);
    await ctx.kb.indexFile(path);
    const task = ctx.tasks.writeHandoff(key, note as HandoffNote, md);
    ctx.broadcast({ type: "task_updated", task });
    return { content: [{ type: "text", text: `Handoff written for ${key}. Status: ready.\n\n${md.slice(0, 500)}...` }] };
  });

  server.tool("swarm_pickup", "List open handoffs or claim one and get pickup prompt", {
    key: z.string().optional(),
    agent: z.string().optional(),
    sessionId: z.string().optional(),
    by: z.string().optional(),
  }, async (args) => {
    if (!args.key) {
      const handoffs = ctx.tasks.listHandoffs();
      return { content: [{ type: "text", text: JSON.stringify(handoffs.map((t) => ({ key: t.key, title: t.title })), null, 2) }] };
    }
    const result = ctx.tasks.claim(
      args.key,
      { agent: (args.agent as never) ?? "unknown", sessionId: args.sessionId ?? "mcp", by: args.by ?? args.agent ?? "agent" },
      ctx.config.claimLeaseSeconds,
    );
    if (!result.ok || !result.task) {
      return { content: [{ type: "text", text: result.error ?? "Claim failed" }], isError: true };
    }
    ctx.broadcast({ type: "task_updated", task: result.task });
    return { content: [{ type: "text", text: renderPickupPrompt(result.task) }] };
  });

  server.tool("swarm_kb_search", "Hybrid semantic search over the knowledge base", { query: z.string(), limit: z.number().optional() }, async ({ query, limit }) => {
    const results = await ctx.kb.search(query, limit ?? 10);
    return { content: [{ type: "text", text: JSON.stringify(results, null, 2) }] };
  });

  server.tool("swarm_kb_get", "Get a KB document by slug", { slug: z.string() }, async ({ slug }) => {
    const doc = ctx.kb.getDoc(slug);
    if (!doc) return { content: [{ type: "text", text: "Not found" }], isError: true };
    return { content: [{ type: "text", text: JSON.stringify(doc, null, 2) }] };
  });

  server.tool("swarm_kb_write", "Write a KB document", {
    subdir: z.string().optional(),
    filename: z.string(),
    title: z.string().optional(),
    body: z.string(),
  }, async (args) => {
    const path = ctx.kb.writeDoc(args.subdir ?? "notes", args.filename, { title: args.title ?? args.filename }, args.body);
    try {
      await ctx.kb.indexFile(path);
    } catch (err) {
      console.warn(`[swarm] KB index failed for ${path}:`, err);
      return { content: [{ type: "text", text: `Written: ${path} (index deferred: ${err instanceof Error ? err.message : String(err)})` }] };
    }
    return { content: [{ type: "text", text: `Written: ${path}` }] };
  });

  server.resource("swarm-board", "swarm://board", async () => ({
    contents: [{ uri: "swarm://board", mimeType: "application/json", text: JSON.stringify(ctx.tasks.list(), null, 2) }],
  }));

  server.prompt("swarm-handoff", "Template for writing a handoff note", {}, async () => ({
    messages: [{
      role: "user",
      content: {
        type: "text",
        text: "Fill all handoff sections: goal, done, next steps, decisions, gotchas, verification commands, relevant files, KB refs, open questions. Then call swarm_handoff.",
      },
    }],
  }));

  server.prompt("swarm-pickup", "Template for picking up a handoff", { key: z.string().optional() }, async (args) => ({
    messages: [{
      role: "user",
      content: {
        type: "text",
        text: args.key
          ? `Call swarm_pickup with key=${args.key}, then restate the plan before continuing.`
          : "Call swarm_pickup without a key to list open handoffs, claim one, read the pickup prompt, restate the plan, then continue.",
      },
    }],
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

async function createSession(ctx: SwarmContext): Promise<McpSession> {
  const server = createMcpServer(ctx);
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
    const session = await createSession(ctx);
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
