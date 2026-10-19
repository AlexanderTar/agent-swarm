import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";
import type { MockDaemon, StreamSignal } from "./src/mock/daemon";

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (c) => {
      data += c;
    });
    req.on("end", () => resolve(data));
  });
}

function lower(h: IncomingMessage["headers"]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(h)) if (typeof v === "string") out[k.toLowerCase()] = v;
  return out;
}

// While /__mock/remote?on=1 is set the mock plays a tunnel/LAN origin: /api/auth/me reports it, the
// board API wants the session cookie instead of the Bearer token, and login accepts one account.
const SESSION_COOKIE = "swarm_session=mock";
const LOGIN = { email: "me@example.com", password: "correct-horse-battery" };

export function mockDaemon(): Plugin {
  return {
    name: "swarm-mock-daemon",
    apply: "serve",
    configureServer(server) {
      if (process.env.SWARM_MOCK !== "1") return;
      let current: Promise<MockDaemon> | undefined;
      let remote = false;
      const daemon = () =>
        (current ??= server
          .ssrLoadModule("/src/mock/daemon.ts")
          .then((m) => (m.createMockDaemon as () => MockDaemon)()));

      server.middlewares.use(
        async (req: IncomingMessage, res: ServerResponse, next: () => void) => {
          const url = req.url ?? "";
          if (!url.startsWith("/api/") && !url.startsWith("/__mock/")) return next();
          if (url === "/__mock/reset") {
            current = undefined;
            await daemon();
            return void res.end("ok");
          }
          const d = await daemon();
          if (url.startsWith("/__mock/remote")) {
            remote = new URL(url, "http://mock.test").searchParams.get("on") === "1";
            return void res.end("ok");
          }
          if (url === "/__mock/disconnect") return void (d.disconnect(), res.end("ok"));
          if (url === "/__mock/reconnect") return void (d.reconnect(), res.end("ok"));
          if (d.offline) {
            res.statusCode = 503;
            return void res.end();
          }
          const headers = lower(req.headers);
          const json = (status: number, body?: unknown) => {
            res.statusCode = status;
            res.setHeader("Content-Type", "application/json");
            res.end(body === undefined ? "" : JSON.stringify(body));
          };
          const signedIn = (headers.cookie ?? "").split(/;\s*/).includes(SESSION_COOKIE);
          if (url === "/api/auth/me") {
            return json(
              200,
              remote
                ? {
                    origin: "tunnel",
                    authenticated: signedIn,
                    ...(signedIn && { email: LOGIN.email }),
                  }
                : { origin: "local", authenticated: true },
            );
          }
          if (url === "/api/auth/login" && req.method === "POST") {
            const body = JSON.parse((await readBody(req)) || "{}") as {
              email?: string;
              password?: string;
            };
            if (body.email === "limited@example.com") {
              res.setHeader("Retry-After", "540");
              return json(429, {
                error: {
                  code: "rate_limited",
                  message: "Too many login attempts. Try again later.",
                },
              });
            }
            if (body.email !== LOGIN.email || body.password !== LOGIN.password) {
              return json(401, {
                error: { code: "unauthorized", message: "Wrong email or password." },
              });
            }
            res.setHeader("Set-Cookie", `${SESSION_COOKIE}; Path=/; HttpOnly; SameSite=Lax`);
            return json(204);
          }
          if (url === "/api/auth/logout" && req.method === "POST") {
            res.setHeader("Set-Cookie", "swarm_session=; Path=/; Max-Age=0");
            return json(204);
          }
          if (remote) {
            if (!signedIn)
              return json(401, { error: { code: "unauthorized", message: "Sign in." } });
            headers.authorization = `Bearer ${d.db.token}`; // the cookie stands in for the token
          }
          if (url.startsWith("/api/events")) {
            if (headers.authorization !== `Bearer ${d.db.token}`) {
              res.statusCode = 401;
              return void res.end();
            }
            res.writeHead(200, {
              "Content-Type": "text/event-stream",
              "Cache-Control": "no-cache",
              Connection: "keep-alive",
            });
            const send = (e: { seq: number; type: string; data: unknown }) =>
              res.write(`id: ${e.seq}\nevent: ${e.type}\ndata: ${JSON.stringify(e.data)}\n\n`);
            const lastId = headers["last-event-id"];
            const after = lastId === undefined ? d.events.length : Number(lastId);
            if (lastId !== undefined && after < d.retainedFrom - 1)
              res.write("event: reset\ndata: {}\n\n");
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
          const r = d.handle({
            method: req.method ?? "GET",
            url,
            headers,
            body: raw ? JSON.parse(raw) : undefined,
          });
          res.statusCode = r.status;
          res.setHeader("Content-Type", "application/json");
          res.end(r.body === undefined || r.status === 204 ? "" : JSON.stringify(r.body));
        },
      );
    },
  };
}
