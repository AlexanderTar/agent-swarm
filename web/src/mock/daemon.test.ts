import { describe, expect, it } from "vitest";
import type { AgentNode, Graph, Item, ItemDetail, Request } from "../types";
import { createMockDaemon } from "./daemon";
import { mockFetch } from "./fetch";

const auth = { authorization: "Bearer mock-token" };
const call = (d: ReturnType<typeof createMockDaemon>, method: string, url: string, body?: unknown) =>
  d.handle({ method, url, headers: auth, body });

describe("mock daemon", () => {
  it("requires the token except for bootstrap", () => {
    const d = createMockDaemon();
    expect(d.handle({ method: "GET", url: "/api/bootstrap" })).toEqual({ status: 200, body: { token: "mock-token" } });
    expect(d.handle({ method: "GET", url: "/api/items?view=flat" }).status).toBe(401);
    expect(call(d, "GET", "/api/nope").status).toBe(404);
  });

  it("serves items and item detail", () => {
    const d = createMockDaemon();
    const list = call(d, "GET", "/api/items?view=flat").body as { items: Item[] };
    expect(list.items.some((i) => i.key === "EPIC-12")).toBe(true);
    const det = call(d, "GET", "/api/items/STORY-40").body as ItemDetail;
    expect(det.ancestors.map((i) => i.key)).toEqual(["EPIC-12"]);
    expect(det.children.map((i) => i.key)).toContain("TASK-101");
    expect(det.agents.map((a) => a.name)).toContain("login-form-coder");
    expect(call(d, "GET", "/api/items/TASK-102").body).toMatchObject({ deps: { blocked_by: [{ key: "TASK-98" }] } });
  });

  it("computes blocked_by at construction, not just after a mutation (13.5, store.go:445)", () => {
    const d = createMockDaemon();
    const t102 = d.db.items.find((i) => i.key === "TASK-102")!;
    // TASK-98 is done; the daemon computes blocked_by from unresolved dependencies only.
    expect(t102.blocked_by).toEqual([]);
    const t104 = d.db.items.find((i) => i.key === "TASK-104")!;
    // TASK-102 is not closed, so it still counts as an unresolved blocker.
    expect(t104.blocked_by).toEqual(["TASK-102"]);
    // The raw edge list on the details endpoint is unaffected — it's not the unresolved computation.
    expect(call(d, "GET", "/api/items/TASK-102").body).toMatchObject({ deps: { blocked_by: [{ key: "TASK-98" }] } });
  });

  it("lists an epic's agents without a duplicate finished node (F14)", () => {
    const d = createMockDaemon();
    const det = call(d, "GET", "/api/items/EPIC-12").body as ItemDetail;
    const names = det.agents.map((a) => a.name);
    expect(names.filter((n) => n === "login-form-coder-1")).toHaveLength(0);
  });

  it("applies and refuses status changes", () => {
    const d = createMockDaemon();
    const story = d.db.items.find((i) => i.key === "STORY-40")!;
    expect(call(d, "PATCH", "/api/items/STORY-40", { status: "done", revision: story.revision })).toEqual({
      status: 422,
      body: { error: { code: "transition_denied", message: "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.", reason: "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first." } },
    });
    expect(call(d, "PATCH", "/api/items/STORY-40", { status: "blocked", revision: story.revision - 1 }).status).toBe(409);
    const ok = call(d, "PATCH", "/api/items/STORY-40", { status: "blocked", revision: story.revision });
    expect(ok.status).toBe(200);
    expect((ok.body as Item).status).toBe("blocked");
    expect(d.events.at(-1)).toMatchObject({ type: "item.changed", data: { key: "STORY-40", root_key: "EPIC-12" } });
  });

  it("saves and restores status_before_block across a Blocked round-trip (13.1)", () => {
    const d = createMockDaemon();
    const t = d.db.items.find((i) => i.key === "TASK-101")!;
    expect(t.status).toBe("in_progress");
    const toBlocked = call(d, "PATCH", "/api/items/TASK-101", { status: "blocked", revision: t.revision });
    expect(toBlocked.status).toBe(200);
    expect((toBlocked.body as Item).status_before_block).toBe("in_progress");
    const t2 = toBlocked.body as Item;
    const backOut = call(d, "PATCH", "/api/items/TASK-101", { status: "in_progress", revision: t2.revision });
    expect(backOut.status).toBe(200);
    expect((backOut.body as Item).status).toBe("in_progress");
    expect((backOut.body as Item).status_before_block).toBeNull();
  });

  it("creates items idempotently and refuses spikes", () => {
    const d = createMockDaemon();
    const body = { request_id: "r1", type: "task", title: "New", brief: "", acceptance: [], parent_key: "STORY-40" };
    const a = call(d, "POST", "/api/items", body);
    const b = call(d, "POST", "/api/items", body);
    expect(a.status).toBe(201); // contracts D-12: POST /api/items is 201, a replay is 201 too
    expect(b.status).toBe(201);
    expect(b.body).toEqual(a.body);
    expect(call(d, "POST", "/api/items", { ...body, request_id: "r2", type: "spike" })).toMatchObject({
      status: 400, body: { error: { message: "Spikes start with an intent. Use New spike." } },
    });
    expect(call(d, "POST", "/api/items", { ...body, request_id: "r3", parent_key: "EPIC-12" }).status).toBe(400);
  });

  it("matches the daemon's exact error copy (13.3)", () => {
    const d = createMockDaemon();
    expect(d.handle({ method: "GET", url: "/api/items?view=flat" })).toMatchObject({
      status: 401, body: { error: { code: "unauthorized", message: "Missing or invalid token." } },
    });
    expect(call(d, "GET", "/api/nope")).toMatchObject({
      status: 404, body: { error: { code: "not_found", message: "Unknown API route." } },
    });
    expect(call(d, "GET", "/api/items/NOPE-1")).toMatchObject({
      status: 404, body: { error: { code: "not_found", message: "No item NOPE-1." } },
    });
    expect(call(d, "POST", "/api/items", { request_id: "p1", type: "task", title: "X", brief: "", acceptance: [] })).toMatchObject({
      status: 400, body: { error: { message: "A task needs a parent story, bug or spike." } },
    });
    expect(call(d, "POST", "/api/items", { request_id: "p2", type: "task", title: "X", brief: "", acceptance: [], parent_key: "EPIC-12" })).toMatchObject({
      status: 400, body: { error: { message: "A task can't be a child of an epic." } },
    });
    expect(call(d, "POST", "/api/items", { request_id: "p3", type: "story", title: "X", brief: "", acceptance: [], parent_key: "NOPE-1" })).toMatchObject({
      status: 404, body: { error: { message: "No item NOPE-1." } },
    });
  });

  it("creates a repo, 201 per contracts D-12, and refuses a path with no git repo", () => {
    const d = createMockDaemon();
    const r = call(d, "POST", "/api/repos", { path: "/Users/alex/GitHub/new-repo" });
    expect(r.status).toBe(201);
    expect(call(d, "POST", "/api/repos", { path: "/Users/alex/GitHub/not-a-repo" }).status).toBe(422);
  });

  it("adds dependencies and refuses cycles and hierarchy edges", () => {
    const d = createMockDaemon();
    expect(call(d, "POST", "/api/items/TASK-98/deps", { blocked_by: "TASK-104" })).toMatchObject({
      status: 409, body: { error: { code: "conflict", message: "This dependency would create a cycle." } },
    });
    // F10: the daemon refuses a hierarchy edge with 409 conflict (items/deps.go:57), not 400.
    expect(call(d, "POST", "/api/items/TASK-101/deps", { blocked_by: "STORY-40" })).toMatchObject({
      status: 409, body: { error: { code: "conflict", message: "A task can't depend on its own story or epic." } },
    });
    expect(call(d, "POST", "/api/items/TASK-103/deps", { blocked_by: "TASK-101" }).status).toBe(204);
  });

  it("builds root and neighbourhood graphs with external nodes", () => {
    const d = createMockDaemon();
    const root = call(d, "GET", "/api/items/TASK-102/graph?scope=root").body as Graph;
    expect(root.nodes.find((n) => n.key === "TASK-98")).toMatchObject({ external: true, root_key: "BUG-7" });
    expect(root.nodes.find((n) => n.key === "STORY-41")?.external).toBe(false);
    const hood = call(d, "GET", "/api/items/TASK-104/graph?scope=neighbourhood&hops=1").body as Graph;
    expect(hood.nodes.map((n) => n.key).sort()).toEqual(["TASK-102", "TASK-104"]);
    const two = call(d, "GET", "/api/items/TASK-104/graph?scope=neighbourhood&hops=2").body as Graph;
    expect(two.nodes.map((n) => n.key).sort()).toEqual(["TASK-102", "TASK-104", "TASK-98"]);
  });

  it("refuses approve when artifact_revision is stale (13.4, §16.11 C2)", () => {
    const d = createMockDaemon();
    expect(call(d, "POST", "/api/requests/req_section/approve", { section_sha256: "sha-dm-3", artifact_revision: 2, via: "board" })).toMatchObject({
      status: 409, body: { error: { message: "This request changed. Review the latest version." } },
    });
    const ok = call(d, "POST", "/api/requests/req_section/approve", { section_sha256: "sha-dm-3", artifact_revision: 3, via: "board" });
    expect(ok.status).toBe(200);
    expect((ok.body as Request).state).toBe("approved");
  });

  it("resolves requests with hash, version and comment checks", () => {
    const d = createMockDaemon();
    expect(call(d, "POST", "/api/requests/req_section/approve", { section_sha256: "old", via: "board" })).toMatchObject({
      status: 409, body: { error: { message: "This request changed. Review the latest version." } },
    });
    expect(call(d, "POST", "/api/requests/req_section/request-changes", { comment: "", via: "board" }).status).toBe(400);
    expect(call(d, "POST", "/api/requests/req_repos/confirm-repos", { repos: ["repo_chat"], repos_version: 5, via: "board" }).status).toBe(409);
    expect(call(d, "POST", "/api/requests/req_repos/confirm-repos", { repos: [], repos_version: 0, via: "board" }).status).toBe(400);
    const ok = call(d, "POST", "/api/requests/req_section/approve", { section_sha256: "sha-dm-3", artifact_revision: 3, via: "board" });
    expect((ok.body as Request).state).toBe("approved");
    expect(call(d, "POST", "/api/requests/req_section/approve", { section_sha256: "sha-dm-3", via: "board" }).status).toBe(409);
    expect((call(d, "GET", "/api/requests?state=open").body as Request[]).some((r) => r.id === "req_section")).toBe(false);
    expect(d.events.at(-1)?.type).toBe("request.resolved");
    const close = call(d, "POST", "/api/requests/req_close/close-spike", { via: "board" });
    expect(close.status).toBe(200);
    expect(d.db.items.find((i) => i.key === "SPIKE-4")?.status).toBe("done");
  });

  it("serves artifact snapshots by revision and section", () => {
    const d = createMockDaemon();
    const r = call(d, "GET", "/api/artifacts/art_spec?revision=3&section=data-model");
    expect(r.body).toMatchObject({ artifact: { id: "art_spec", revision: 3 }, markdown: expect.stringContaining("## Data model") });
    expect(call(d, "GET", "/api/artifacts/art_spec?revision=9").status).toBe(404);
  });

  it("starts orchestrators and spikes and refuses duplicates", () => {
    const d = createMockDaemon();
    const body = { request_id: "o1", agent: "claude", model: "opus", repos: ["repo_chat"], repos_version: 0, name: "billing-orchestrator" };
    const r = call(d, "POST", "/api/items/EPIC-20/orchestrator", body);
    expect(r.status).toBe(200);
    expect(d.db.items.find((i) => i.key === "EPIC-20")).toMatchObject({ status: "ready", repos: ["repo_chat"], repos_version: 1 });
    expect(call(d, "POST", "/api/items/EPIC-20/orchestrator", { ...body, request_id: "o2", name: "x" })).toMatchObject({
      status: 409, body: { error: { message: "This item already has an orchestrator." } },
    });
    expect(call(d, "POST", "/api/spikes", { request_id: "s1", name: "!!!", intent: "feature", agent: "claude", model: "opus" })).toMatchObject({
      status: 400, body: { error: { message: "Enter a name containing a letter or number." } },
    });
    expect(call(d, "POST", "/api/spikes", { request_id: "s2", name: "Auth epic orchestrator", intent: "feature", agent: "claude", model: "opus" })).toMatchObject({
      status: 409, body: { error: { message: "This agent name is already in use." } },
    });
    d.db.settings.max_orchestrators = 8;
    const s = call(d, "POST", "/api/spikes", { request_id: "s3", name: "Offline sync", intent: "debug", agent: "claude", model: "opus" });
    expect(s.body).toMatchObject({ item: { type: "spike", status: "draft", spike_intent: "debug" }, agent: { name: "offline-sync", state: "active" }, queued: false });
    d.db.settings.max_orchestrators = 1;
    const q = call(d, "POST", "/api/spikes", { request_id: "s4", name: "Another spike", intent: "feature", agent: "claude", model: "opus" });
    expect(q.body).toMatchObject({ agent: { state: "queued", session: null }, queued: true });
  });

  it("runs agent actions", () => {
    const d = createMockDaemon();
    expect(call(d, "POST", "/api/agents/login-form-coder/pause", { scope: "session" }).body).toMatchObject({ session: { state: "pause_requested" } });
    expect(call(d, "POST", "/api/agents/crash-debug-orchestrator/resume", {}).body).toMatchObject({ session: { state: "running" } });
    expect(call(d, "POST", "/api/agents/nobody/pause", {}).status).toBe(404);
  });

  it("acknowledges an agent with 204 and no body (13.6, contracts §4)", () => {
    const d = createMockDaemon();
    const r = call(d, "POST", "/api/agents/login-form-coder/ack", {});
    expect(r).toEqual({ status: 204, body: undefined });
    expect(d.db.agents.flatMap((a) => a.children).find((a) => a.name === "login-form-coder")?.state).toBe("acknowledged");
  });

  it("filters agents by state and root (13.2, contracts §5)", () => {
    const d = createMockDaemon();
    call(d, "POST", "/api/agents/retry-copy-orchestrator/cancel", {});
    const active = (call(d, "GET", "/api/agents?state=active").body as AgentNode[]).map((a) => a.name);
    expect(active).not.toContain("retry-copy-orchestrator");
    expect(active).toContain("auth-epic-orchestrator");
    const all = (call(d, "GET", "/api/agents?state=all").body as AgentNode[]).map((a) => a.name);
    expect(all).toContain("retry-copy-orchestrator");
    const byRoot = (call(d, "GET", "/api/agents?root=BUG-7").body as AgentNode[]).map((a) => a.name);
    expect(byRoot).toEqual(["crash-debug-orchestrator"]);
    // the nested finished[] array must survive filtering (§16.9 "▸ Finished (N)")
    const auth = (call(d, "GET", "/api/agents").body as AgentNode[]).find((a) => a.name === "auth-epic-orchestrator");
    expect(auth?.finished.map((a) => a.name)).toContain("login-form-coder-1");
  });

  it("honours overrides", () => {
    const d = createMockDaemon();
    d.override("GET /api/settings", { status: 500, body: { error: { code: "internal", message: "x" } } });
    expect(call(d, "GET", "/api/settings").status).toBe(500);
  });
});

