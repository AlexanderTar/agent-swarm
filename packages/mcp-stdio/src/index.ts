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

async function readToken(): Promise<string | undefined> {
  if (process.env.SWARM_TOKEN) return process.env.SWARM_TOKEN;
  try {
    return readFileSync(`${homedir()}/.swarm/run/daemon.token`, "utf8").trim();
  } catch {
    return undefined;
  }
}

async function registerSession(): Promise<void> {
  try {
    await fetch(`${SWARM_URL}/hooks/session/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        sessionId,
        agent: process.env.SWARM_AGENT ?? "codex",
        cwd: process.cwd(),
        pid: process.pid,
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

  const upstream = new Client({ name: "swarm-mcp-proxy", version: "0.1.0" });
  const httpTransport = new StreamableHTTPClientTransport(new URL(`${SWARM_URL}/mcp`), {
    requestInit: { headers },
  });
  await upstream.connect(httpTransport);

  const server = new Server(
    { name: "swarm", version: "0.1.0" },
    { capabilities: { tools: {}, resources: {}, prompts: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async (req) => upstream.listTools(req.params));
  server.setRequestHandler(CallToolRequestSchema, async (req) => upstream.callTool(req.params));
  server.setRequestHandler(ListResourcesRequestSchema, async (req) => upstream.listResources(req.params));
  server.setRequestHandler(ReadResourceRequestSchema, async (req) => upstream.readResource(req.params));
  server.setRequestHandler(ListPromptsRequestSchema, async (req) => upstream.listPrompts(req.params));
  server.setRequestHandler(GetPromptRequestSchema, async (req) => upstream.getPrompt(req.params));

  const stdio = new StdioServerTransport();
  await server.connect(stdio);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
