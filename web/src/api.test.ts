import { describe, expect, it, vi } from "vitest";
import { ApiError, createApi, errorText } from "./api";

type Call = { url: string; init: RequestInit | undefined };

function fakeFetch(routes: Record<string, (init?: RequestInit) => Response>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    const method = init?.method ?? "GET";
    const handler = routes[`${method} ${url}`];
    if (!handler) return new Response("no route", { status: 599 });
    return handler(init);
  });
  return { fn: fn as unknown as typeof fetch, calls };
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const localMe = { "GET /api/auth/me": () => json({ origin: "local", authenticated: true }) };
const remoteMe = (authenticated = true) => ({ "GET /api/auth/me": () => json({ origin: "tunnel", authenticated, email: "me@example.com" }) });

describe("createApi", () => {
  it("bootstraps the token once and sends it as a Bearer header", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t1" }),
      "GET /api/items?view=flat": () => json({ items: [], matches: 0 }),
    });
    const api = createApi({ fetchFn: f.fn });
    await api.items();
    await api.items();
    expect(f.calls.filter((c) => c.url === "/api/bootstrap")).toHaveLength(1);
    const headers = f.calls.at(-1)?.init?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer t1");
  });

  it("re-bootstraps once on 401 and retries", async () => {
    let tokens = 0;
    let first = true;
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: `t${++tokens}` }),
      "GET /api/settings": (init) => {
        const auth = (init?.headers as Record<string, string>).Authorization;
        if (first) { first = false; return json({ error: { code: "unauthorized", message: "no" } }, 401); }
        return json({ ok: auth });
      },
    });
    const api = createApi({ fetchFn: f.fn });
    await expect(api.settings()).resolves.toEqual({ ok: "Bearer t2" });
  });

  it("gives up after the second 401", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "GET /api/settings": () => json({ error: { code: "unauthorized", message: "no" } }, 401),
    });
    await expect(createApi({ fetchFn: f.fn }).settings()).rejects.toMatchObject({ status: 401, code: "unauthorized" });
  });

  it("parses the error envelope including the transition reason", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "PATCH /api/items/STORY-40": () =>
        json({ error: { code: "transition_denied", message: "denied", reason: "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first." } }, 422),
    });
    const err = await createApi({ fetchFn: f.fn }).patchItem("STORY-40", { status: "done", revision: 3 }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 422,
      code: "transition_denied",
      reason: "Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.",
    });
  });

  it("maps a non-JSON failure to internal", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "GET /api/catalog": () => new Response("boom", { status: 502, statusText: "Bad Gateway" }),
    });
    await expect(createApi({ fetchFn: f.fn }).catalog()).rejects.toMatchObject({ status: 502, code: "internal", message: "Bad Gateway" });
  });

  it("returns undefined for 204 and sends JSON bodies", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "POST /api/items/TASK-2/deps": () => new Response(null, { status: 204 }),
    });
    await expect(createApi({ fetchFn: f.fn }).addDep("TASK-2", "TASK-1")).resolves.toBeUndefined();
    const call = f.calls.at(-1);
    expect(call?.init?.body).toBe(JSON.stringify({ blocked_by: "TASK-1" }));
    expect((call?.init?.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("builds query strings and adds via=board", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "GET /api/items/EPIC-1/graph?scope=root": () => json({ nodes: [], edges: [] }),
      "GET /api/items/EPIC-1/graph?scope=neighbourhood&hops=2": () => json({ nodes: [], edges: [] }),
      "GET /api/repos?q=endurio": () => json({ recent: [], groups: [], all: [], scanned_at: 0, scanning: false }),
      "GET /api/repos": () => json({ recent: [], groups: [], all: [], scanned_at: 0, scanning: false }),
      "GET /api/artifacts/art_1?revision=3&section=data-model": () => json({ artifact: {}, markdown: "" }),
      "GET /api/agents?state=active": () => json([]),
      "POST /api/requests/req_1/approve": () => json({}),
      "POST /api/requests/req_1/close-spike": () => json({}),
    });
    const api = createApi({ fetchFn: f.fn });
    await api.graph("EPIC-1", "root", 3);
    await api.graph("EPIC-1", "neighbourhood", 2);
    await api.repos("endurio");
    await api.repos();
    await api.artifact("art_1", { revision: 3, section: "data-model" });
    await api.agents();
    await api.approve("req_1", { section_sha256: "abc", artifact_revision: 3 });
    await api.closeSpike("req_1");
    const bodies = f.calls.filter((c) => c.init?.method === "POST").map((c) => JSON.parse(String(c.init?.body)));
    expect(bodies).toEqual([
      { section_sha256: "abc", artifact_revision: 3, via: "board" },
      { via: "board" },
    ]);
  });

  it("encodes agent actions", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "POST /api/agents/login-form-coder/pause": () => json({}),
    });
    await createApi({ fetchFn: f.fn }).agentAction("login-form-coder", "pause", { scope: "subtree" });
    expect(JSON.parse(String(f.calls.at(-1)?.init?.body))).toEqual({ scope: "subtree" });
  });

  it("does not cache a failed bootstrap", async () => {
    let n = 0;
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => (++n === 1 ? new Response("down", { status: 503 }) : json({ token: "t" })),
      "GET /api/settings": () => json({ ok: true }),
    });
    const api = createApi({ fetchFn: f.fn });
    await expect(api.settings()).rejects.toMatchObject({ status: 503 });
    await expect(api.settings()).resolves.toEqual({ ok: true });
  });
});

