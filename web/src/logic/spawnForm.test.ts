import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { seed } from "../mock/fixtures";
import { makeAgent } from "./agentActions";
import { makeItem } from "./tree";
import { mapSubmitError, nameError, newRequestId, orchestratorPayload, orchestratorsBusy, spikePayload, submitLabel } from "./spawnForm";

const db = seed();
const fields = { choice: { agent: "claude" as const, model: "opus", effort: "" }, advisor: { agent: "claude" as const, model: "fable" } };

describe("spawn form rules (§16.3, §16.10)", () => {
  it("detects the orchestrator limit", () => {
    // fixture: 6 live agents (4 orchestrators + 2 active children of
    // auth-epic-orchestrator) share one pool now (unify-agent-limits).
    expect(orchestratorsBusy(db.agents, db.settings)).toBe(true);
    expect(orchestratorsBusy(db.agents, { ...db.settings, max_concurrent_agents: 8 })).toBe(false);
    expect(orchestratorsBusy([makeAgent({ role: "orchestrator", state: "finished" })], { ...db.settings, max_concurrent_agents: 1 })).toBe(false);
    expect(submitLabel(true)).toBe("Queue orchestrator");
    expect(submitLabel(false)).toBe("Start orchestrator");
  });

  it("validates the name", () => {
    expect(nameError("???")).toBe("Enter a name containing a letter or number.");
    expect(nameError("")).toBe("Enter a name containing a letter or number.");
    expect(nameError("Login crash")).toBeUndefined();
  });

  it("maps submit errors", () => {
    expect(mapSubmitError(new ApiError(409, "conflict", "This agent name is already in use."))).toEqual({ name: "This agent name is already in use." });
    expect(mapSubmitError(new ApiError(400, "bad_request", "Enter a name containing a letter or number."))).toEqual({ name: "Enter a name containing a letter or number." });
    expect(mapSubmitError(new ApiError(422, "preflight_failed", "Codex isn't installed on this Mac."))).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.", detail: "Codex isn't installed on this Mac.",
    });
    expect(mapSubmitError(new ApiError(409, "conflict", "This item already has an orchestrator."))).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.", detail: "This item already has an orchestrator.",
    });
    expect(mapSubmitError(new TypeError("Failed to fetch"))).toEqual({ banner: "Couldn't start orchestrator. Your entries are saved." });
  });

  // Deviation from the brief: orchestratorPayload/spikePayload take a `catalog` argument (see
  // spawnForm.ts's comment) because advisorPayload requires one in this codebase. Passing the real
  // fixture catalog here (rather than []) is what a real Form component does; the fixture's
  // settings.roles.advisor has no stored effort, so the expected payload is unaffected either way.
  it("builds payloads", () => {
    const item = makeItem({ key: "EPIC-12", repos_version: 1 });
    expect(orchestratorPayload({ name: "auth-orchestrator", repos: ["repo_chat"], fields }, item, db.settings, db.catalog, "r1")).toEqual({
      request_id: "r1", agent: "claude", model: "opus", advisor: { agent: "claude", model: "fable" },
      repos: ["repo_chat"], repos_version: 1, name: "auth-orchestrator",
    });
    expect(spikePayload({ name: "Offline sync", intent: "debug", repos: [], request: "", fields: { ...fields, advisor: "none", choice: { ...fields.choice, effort: "high" } } }, db.settings, db.catalog, "r2")).toEqual({
      request_id: "r2", name: "Offline sync", intent: "debug", repos: [], agent: "claude", model: "opus", effort: "high", advisor: "none",
    });
    expect(spikePayload({ name: "x", intent: "feature", repos: ["a"], request: "Look into it", fields }, db.settings, db.catalog, "r3")).toMatchObject({ request: "Look into it", repos: ["a"] });
    expect(newRequestId()).toMatch(/^[0-9a-f-]{36}$/);
  });
});
