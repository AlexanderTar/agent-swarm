#!/usr/bin/env node
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  GetPromptRequestSchema,
  ListPromptsRequestSchema,
  ListResourcesRequestSchema,
  ListToolsRequestSchema,
  ReadResourceRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

const SWARM_URL = process.env.SWARM_URL ?? "http://127.0.0.1:7777";
const sessionId = process.env.SWARM_SESSION_ID ?? randomUUID();
const agentKind = process.env.SWARM_AGENT ?? "unknown";
const agentModel = process.env.SWARM_MODEL;

async function readToken(): Promise<string | undefined> {
  if (process.env.SWARM_TOKEN) return process.env.SWARM_TOKEN;
  try {
    return readFileSync(`${homedir()}/.swarm/run/daemon.token`, "utf8").trim();
  } catch {
    return undefined;
  }
}

async function registerSession(): Promise<void> {
  if (process.env.SWARM_REGISTER_SESSION !== "1") return;

  try {
    await fetch(`${SWARM_URL}/hooks/session/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        sessionId,
        agent: process.env.SWARM_AGENT ?? "unknown",
        cwd: process.cwd(),
        pid: process.pid,
        ...(process.env.SWARM_MODEL ? { model: process.env.SWARM_MODEL } : {}),
      }),
    });
  } catch {
    // daemon may not be up yet
  }
}

async function endSession(): Promise<void> {
  try {
    await fetch(`${SWARM_URL}/hooks/session/end`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId }),
    });
  } catch {
    // ignore
  }
}

function isSessionLostError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err);
  return (
    msg.includes("Session not found") ||
    msg.includes("-32001") ||
    msg.includes("HTTP 404") ||
    /session.*(expired|terminated|invalid)/i.test(msg)
  );
}

/**
 * Auto-inject agent identity into task tool calls when the caller omitted it.
 * Backward-compat: only fills missing/empty fields, never overwrites explicit values.
 */
function injectAgentContext(params: { name: string; arguments?: Record<string, unknown> }): {
  name: string;
  arguments?: Record<string, unknown>;
} {
  const tools = new Set(["swarm_task_create", "swarm_task_stage", "swarm_task_join", "swarm_pickup"]);
  if (!tools.has(params.name) || !params.arguments || typeof params.arguments !== "object") return params;
  const args = { ...params.arguments };
  const missing = (v: unknown): boolean => v === undefined || v === null || (typeof v === "string" && v.trim() === "");
  if (params.name === "swarm_task_create" || params.name === "swarm_pickup" || params.name === "swarm_task_join") {
    if (missing(args.agent)) args.agent = agentKind;
    if (missing(args.sessionId)) args.sessionId = sessionId;
  }
  if (params.name === "swarm_task_stage" && (args.action === "claim" || args.action === "heartbeat")) {
    if (missing(args.agent)) args.agent = agentKind;
    if (missing(args.sessionId)) args.sessionId = sessionId;
  }
  if (params.name === "swarm_task_stage" || params.name === "swarm_task_join" || params.name === "swarm_pickup") {
    if (missing(args.cwd)) {
      try {
        args.cwd = process.cwd();
      } catch {
        /* ignore */
      }
    }
    if (missing(args.pid)) args.pid = process.pid;
    if (missing(args.model) && agentModel) args.model = agentModel;
  }
  return { ...params, arguments: args };
}

async function main(): Promise<void> {
  try {
    const health = await fetch(`${SWARM_URL}/api/health`, { signal: AbortSignal.timeout(3000) });
    if (!health.ok) throw new Error(`health ${health.status}`);
  } catch {
    const uid = process.getuid?.() ?? "";
    console.error(
      `swarm daemon not running at ${SWARM_URL}\nTry: launchctl kickstart -k gui/${uid}/dev.swarm.daemon`,
    );
    process.exit(1);
  }

  await registerSession();
  process.on("exit", () => void endSession());
  process.on("SIGINT", () => {
    void endSession().finally(() => process.exit(0));
  });
  process.on("SIGTERM", () => {
    void endSession().finally(() => process.exit(0));
  });

  const token = await readToken();
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;

  let upstream = new Client({ name: "swarm-mcp-proxy", version: "0.1.0" });
  let httpTransport = new StreamableHTTPClientTransport(new URL(`${SWARM_URL}/mcp`), {
    requestInit: { headers },
  });
  await upstream.connect(httpTransport);

  async function reconnectUpstream(): Promise<void> {
    try {
      await upstream.close();
    } catch {
      /* ignore */
    }
    try {
      await httpTransport.close();
    } catch {
      /* ignore */
    }
    upstream = new Client({ name: "swarm-mcp-proxy", version: "0.1.0" });
    httpTransport = new StreamableHTTPClientTransport(new URL(`${SWARM_URL}/mcp`), {
      requestInit: { headers },
    });
    await upstream.connect(httpTransport);
  }

  async function withUpstreamRetry<T>(fn: (client: Client) => Promise<T>): Promise<T> {
    try {
      return await fn(upstream);
    } catch (err) {
      if (!isSessionLostError(err)) throw err;
      await reconnectUpstream();
      return await fn(upstream);
    }
  }

  const server = new Server(
    { name: "swarm", version: "0.1.0" },
    { capabilities: { tools: {}, resources: {}, prompts: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async (req) =>
    withUpstreamRetry((c) => c.listTools(req.params)),
  );
  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    const params = injectAgentContext(req.params) as typeof req.params;
    return withUpstreamRetry((c) => c.callTool(params));
  });
  server.setRequestHandler(ListResourcesRequestSchema, async (req) =>
    withUpstreamRetry((c) => c.listResources(req.params)),
  );
  server.setRequestHandler(ReadResourceRequestSchema, async (req) =>
    withUpstreamRetry((c) => c.readResource(req.params)),
  );
  server.setRequestHandler(ListPromptsRequestSchema, async (req) =>
    withUpstreamRetry((c) => c.listPrompts(req.params)),
  );
  server.setRequestHandler(GetPromptRequestSchema, async (req) =>
    withUpstreamRetry((c) => c.getPrompt(req.params)),
  );

  const stdio = new StdioServerTransport();
  await server.connect(stdio);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
