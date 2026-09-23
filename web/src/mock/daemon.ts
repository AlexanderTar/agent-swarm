import { C } from "../copy";
import { kebab } from "../logic/kebab";
import { checkMove } from "../logic/transitions";
import { PARENT_TYPES, ancestors, buildIndex, makeItem } from "../logic/tree";
import type { AgentNode, AgentEndpoint, Graph, Item, ItemType, Request } from "../types";
import { type MockDb, NOW, seed } from "./fixtures";

// 13.3: the daemon's own hint for a top-level create with no parent (items/store.go:53).
const PARENT_HINT: Partial<Record<ItemType, string>> = {
  story: "A story needs a parent epic.",
  task: "A task needs a parent story, bug or spike.",
};
const article = (w: string) => (/^[aeiou]/i.test(w) ? `an ${w}` : `a ${w}`);
const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export interface MockRequest { method: string; url: string; headers?: Record<string, string>; body?: unknown }
export interface MockResponse { status: number; body?: unknown }
export interface MockEvent { seq: number; type: string; data: unknown }
export type StreamSignal = MockEvent | "close";

export interface MockDaemon {
  db: MockDb;
  events: MockEvent[];
  calls: { method: string; path: string; body: unknown }[];
  offline: boolean;
  retainedFrom: number;
  handle(req: MockRequest): MockResponse;
  override(route: string, res: MockResponse | ((body: unknown) => MockResponse)): void;
  emit(type: string, data: unknown): void;
  subscribe(fn: (s: StreamSignal) => void): () => void;
  disconnect(): void;
  reconnect(): void;
  // F12: pause a route's response until released, so tests can observe an in-flight request
  // (e.g. an "Updating…" state) before it settles. `handle()` itself stays synchronous;
  // `mockFetch` is what awaits the pending hold.
  hold(route: string): () => void;
  waitFor(route: string): Promise<void>;
}

// biome-ignore lint/suspicious/noExplicitAny: request bodies are untyped JSON
type Body = any;
type Ctx = { p: Record<string, string>; q: URLSearchParams; body: Body };
type Route = [method: string, pattern: RegExp, fn: (c: Ctx) => MockResponse];

const ok = (body?: unknown, status = 200): MockResponse => ({ status, body });
const fail = (status: number, code: string, message: string, reason?: string): MockResponse => ({
  status,
  body: { error: reason === undefined ? { code, message } : { code, message, reason } },
});
// 13.3: matches items/store.go:122 ("No item %s.") for every item-key lookup.
const notFound = (key: string): MockResponse => fail(404, "not_found", `No item ${key}.`);
const closed = (s: string) => s === "done" || s === "cancelled";

function allAgents(nodes: AgentNode[]): AgentNode[] {
  return nodes.flatMap((n) => [n, ...allAgents(n.children), ...allAgents(n.finished)]);
}

