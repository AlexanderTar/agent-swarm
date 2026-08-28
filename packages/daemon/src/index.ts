#!/usr/bin/env node
import { writeFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import Fastify from "fastify";
import cors from "@fastify/cors";
import fastifyStatic from "@fastify/static";
import websocket from "@fastify/websocket";
import { createContext } from "./context.js";
import { registerHookRoutes, registerApiRoutes } from "./routes.js";
import { handleMcpNodeRequest } from "./mcp.js";
import { startScheduler } from "./jobs.js";

const home = process.env.SWARM_HOME;
const scratchPort = process.env.SWARM_SCRATCH_PORT;

async function main(): Promise<void> {
  const ctx = createContext(home);

  const preflight = await ctx.ollama.preflight();
  if (!preflight.ok) {
    console.error("Ollama preflight failed:", preflight.errors.join("; "));
    process.exit(1);
  }

  const app = Fastify({ logger: false });
  await app.register(cors, {
    origin: [`http://${ctx.config.host}:${ctx.config.port}`, "http://127.0.0.1:7777", "http://localhost:7777"],
  });
  await app.register(websocket);

  app.addHook("onRequest", async (req, reply) => {
    if (req.url.startsWith("/api/health")) return;
    if (req.url.startsWith("/api/bootstrap")) return;
    if (req.url.startsWith("/hooks/")) return;
    if (req.method === "GET" && !req.url.startsWith("/api/") && !req.url.startsWith("/mcp") && req.url !== "/ws") return;
    const auth = req.headers.authorization;
    if (auth !== `Bearer ${ctx.token}`) {
      return reply.code(401).send({ error: "Unauthorized" });
    }
  });

  await registerHookRoutes(app, ctx);
  await registerApiRoutes(app, ctx);

  app.all("/mcp", async (req, reply) => {
    await handleMcpNodeRequest(ctx, req.raw, reply.raw, req.body);
    reply.hijack();
  });

  app.get("/ws", { websocket: true }, (socket) => {
    ctx.clients.add(socket);
    socket.on("close", () => ctx.clients.delete(socket));
    socket.send(JSON.stringify({ type: "board_snapshot", tasks: ctx.tasks.list() }));
  });

  // Resolve web UI relative to this file (packages/daemon/dist/index.js), not process.cwd().
  // launchd sets cwd to ~/.swarm, which would otherwise miss packages/web/out.
  const daemonDir = dirname(fileURLToPath(import.meta.url));
  const webCandidates = [
    join(daemonDir, "../../web/out"),
    home ? join(home, "app/current/packages/web/out") : "",
  ].filter(Boolean);
  const webOut = webCandidates.find((p) => existsSync(p));
  if (webOut) {
    await app.register(fastifyStatic, { root: webOut, prefix: "/" });
  } else {
    app.get("/", async () => ({ message: "Agent Swarm daemon running. Build web UI with pnpm --filter @swarm/web build." }));
  }

  const port = scratchPort ? Number(scratchPort) : ctx.config.port;
  const host = ctx.config.host;

  await app.listen({ port, host });
  writeFileSync(`${ctx.paths.run}/daemon.pid`, String(process.pid));
  writeFileSync(`${ctx.paths.run}/daemon.port`, String(port));

  const stopScheduler = startScheduler(ctx);

  const shutdown = async () => {
    stopScheduler();
    ctx.kb.stopWatching();
    ctx.db.close();
    await app.close();
    process.exit(0);
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);

  console.log(`swarmd listening on http://${host}:${port}`);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
