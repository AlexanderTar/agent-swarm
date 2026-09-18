import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";
import type { MockDaemon, StreamSignal } from "./src/mock/daemon";

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (c) => { data += c; });
    req.on("end", () => resolve(data));
  });
}

function lower(h: IncomingMessage["headers"]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(h)) if (typeof v === "string") out[k.toLowerCase()] = v;
  return out;
}

export function mockDaemon(): Plugin {
  return {
    name: "swarm-mock-daemon",
    apply: "serve",
    configureServer(server) {
      if (process.env.SWARM_MOCK !== "1") return;
      let current: Promise<MockDaemon> | undefined;
      const daemon = () =>
        (current ??= server.ssrLoadModule("/src/mock/daemon.ts").then((m) => (m.createMockDaemon as () => MockDaemon)()));

      server.middlewares.use(async (req: IncomingMessage, res: ServerResponse, next: () => void) => {
        const url = req.url ?? "";
        if (!url.startsWith("/api/") && !url.startsWith("/__mock/")) return next();
        if (url === "/__mock/reset") {
          current = undefined;
          await daemon();
          return void res.end("ok");
        }
        const d = await daemon();
        if (url === "/__mock/disconnect") return void (d.disconnect(), res.end("ok"));
        if (url === "/__mock/reconnect") return void (d.reconnect(), res.end("ok"));
        if (d.offline) {
          res.statusCode = 503;
          return void res.end();
        }
        const headers = lower(req.headers);
        if (url.startsWith("/api/events")) {
          if (headers.authorization !== `Bearer ${d.db.token}`) {
            res.statusCode = 401;
            return void res.end();
          }
          res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" });
          const send = (e: { seq: number; type: string; data: unknown }) =>
            res.write(`id: ${e.seq}\nevent: ${e.type}\ndata: ${JSON.stringify(e.data)}\n\n`);
          const lastId = headers["last-event-id"];
          const after = lastId === undefined ? d.events.length : Number(lastId);
          if (lastId !== undefined && after < d.retainedFrom - 1) res.write("event: reset\ndata: {}\n\n");
          else for (const e of d.events) if (e.seq > after) send(e);
          res.write(": open\n\n");
          const unsubscribe = d.subscribe((s: StreamSignal) => {
            if (s === "close") {
              unsubscribe();
              res.end();
            } else send(s);
          });
          req.on("close", unsubscribe);
          return;
        }
        const raw = await readBody(req);
        const r = d.handle({ method: req.method ?? "GET", url, headers, body: raw ? JSON.parse(raw) : undefined });
        res.statusCode = r.status;
        res.setHeader("Content-Type", "application/json");
        res.end(r.body === undefined || r.status === 204 ? "" : JSON.stringify(r.body));
      });
    },
  };
}