export function createMockDaemon(db: MockDb = seed()): MockDaemon {
  const listeners = new Set<(s: StreamSignal) => void>();
  const overrides = new Map<string, MockResponse | ((body: unknown) => MockResponse)>();
  const idem = new Map<string, MockResponse>();
  const holds = new Map<string, Promise<void>>();
  let counter = 1000;

  const item = (key: string) => db.items.find((i) => i.key === key);
  const agent = (name: string) => allAgents(db.agents).find((a) => a.name === name);
  const subtree = (key: string): Set<string> => {
    const out = new Set([key]);
    let grew = true;
    while (grew) {
      grew = false;
      for (const i of db.items) if (i.parent_key && out.has(i.parent_key) && !out.has(i.key)) { out.add(i.key); grew = true; }
    }
    return out;
  };
  const recompute = () => {
    for (const it of db.items) {
      it.blocked_by = db.deps.filter((d) => d.item === it.key && !closed(item(d.blocked_by)?.status ?? "done")).map((d) => d.blocked_by);
      it.open_requests = db.requests.filter((r) => r.state === "open" && r.item_key === it.key).length;
    }
  };
  const once = (id: string | undefined, fn: () => MockResponse): MockResponse => {
    if (!id) return fn();
    const hit = idem.get(id);
    if (hit) return hit;
    const res = fn();
    if (res.status < 400) idem.set(id, res);
    return res;
  };

  const d: MockDaemon = {
    db,
    events: [],
    calls: [],
    offline: false,
    retainedFrom: 0,
    handle,
    override: (route, res) => void overrides.set(route, res),
    emit(type, data) {
      const e = { seq: d.events.length + 1, type, data };
      d.events.push(e);
      for (const l of listeners) l(e);
    },
    subscribe(fn) {
      listeners.add(fn);
      return () => void listeners.delete(fn);
    },
    disconnect() {
      d.offline = true;
      for (const l of [...listeners]) l("close");
    },
    reconnect() {
      d.offline = false;
    },
    hold(route) {
      let release: () => void = () => {};
      holds.set(route, new Promise<void>((res) => { release = res; }));
      return () => {
        release();
        holds.delete(route);
      };
    },
    waitFor(route) {
      return holds.get(route) ?? Promise.resolve();
    },
  };

  const itemChanged = (it: Item) => d.emit("item.changed", { key: it.key, root_key: it.root_key });

  function detail(key: string): MockResponse {
    const it = item(key);
    if (!it) return notFound(key);
    const idx = buildIndex(db.items);
    const keys = subtree(key);
    const all = allAgents(db.agents).filter((a) => keys.has(a.item_key));
    const names = new Set(all.map((a) => a.name));
    return ok({
      item: it,
      ancestors: ancestors(key, idx.byKey),
      children: idx.children.get(key) ?? [],
      deps: {
        blocked_by: db.deps.filter((x) => x.item === key).map((x) => item(x.blocked_by)).filter(Boolean),
        blocks: db.deps.filter((x) => x.blocked_by === key).map((x) => item(x.item)).filter(Boolean),
      },
      agents: all.filter((a) => !a.parent_name || !names.has(a.parent_name)),
      requests: db.requests.filter((r) => r.state === "open" && r.item_key === key),
      artifacts: db.artifacts.filter((a) => a.artifact.item_key === key).map((a) => a.artifact),
    });
  }

  function patch(key: string, body: Body): MockResponse {
    const it = item(key);
    if (!it) return notFound(key);
    if (body.revision !== it.revision) return fail(409, "conflict", C.staleRevision);
    if (body.status && body.status !== it.status) {
      const c = checkMove(it, body.status);
      if (!c.ok) return fail(422, "transition_denied", c.reason, c.reason);
      // 13.1: mirrors setStatus (internal/items/transition.go:304-316) — save the status being left
      // when moving to Blocked, and clear the saved status on every other move.
      it.status_before_block = body.status === "blocked" ? it.status : null;
      it.status = body.status;
    }
    for (const f of ["title", "brief", "acceptance", "priority"] as const) if (body[f] !== undefined) (it as Body)[f] = body[f];
    it.revision += 1;
    it.updated_at = NOW;
    recompute();
    itemChanged(it);
    return ok(it);
  }

  function createItem(body: Body): MockResponse {
    return once(body.request_id, () => {
      if (body.type === "spike") return fail(400, "bad_request", C.spikeViaNewItem);
      const type = body.type as ItemType;
      const allowed = PARENT_TYPES[type];
      const parent = body.parent_key ? item(body.parent_key) : undefined;
      // 13.3: mirrors items/store.go:214-231 — a missing parent key is 404, a disallowed parent type
      // is "%s can't be a child of %s.", and a required-but-absent parent is the daemon's own hint.
      if (!body.parent_key) {
        const hint = PARENT_HINT[type];
        if (hint) return fail(400, "bad_request", hint);
      } else if (!parent) {
        return notFound(body.parent_key);
      } else if (!allowed?.includes(parent.type)) {
        return fail(400, "bad_request", `${capitalize(article(type))} can't be a child of ${article(parent.type)}.`);
      }
      const key = `${String(body.type).toUpperCase()}-${++counter}`;
      const it = makeItem({
        key, type: body.type, title: body.title, brief: body.brief, acceptance: body.acceptance, status: "draft",
        parent_key: parent?.key ?? null, root_key: parent?.root_key ?? key, created_at: NOW, sort_order: counter,
      });
      db.items.push(it);
      itemChanged(it);
      return ok(it, 201); // contracts D-12: POST /api/items is 201, a replay is 201 too
    });
  }

  function addDep(key: string, body: Body): MockResponse {
    const a = item(key);
    const b = item(body.blocked_by);
    if (!a) return notFound(key);
    if (!b) return notFound(body.blocked_by);
    const idx = buildIndex(db.items);
    const lineage = (k: string) => ancestors(k, idx.byKey).map((x) => x.key);
    // F10: the daemon refuses a hierarchy edge with 409 conflict (items/deps.go:57), not 400.
    if (lineage(a.key).includes(b.key) || lineage(b.key).includes(a.key)) return fail(409, "conflict", C.depHierarchy);
    const seen = new Set<string>();
    const stack = [b.key];
    while (stack.length) {
      const cur = stack.pop() as string;
      if (cur === a.key) return fail(409, "conflict", C.cycle);
      if (seen.has(cur)) continue;
      seen.add(cur);
      for (const x of db.deps) if (x.item === cur) stack.push(x.blocked_by);
    }
    db.deps.push({ item: a.key, blocked_by: b.key });
    recompute();
    itemChanged(a);
    return ok(undefined, 204);
  }

  function graph(key: string, q: URLSearchParams): MockResponse {
    const it = item(key);
    if (!it) return notFound(key);
    let keys: Set<string>;
    if (q.get("scope") === "neighbourhood") {
      keys = new Set([key]);
      for (let h = 0; h < Number(q.get("hops") ?? 1); h++) {
        const frontier = new Set(keys);
        for (const x of db.deps) {
          if (frontier.has(x.item)) keys.add(x.blocked_by);
          if (frontier.has(x.blocked_by)) keys.add(x.item);
        }
      }
    } else {
      keys = new Set(db.items.filter((i) => i.root_key === it.root_key).map((i) => i.key));
      for (const x of db.deps) {
        if (keys.has(x.item)) keys.add(x.blocked_by);
        if (keys.has(x.blocked_by)) keys.add(x.item);
      }
    }
    const nodes = [...keys].flatMap((k) => {
      const n = item(k);
      return n ? [{ key: n.key, type: n.type, title: n.title, status: n.status, root_key: n.root_key, external: n.root_key !== it.root_key }] : [];
    });
    const edges = db.deps.filter((x) => keys.has(x.item) && keys.has(x.blocked_by)).map((x) => ({ from: x.blocked_by, to: x.item }));
    return ok({ nodes, edges } satisfies Graph);
  }

  function newAgent(name: string, body: Body, it: Item): AgentNode {
    // Every role shares one admission pool now (unify-agent-limits), so a new
    // orchestrator queues against the same count every other spawn does.
    const running = allAgents(db.agents).filter((a) => a.state === "queued" || a.state === "active").length;
    const queued = running >= db.settings.max_concurrent_agents;
    const node: AgentNode = {
      id: `agt_${++counter}`, name, kind: body.agent, model: body.model, effort: body.effort ?? null, role: "orchestrator",
      item_key: it.key, item_title: it.title, root_key: it.root_key, parent_name: null, advisor: null,
      state: queued ? "queued" : "active",
      session: queued ? null : { id: `ses_${counter}`, state: "spawning", attempt: 1, generation: 1, waiting: false, stale: false, tmux_alive: true, started_at: NOW, ended_at: null },
      preflight_error: null, created_at: NOW, finished_at: null, children: [], finished: [],
    };
    db.agents.push(node);
    d.emit("agent.changed", { name, root_key: it.root_key });
    return node;
  }

  function startOrchestrator(key: string, body: Body): MockResponse {
    return once(body.request_id, () => {
      const it = item(key);
      if (!it) return notFound(key);
      const live = allAgents(db.agents).some((a) => a.item_key === key && a.role === "orchestrator" && (a.state === "active" || a.state === "queued"));
      if (live) return fail(409, "conflict", C.orchestratorExists);
      const name = body.name || `${kebab(it.title, 24)}-orchestrator`;
      if (agent(name)) return fail(409, "conflict", C.nameTaken);
      if (body.repos?.length) {
        it.repos = body.repos;
        it.repos_version += 1;
      }
      if (it.status === "draft") it.status = "ready";
      itemChanged(it);
      return ok(newAgent(name, body, it));
    });
  }

  function createSpike(body: Body): MockResponse {
    return once(body.request_id, () => {
      const name = kebab(String(body.name ?? ""));
      if (!name) return fail(400, "bad_request", C.nameEmpty);
      if (agent(name)) return fail(409, "conflict", C.nameTaken);
      const key = `SPIKE-${++counter}`;
      const it = makeItem({
        key, title: body.name, brief: String(body.request ?? ""), status: "draft",
        spike_intent: body.intent, repos: body.repos ?? [], created_at: NOW,
      });
      db.items.push(it);
      itemChanged(it);
      const node = newAgent(name, body, it);
      return ok({ item: it, agent: node, queued: node.state === "queued" });
    });
  }

  function agentAction(name: string, action: AgentEndpoint): MockResponse {
    const a = agent(name);
    if (!a) return fail(404, "not_found", `${name} not found`);
    const s = a.session;
    switch (action) {
      case "terminal":
        return ok({ tmux: name, opened_by: "fallback" });
      case "pause":
        if (s) s.state = "pause_requested";
        break;
      case "resume":
        if (s && ["pause_requested", "quiescing", "stopping"].includes(s.state)) return fail(409, "conflict", "Still stopping. Try again in a few seconds.");
        if (s) { s.state = "running"; s.generation += 1; }
        break;
      case "cancel":
        a.state = "finished";
        if (s) s.state = "cancelled";
        break;
      case "ack": {
        a.state = "acknowledged";
        // R2 fix: contracts §3.2 — `finished` is "completed, cancelled and acknowledged children";
        // move the node out of its parent's `children` into `finished` (a top-level node has neither
        // bucket to move between — it's just hidden by the `state=active` filter, see agentsList).
        const parent = a.parent_name ? agent(a.parent_name) : undefined;
        if (parent) {
          const i = parent.children.findIndex((c) => c.name === a.name);
          if (i >= 0) parent.children.splice(i, 1);
          if (!parent.finished.some((c) => c.name === a.name)) parent.finished.push(a);
        }
        d.emit("agent.changed", { name, root_key: a.root_key });
        return ok(undefined, 204); // 13.6: contracts §4 — ack is 204, no body
      }
      case "retry":
        a.preflight_error = null;
        a.session = { ...(s ?? { id: `ses_${++counter}`, attempt: 0, generation: 0, waiting: false, stale: false, started_at: NOW, ended_at: null }), state: "spawning", attempt: (s?.attempt ?? 0) + 1, generation: (s?.generation ?? 0) + 1, tmux_alive: true };
        break;
    }
    d.emit("agent.changed", { name, root_key: a.root_key });
    return ok(a);
  }

  function resolve(r: Request, state: Request["state"], text: string | null): MockResponse {
    r.state = state;
    r.response_text = text;
    r.responded_via = "board";
    r.responded_at = NOW;
    recompute();
    d.emit("request.resolved", r);
    return ok(r);
  }

  function requestAction(id: string, action: string, body: Body): MockResponse {
    const r = db.requests.find((x) => x.id === id);
    if (!r) return fail(404, "not_found", `${id} not found`);
    if (r.state !== "open") return fail(409, "conflict", C.staleApproval);
    switch (action) {
      case "request-changes": {
        const c = String(body.comment ?? "");
        if (c.length < 1 || c.length > 2000) return fail(400, "bad_request", C.emptyChange);
        return resolve(r, "changes_requested", c);
      }
      case "approve":
        if (body.section_sha256 !== undefined && body.section_sha256 !== r.section_sha256) return fail(409, "conflict", C.staleApproval);
        // 13.4: contracts §4 / §16.11 C2 — a stale artifact_revision refuses the approval too.
        if (body.artifact_revision !== undefined && body.artifact_revision !== r.artifact_revision) return fail(409, "conflict", C.staleApproval);
        if (body.binding !== undefined && JSON.stringify(body.binding) !== JSON.stringify(r.binding)) return fail(409, "conflict", C.staleApproval);
        return resolve(r, "approved", null);
      case "confirm-repos": {
        if (!Array.isArray(body.repos) || body.repos.length === 0) return fail(400, "bad_request", C.chooseRepo);
        const version = (r.binding as { repos_version: number } | null)?.repos_version ?? 0;
        if (body.repos_version !== version) return fail(409, "conflict", C.staleApproval);
        r.confirmed = body.repos;
        const root = item(r.root_key);
        if (root) { root.repos = body.repos; root.repos_version += 1; itemChanged(root); }
        return resolve(r, "approved", body.comment ?? null);
      }
      case "close-spike": {
        if (r.kind !== "close_spike") return fail(400, "bad_request", "not a close_spike request");
        const spike = item(r.item_key);
        if (spike) { spike.status = "done"; itemChanged(spike); }
        return resolve(r, "approved", null);
      }
    }
    return fail(404, "not_found", action);
  }

  function artifact(id: string, q: URLSearchParams): MockResponse {
    const rec = db.artifacts.find((a) => a.artifact.id === id);
    if (!rec) return fail(404, "not_found", `${id} not found`);
    const rev = Number(q.get("revision") ?? rec.artifact.head_revision);
    const snap = rec.revisions[rev];
    if (!snap) return fail(404, "not_found", `revision ${rev} not found`);
    const section = q.get("section");
    const markdown = section ? snap.sections[section] : snap.markdown;
    if (markdown === undefined) return fail(404, "not_found", `section ${section} not found`);
    return ok({ artifact: { ...rec.artifact, revision: rev }, markdown });
  }

  function repos(q: string): MockResponse {
    const needle = q.toLowerCase();
    const hit = (r: { name: string; path: string; groups: string[]; remote_url: string | null }) =>
      [r.name, r.path, r.remote_url ?? "", ...r.groups].some((s) => s.toLowerCase().includes(needle));
    const x = db.repos;
    if (!needle) return ok(x);
    return ok({
      ...x,
      recent: x.recent.filter(hit),
      groups: x.groups.map((g) => ({ ...g, repos: g.repos.filter(hit) })).filter((g) => g.repos.length),
      all: x.all.filter(hit),
    });
  }

  // 13.2: contracts §5 `GET /api/agents?state=active|all&root=`. `active` (the default) drops
  // top-level nodes that are finished/acknowledged; the nested finished[] array always survives,
  // since §16.9's "▸ Finished (N)" renders from it.
  // R2 fix: an unrecognized `state` is a 400, mirroring every other enum query param the daemon
  // validates the same way (items/list.go:38-44 — "view must be tree or flat.", "Unknown status %q.").
  function agentsList(q: URLSearchParams): MockResponse {
    const state = q.get("state") ?? "active";
    if (state !== "active" && state !== "all") return fail(400, "bad_request", `Unknown state "${state}".`);
    const root = q.get("root");
    let nodes = db.agents;
    if (root) nodes = nodes.filter((a) => a.root_key === root);
    if (state === "active") nodes = nodes.filter((a) => a.state !== "finished" && a.state !== "acknowledged");
    return ok(nodes);
  }

  function addRepo(body: Body): MockResponse {
    const path = String(body.path ?? "");
    if (!path.startsWith("/") || path.endsWith("/not-a-repo")) return fail(422, "bad_request", C.notARepo);
    const repo = {
      id: `repo_${++counter}`, name: path.split("/").filter(Boolean).at(-1) ?? path, path, remote_url: null,
      remote_owner: null, default_branch: "main", source: "manual" as const, groups: [], missing: false, dirty: false,
      last_used_at: null,
    };
    db.repos.all.push(repo);
    return ok(repo, 201); // contracts D-12: POST /api/repos is 201
  }

  const K = "(?<key>[^/?]+)";
  const routes: Route[] = [
    ["GET", /^\/api\/items$/, () => ok({ items: db.items, matches: db.items.length })],
    ["POST", /^\/api\/items$/, (c) => createItem(c.body)],
    ["GET", new RegExp(`^/api/items/${K}$`), (c) => detail(c.p.key as string)],
    ["PATCH", new RegExp(`^/api/items/${K}$`), (c) => patch(c.p.key as string, c.body)],
    ["POST", new RegExp(`^/api/items/${K}/deps$`), (c) => addDep(c.p.key as string, c.body)],
    ["GET", new RegExp(`^/api/items/${K}/graph$`), (c) => graph(c.p.key as string, c.q)],
    ["GET", new RegExp(`^/api/items/${K}/checkpoints$`), (c) => ok(
      db.checkpoints.filter((x) => x.item_key === c.p.key).sort((a, b) => b.created_at - a.created_at).slice(0, Number(c.q.get("limit") ?? 50)),
    )],
    ["POST", new RegExp(`^/api/items/${K}/orchestrator$`), (c) => startOrchestrator(c.p.key as string, c.body)],
    ["POST", /^\/api\/spikes$/, (c) => createSpike(c.body)],
    ["GET", /^\/api\/agents$/, (c) => agentsList(c.q)],
    ["POST", /^\/api\/agents\/(?<name>[^/]+)\/(?<action>pause|resume|cancel|ack|retry|terminal)$/, (c) => agentAction(c.p.name as string, c.p.action as AgentEndpoint)],
    ["GET", /^\/api\/agents\/(?<name>[^/]+)\/advice$/, (c) => ok(db.advice[c.p.name as string] ?? [])],
    ["GET", /^\/api\/requests$/, () => ok(db.requests.filter((r) => r.state === "open"))],
    ["POST", /^\/api\/requests\/(?<id>[^/]+)\/(?<action>approve|request-changes|confirm-repos|close-spike)$/, (c) => requestAction(c.p.id as string, c.p.action as string, c.body)],
    ["GET", /^\/api\/artifacts\/(?<id>[^/]+)$/, (c) => artifact(c.p.id as string, c.q)],
    ["GET", /^\/api\/settings$/, () => ok(db.settings)],
    ["GET", /^\/api\/catalog$/, () => ok(db.catalog)],
    ["GET", /^\/api\/repos$/, (c) => repos(c.q.get("q") ?? "")],
    ["POST", /^\/api\/repos$/, (c) => addRepo(c.body)],
    ["POST", /^\/api\/repos\/rescan$/, () => ok({ found: db.repos.all.length, missing: db.repos.all.filter((r) => r.missing).length })],
  ];

  function handle(req: MockRequest): MockResponse {
    const url = new URL(req.url, "http://mock.test");
    const method = req.method.toUpperCase();
    d.calls.push({ method, path: url.pathname + url.search, body: req.body });
    if (method === "GET" && url.pathname === "/api/bootstrap") return ok({ token: db.token });
    const o = overrides.get(`${method} ${url.pathname}`);
    let hit: RegExpExecArray | null = null;
    let fn: Route[2] | undefined;
    if (!o) {
      for (const [m, re, f] of routes) {
        hit = m === method ? re.exec(url.pathname) : null;
        if (hit) { fn = f; break; }
      }
    }
    // R2 fix: the daemon's catch-all (httpapi/server.go:90-100) is registered without the auth
    // wrapper, so an unknown route 404s unauthenticated too — only a *matched* route enforces the
    // bearer token (httpapi/server.go:120's `wrap`), checked here before running the handler.
    if (!o && !fn) return fail(404, "not_found", "Unknown API route.");
    const authz = req.headers?.authorization ?? req.headers?.Authorization;
    // 13.3: byte-identical to the daemon's pinned copy (contracts §2, httpapi/server.go:120).
    if (authz !== `Bearer ${db.token}`) return fail(401, "unauthorized", "Missing or invalid token.");
    if (o) return typeof o === "function" ? o(req.body) : o;
    return (fn as Route[2])({ p: hit?.groups ?? {}, q: url.searchParams, body: req.body ?? {} });
  }

  // 13.5: compute blocked_by/open_requests once up front, the way the daemon always serves them
  // (store.go:445-446) — not only after the first mutation, which made the fixture's raw
  // "TASK-102 blocked by the already-done TASK-98" literal a time-dependent false green.
  recompute();

  return d;
}
