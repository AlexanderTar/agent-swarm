import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { seed } from "../mock/fixtures";
import { makeAgent } from "./agentActions";
import {
  mapSubmitError,
  nameError,
  newRequestId,
  orchestratorPayload,
  orchestratorsBusy,
  spikePayload,
  submitLabel,
} from "./spawnForm";
import { makeItem } from "./tree";

const db = seed();
const fields = {
  choice: { agent: "claude" as const, model: "opus", effort: "" },
  advisor: { agent: "claude" as const, model: "fable" },
};

describe("spawn form rules (§16.3, §16.10)", () => {
  it("detects the orchestrator limit", () => {
    // fixture: 6 live agents (4 orchestrators + 2 active children of
    // auth-epic-orchestrator) share one pool now (unify-agent-limits).
    expect(orchestratorsBusy(db.agents, db.settings)).toBe(true);
    expect(orchestratorsBusy(db.agents, { ...db.settings, max_concurrent_agents: 8 })).toBe(false);
    expect(
      orchestratorsBusy([makeAgent({ role: "orchestrator", state: "finished" })], {
        ...db.settings,
        max_concurrent_agents: 1,
      }),
    ).toBe(false);
    expect(submitLabel(true)).toBe("Queue orchestrator");
    expect(submitLabel(false)).toBe("Start orchestrator");
  });

  it("validates the name", () => {
    expect(nameError("???")).toBe("Enter a name containing a letter or number.");
    expect(nameError("")).toBe("Enter a name containing a letter or number.");
    expect(nameError("Login crash")).toBeUndefined();
  });

  it("maps a 413 to the remote-access detail", () => {
    expect(mapSubmitError(new ApiError(413, "bad_request", "Request body too large"))).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.",
      detail:
        "The attachments are too large to send over remote access. Remove some and try again.",
    });
  });

  it("maps submit errors", () => {
    expect(
      mapSubmitError(new ApiError(409, "conflict", "This agent name is already in use.")),
    ).toEqual({ name: "This agent name is already in use." });
    expect(
      mapSubmitError(
        new ApiError(400, "bad_request", "Enter a name containing a letter or number."),
      ),
    ).toEqual({ name: "Enter a name containing a letter or number." });
    expect(
      mapSubmitError(new ApiError(422, "preflight_failed", "Codex isn't installed on this computer.")),
    ).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.",
      detail: "Codex isn't installed on this computer.",
    });
    expect(
      mapSubmitError(new ApiError(409, "conflict", "This item already has an orchestrator.")),
    ).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.",
      detail: "This item already has an orchestrator.",
    });
    expect(mapSubmitError(new TypeError("Failed to fetch"))).toEqual({
      banner: "Couldn't start orchestrator. Your entries are saved.",
    });
  });

  // Deviation from the brief: orchestratorPayload/spikePayload take a `catalog` argument (see
  // spawnForm.ts's comment) because advisorPayload requires one in this codebase. Passing the real
  // fixture catalog here (rather than []) is what a real Form component does; the fixture's
  // settings.roles.advisor has no stored effort, so the expected payload is unaffected either way.
  it("builds payloads", async () => {
    const item = makeItem({ key: "EPIC-12", repos_version: 1 });
    expect(
      orchestratorPayload(
        { name: "auth-orchestrator", repos: ["repo_chat"], fields },
        item,
        db.settings,
        db.catalog,
        "r1",
      ),
    ).toEqual({
      request_id: "r1",
      agent: "claude",
      model: "opus",
      advisor: { agent: "claude", model: "fable" },
      repos: ["repo_chat"],
      repos_version: 1,
      name: "auth-orchestrator",
    });
    expect(
      await spikePayload(
        {
          name: "Offline sync",
          intent: "debug",
          repos: [],
          request: "",
          images: [],
          fields: { ...fields, advisor: "none", choice: { ...fields.choice, effort: "high" } },
        },
        db.settings,
        db.catalog,
        "r2",
      ),
    ).toEqual({
      request_id: "r2",
      name: "Offline sync",
      intent: "debug",
      repos: [],
      agent: "claude",
      model: "opus",
      effort: "high",
      advisor: "none",
    });
    expect(
      await spikePayload(
        { name: "x", intent: "feature", repos: ["a"], request: "Look into it", images: [], fields },
        db.settings,
        db.catalog,
        "r3",
      ),
    ).toMatchObject({ request: "Look into it", repos: ["a"] });
    expect(newRequestId()).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("sends the chore intent unchanged", async () => {
    const body = await spikePayload(
      { name: "Tidy deps", intent: "chore", repos: [], request: "", images: [], fields },
      db.settings,
      db.catalog,
      "req-1",
    );
    expect(body.intent).toBe("chore");
  });
});

describe("spikePayload name, request and attachments", () => {
  const base = { intent: "chore" as const, repos: [], fields };
  const build = (f: { name: string; request: string; images?: { name: string; blob: Blob }[] }) =>
    spikePayload({ ...base, images: [], ...f }, db.settings, db.catalog, "r");

  it("trims name and request", async () => {
    expect(await build({ name: "  Tidy deps  ", request: "  do it \n" })).toMatchObject({
      name: "Tidy deps",
      request: "do it",
    });
  });

  it("sends an empty name when the name is blank", async () => {
    const body = await build({ name: "   ", request: "Look into it" });
    expect(body.name).toBe("");
  });

  it("omits a blank request and attachments when there are no images", async () => {
    const body = await build({ name: "x", request: "  " });
    expect("request" in body).toBe(false);
    expect("attachments" in body).toBe(false);
  });

  it("sends plain base64 attachments when images are present", async () => {
    const png = new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a])], {
      type: "image/png",
    });
    const body = await build({
      name: "",
      request: "See shot",
      images: [{ name: "shot.png", blob: png }],
    });
    expect(body.attachments).toEqual([{ name: "shot.png", data: "iVBORw0K" }]);
  });

  it("sends a non-image file with its original name and bytes", async () => {
    const pdf = new Blob([new TextEncoder().encode("%PDF")], { type: "application/pdf" });
    const body = await build({ name: "", request: "", images: [{ name: "Spec.pdf", blob: pdf }] });
    expect(body.attachments).toEqual([{ name: "Spec.pdf", data: "JVBERg==" }]);
  });
});