describe("mockFetch", () => {
  it("serves JSON and streams events with replay after Last-Event-ID", async () => {
    const d = createMockDaemon();
    const f = mockFetch(d);
    const res = await f("/api/bootstrap");
    expect(await res.json()).toEqual({ token: "mock-token" });
    d.emit("item.changed", { key: "A", root_key: "A" });
    d.emit("item.changed", { key: "B", root_key: "B" });
    const stream = await f("/api/events", { headers: { Authorization: "Bearer mock-token", "Last-Event-ID": "1" } });
    const reader = stream.body!.getReader();
    const text = new TextDecoder().decode((await reader.read()).value);
    expect(text).toBe('id: 2\nevent: item.changed\ndata: {"key":"B","root_key":"B"}\n\n');
    d.disconnect();
    expect((await reader.read()).done).toBe(true);
    await expect(f("/api/settings", { headers: { Authorization: "Bearer mock-token" } })).rejects.toThrow("Failed to fetch");
  });

  it("sends reset for an expired Last-Event-ID", async () => {
    const d = createMockDaemon();
    d.emit("item.changed", {});
    d.emit("item.changed", {});
    d.retainedFrom = 2;
    const stream = await mockFetch(d)("/api/events", { headers: { Authorization: "Bearer mock-token", "Last-Event-ID": "0" } });
    const text = new TextDecoder().decode((await stream.body!.getReader().read()).value);
    expect(text.startsWith("event: reset\ndata: {}\n\n")).toBe(true);
  });

  it("sends reset for a future Last-Event-ID (F17)", async () => {
    const d = createMockDaemon();
    d.emit("item.changed", {});
    d.emit("item.changed", {});
    const stream = await mockFetch(d)("/api/events", { headers: { Authorization: "Bearer mock-token", "Last-Event-ID": "9" } });
    const text = new TextDecoder().decode((await stream.body!.getReader().read()).value);
    expect(text.startsWith("event: reset\ndata: {}\n\n")).toBe(true);
  });

  it("holds a response until released (F12)", async () => {
    const d = createMockDaemon();
    const f = mockFetch(d);
    const release = d.hold("GET /api/settings");
    let settled = false;
    const pending = f("/api/settings", { headers: { Authorization: "Bearer mock-token" } }).then((r) => {
      settled = true;
      return r;
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(settled).toBe(false);
    release();
    const res = await pending;
    expect(settled).toBe(true);
    expect(res.status).toBe(200);
  });
});