describe("createApi auth modes", () => {
  it("keeps the Bearer flow when /api/auth/me says local", async () => {
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t1" }),
      "GET /api/settings": () => json({ ok: true }),
    });
    const api = createApi({ fetchFn: f.fn });
    await api.settings();
    await api.settings();
    expect(f.calls.filter((c) => c.url === "/api/auth/me")).toHaveLength(1);
    expect(f.calls.filter((c) => c.url === "/api/bootstrap")).toHaveLength(1);
    expect(await api.authHeaders()).toEqual({ Authorization: "Bearer t1" });
  });

  it("uses the session cookie off this Mac: no bootstrap, no Authorization, same-origin credentials", async () => {
    const f = fakeFetch({ ...remoteMe(), "GET /api/settings": () => json({ ok: true }) });
    const api = createApi({ fetchFn: f.fn });
    await expect(api.settings()).resolves.toEqual({ ok: true });
    expect(f.calls.some((c) => c.url === "/api/bootstrap")).toBe(false);
    const init = f.calls.at(-1)?.init;
    expect(init?.headers as Record<string, string>).not.toHaveProperty("Authorization");
    expect(init?.credentials).toBe("same-origin");
    expect(await api.authHeaders()).toEqual({});
  });

  it("reports the session and the origin it came from", async () => {
    const f = fakeFetch(remoteMe(false));
    await expect(createApi({ fetchFn: f.fn }).session()).resolves.toEqual({ origin: "tunnel", authenticated: false, email: "me@example.com" });
  });

  it("routes a 401 in cookie mode to onUnauthenticated without retrying", async () => {
    const onUnauthenticated = vi.fn();
    const f = fakeFetch({
      ...remoteMe(),
      "GET /api/settings": () => json({ error: { code: "unauthorized", message: "no" } }, 401),
    });
    const api = createApi({ fetchFn: f.fn, onUnauthenticated });
    await expect(api.settings()).rejects.toMatchObject({ status: 401 });
    expect(onUnauthenticated).toHaveBeenCalledTimes(1);
    expect(f.calls.filter((c) => c.url === "/api/settings")).toHaveLength(1);
    await api.unauthorized();
    expect(onUnauthenticated).toHaveBeenCalledTimes(2);
  });

  it("does not call onUnauthenticated in token mode", async () => {
    const onUnauthenticated = vi.fn();
    const f = fakeFetch({
      ...localMe,
      "GET /api/bootstrap": () => json({ token: "t" }),
      "GET /api/settings": () => json({ error: { code: "unauthorized", message: "no" } }, 401),
    });
    const api = createApi({ fetchFn: f.fn, onUnauthenticated });
    await expect(api.settings()).rejects.toMatchObject({ status: 401 });
    await api.session();
    await api.unauthorized();
    expect(onUnauthenticated).not.toHaveBeenCalled();
  });

  it("posts credentials to login and clears the cached session", async () => {
    let authed = false;
    const f = fakeFetch({
      "GET /api/auth/me": () => json({ origin: "tunnel", authenticated: authed }),
      "POST /api/auth/login": () => { authed = true; return new Response(null, { status: 204 }); },
    });
    const api = createApi({ fetchFn: f.fn });
    expect((await api.session()).authenticated).toBe(false);
    await api.login("me@example.com", "pw");
    const login = f.calls.find((c) => c.url === "/api/auth/login");
    expect(JSON.parse(String(login?.init?.body))).toEqual({ email: "me@example.com", password: "pw" });
    expect(login?.init?.credentials).toBe("same-origin");
    expect((await api.session()).authenticated).toBe(true);
  });

  it("surfaces the wrong-password and rate-limit errors from login", async () => {
    let n = 0;
    const f = fakeFetch({
      ...remoteMe(false),
      "POST /api/auth/login": () => (++n === 1
        ? json({ error: { code: "unauthorized", message: "Wrong email or password." } }, 401)
        : new Response(JSON.stringify({ error: { code: "rate_limited", message: "Too many login attempts. Try again later." } }), { status: 429, headers: { "Retry-After": "540" } })),
    });
    const api = createApi({ fetchFn: f.fn });
    await expect(api.login("a@b.c", "x")).rejects.toMatchObject({ status: 401, code: "unauthorized" });
    await expect(api.login("a@b.c", "x")).rejects.toMatchObject({ status: 429, code: "rate_limited", retryAfter: 540 });
  });

  it("posts logout", async () => {
    const f = fakeFetch({ ...remoteMe(), "POST /api/auth/logout": () => new Response(null, { status: 204 }) });
    await createApi({ fetchFn: f.fn }).logout();
    expect(f.calls.some((c) => c.url === "/api/auth/logout" && c.init?.method === "POST")).toBe(true);
  });
});

describe("errorText", () => {
  it("prefers the transition reason, then the message", () => {
    expect(errorText(new ApiError(422, "transition_denied", "denied", "Couldn't move TASK-7 to Done. No agent has reported it complete."))).toBe(
      "Couldn't move TASK-7 to Done. No agent has reported it complete.",
    );
    expect(errorText(new ApiError(409, "conflict", "This item changed elsewhere. Showing its latest status."))).toBe(
      "This item changed elsewhere. Showing its latest status.",
    );
    expect(errorText(new Error("Failed to fetch"))).toBe("Failed to fetch");
    expect(errorText("boom")).toBe("boom");
  });
});
