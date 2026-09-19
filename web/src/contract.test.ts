import { describe, expect, it } from "vitest";
import { seed } from "./mock/fixtures";

const BASE = process.env.SWARM_E2E_URL ?? "http://127.0.0.1:17777";
const live = process.env.SWARM_E2E_LIVE === "1";

// Every key present in the fixture must exist in the live value with the same JSON type.
function shapeDiff(fixture: unknown, actual: unknown, path = "$"): string[] {
  if (fixture === null || actual === null) return [];
  if (Array.isArray(fixture)) {
    if (!Array.isArray(actual)) return [`${path}: expected array`];
    return fixture.length && actual.length ? shapeDiff(fixture[0], actual[0], `${path}[0]`) : [];
  }
  if (typeof fixture === "object") {
    if (typeof actual !== "object") return [`${path}: expected object`];
    return Object.entries(fixture as Record<string, unknown>).flatMap(([k, v]) =>
      k in (actual as object) ? shapeDiff(v, (actual as Record<string, unknown>)[k], `${path}.${k}`) : [`${path}.${k}: missing`],
    );
  }
  return typeof fixture === typeof actual ? [] : [`${path}: expected ${typeof fixture}, got ${typeof actual}`];
}

describe.runIf(live)("live API contract", () => {
  const db = seed();
  let token = "";
  const get = async (p: string) => {
    if (!token) token = ((await (await fetch(`${BASE}/api/bootstrap`)).json()) as { token: string }).token;
    const r = await fetch(`${BASE}${p}`, { headers: { Authorization: `Bearer ${token}` } });
    expect(r.ok, p).toBe(true);
    return r.json();
  };
  it.each([
    ["/api/items?view=flat", { items: db.items, matches: 0 }],
    ["/api/items/EPIC-12", { item: db.items[0], ancestors: [], children: [db.items[1]], deps: { blocked_by: [], blocks: [] }, agents: db.agents, requests: [], artifacts: [db.artifacts[0]?.artifact] }],
    ["/api/items/TASK-102/graph?scope=root", { nodes: [{ key: "", type: "", title: "", status: "", root_key: "", external: false }], edges: [{ from: "", to: "" }] }],
    ["/api/items/TASK-101/checkpoints", db.checkpoints],
    ["/api/agents?state=active", db.agents],
    ["/api/requests?state=open", db.requests],
    ["/api/settings", db.settings],
    ["/api/catalog", db.catalog],
    ["/api/repos", db.repos],
  ])("%s matches the board's shapes", async (path, fixture) => {
    expect(shapeDiff(fixture, await get(path))).toEqual([]);
  });
  it("serves artifact snapshots by revision", async () => {
    const reqs = (await get("/api/requests?state=open")) as { artifact_id: string | null; artifact_revision: number | null }[];
    const withArt = reqs.find((r) => r.artifact_id);
    if (!withArt) return;
    const a = await get(`/api/artifacts/${withArt.artifact_id}?revision=${withArt.artifact_revision}`);
    expect(shapeDiff({ artifact: db.artifacts[0]?.artifact, markdown: "" }, a)).toEqual([]);
  });
});
