import { describe, expect, it } from "vitest";
import type { AgentCatalogEntry, CatalogModel, Settings } from "../types";
import {
  advisorOptions, advisorPayload, agentOptions, catalogNote, changeAgent, changeModel, choicePayload, decodeAdvisor,
  defaultEffortLabel, effortOptions, encodeAdvisor, isValid, modelLabel, modelOptions, prefill, resolveModel, validateChoice,
} from "./catalog";

// L26/L27: no real model names anywhere in src/logic, fixtures included.
const m = (p: Partial<CatalogModel> & { id: string }): CatalogModel => ({
  label: p.id, efforts: [], default_effort: "", effort_encoding: "flag", advisor_capable: false, ...p,
});

const entry = (p: Partial<AgentCatalogEntry> & Pick<AgentCatalogEntry, "kind" | "models">): AgentCatalogEntry => ({
  installed: true, version: "1", auth_ok: true, auth_error: "", superpowers: true, default_model: "",
  catalog_source: "test", catalog_fetched_at: 0, catalog_stale: false, catalog_error: "", ...p,
});

const catalog: AgentCatalogEntry[] = [
  entry({
    kind: "claude",
    models: [
      m({ id: "model-1", label: "Model One", aliases: ["alpha"], efforts: ["low", "medium", "high", "xhigh", "max"], advisor_capable: true }),
      m({ id: "model-2", label: "Model Two", efforts: ["low", "medium", "high"], advisor_capable: true }),
      m({ id: "model-3", label: "Model Three", aliases: ["gamma"] }),
      m({ id: "model-4", label: "Model Four", hidden: true }),
      // The offline alias fallback (§6.7) ships models whose id IS their alias.
      m({ id: "delta", label: "Delta (latest)", aliases: ["delta"], efforts: ["low", "high"], advisor_capable: true }),
    ],
  }),
  entry({
    kind: "codex",
    models: [m({ id: "model-5", label: "Model Five", efforts: ["low", "medium", "high"], default_effort: "medium" })],
  }),
  // Slug-encoded agent (§6.7): "default" is the bare slug's level and the daemon lists it first.
  entry({
    kind: "cursor",
    models: [
      m({
        id: "model-6", label: "Model Six", efforts: ["default", "low", "high"], default_effort: "high",
        effort_encoding: "slug", launch_ids: { default: "model-6", low: "model-6-low", high: "model-6-high" },
      }),
      m({
        id: "model-7", label: "Model Seven", efforts: ["default", "medium"], default_effort: "medium",
        effort_encoding: "slug", launch_ids: { default: "model-7", medium: "model-7-medium" },
      }),
      // D2 fell through to the bare slug, so "" and "default" resolve to the same launch id.
      m({
        id: "model-8", label: "Model Eight", efforts: ["default", "none", "minimal"], default_effort: "default",
        effort_encoding: "slug", launch_ids: { default: "model-8", none: "model-8-none", minimal: "model-8-minimal" },
      }),
    ],
  }),
];

const settings = {
  enabled_agents: ["claude", "codex"],
  roles: {
    orchestrator: { agent: "claude", model: "alpha" },
    advisor: { agent: "claude", model: "model-2", effort: "high" },
  },
} as unknown as Settings;

describe("catalog rules (§16.3, §16.4, L26–L28)", () => {
  it("resolves aliases and lists aliases first without hidden models", () => {
    expect(resolveModel(catalog[0], "alpha")?.id).toBe("model-1");
    expect(modelLabel(catalog[0], "alpha")).toBe("Alpha (latest)");
    expect(modelLabel(catalog[0], "model-3")).toBe("Model Three");
    expect(modelLabel(undefined, "zzz")).toBe("zzz");
    expect(modelOptions(catalog[0]).map((o) => o.label)).toEqual([
      "Alpha (latest)", "Gamma (latest)", "Delta (latest)", "Model One", "Model Two", "Model Three",
    ]);
    expect(modelOptions(catalog[0], true).map((o) => o.value)).toEqual(["alpha", "delta", "model-1", "model-2"]);
    expect(modelOptions(undefined)).toEqual([]);
    expect(agentOptions(["claude", "agy"])).toEqual([{ value: "claude", label: "Claude" }, { value: "agy", label: "agy" }]);
  });

  it("offers a model whose id is its own alias exactly once", () => {
    // §6.7 ClaudeAliasFallback: the alias pass and the full-name pass emit the same launch id.
    const rows = modelOptions(catalog[0]);
    expect(rows.filter((o) => o.value === "delta")).toEqual([{ value: "delta", label: "Delta (latest)" }]);
    expect(new Set(rows.map((o) => o.value)).size).toBe(rows.length);
    const advisor = advisorOptions(catalog, ["claude"]);
    expect(new Set(advisor.map((o) => o.value)).size).toBe(advisor.length);
  });

  it("labels the default effort", () => {
    expect(defaultEffortLabel("claude", m({ id: "model-8", efforts: ["low", "high"] }))).toBe("Default (high)");
    expect(defaultEffortLabel("claude", m({ id: "model-8", efforts: ["low", "medium"] }))).toBe("Default (Claude Code)");
    expect(defaultEffortLabel("codex", m({ id: "model-8", efforts: ["low", "medium"], default_effort: "medium" }))).toBe("Default (medium)");
    expect(defaultEffortLabel("agy", m({ id: "model-8", efforts: ["low", "high"] }))).toBe("Default (high)");
  });

  it("offers effort levels or null for models without effort", () => {
    expect(effortOptions("codex", resolveModel(catalog[1], "model-5"))).toEqual([
      { value: "", label: "Default (medium)" }, { value: "low", label: "low" }, { value: "medium", label: "medium" }, { value: "high", label: "high" },
    ]);
    expect(effortOptions("claude", resolveModel(catalog[0], "gamma"))).toBeNull();
    expect(effortOptions("claude", undefined)).toBeNull();
  });

  it("names the bare `default` level after the agent and drops it when it is the model's default", () => {
    // Worked example 1: the level is a real, distinct choice, so it stays and reads "Default (<Agent>)".
    expect(effortOptions("cursor", m({
      id: "model-9", efforts: ["default", "low", "medium", "high"], default_effort: "medium", effort_encoding: "slug",
    }))).toEqual([
      { value: "", label: "Default (medium)" },
      { value: "default", label: "Default (Cursor)" },
      { value: "low", label: "low" },
      { value: "medium", label: "medium" },
      { value: "high", label: "high" },
    ]);
    // Worked example 2: "" already resolves to the bare slug, so the redundant row is dropped.
    expect(effortOptions("cursor", resolveModel(catalog[2], "model-8"))).toEqual([
      { value: "", label: "Default (Cursor)" },
      { value: "none", label: "none" },
      { value: "minimal", label: "minimal" },
    ]);
  });

  it("normalises a stored `default` that the model no longer offers", () => {
    const stored = (model: string) => ({
      enabled_agents: ["cursor"],
      roles: { orchestrator: { agent: "cursor", model, effort: "default" } },
    } as unknown as Settings);
    expect(prefill(stored("model-8"), "orchestrator", catalog).choice.effort).toBe("");
    expect(prefill(stored("model-6"), "orchestrator", catalog).choice.effort).toBe("default");
    expect(changeModel({ agent: "cursor", model: "model-6", effort: "default" }, "model-8", catalog).choice.effort).toBe("");
    expect(changeAgent({ agent: "claude", model: "model-8", effort: "default" }, "cursor", catalog).choice.effort).toBe("");
  });

  it("keeps the daemon's level order for slug models, including the bare `default` level", () => {
    // R7/R8: only slug agents carry a "default" level and the daemon already ranks it first.
    expect(effortOptions("cursor", resolveModel(catalog[2], "model-6"))).toEqual([
      { value: "", label: "Default (high)" },
      { value: "default", label: "Default (Cursor)" },
      { value: "low", label: "low" },
      { value: "high", label: "high" },
    ]);
    expect(changeModel({ agent: "cursor", model: "model-7", effort: "default" }, "model-6", catalog)).toEqual({
      choice: { agent: "cursor", model: "model-6", effort: "default" },
    });
    expect(changeModel({ agent: "cursor", model: "model-6", effort: "high" }, "model-7", catalog)).toEqual({
      choice: { agent: "cursor", model: "model-7", effort: "" },
      note: "high isn't available for Model Seven; using the default.",
    });
    // The level travels to the daemon untouched; launch_ids resolution is the daemon's job.
    expect(choicePayload({ agent: "cursor", model: "model-6", effort: "default" })).toEqual({
      agent: "cursor", model: "model-6", effort: "default",
    });
  });

  it("prefills from Settings", () => {
    expect(prefill(settings)).toEqual({
      choice: { agent: "claude", model: "alpha", effort: "" },
      advisor: { agent: "claude", model: "model-2" },
    });
    const bare = { enabled_agents: ["codex"], roles: {} } as unknown as Settings;
    expect(prefill(bare)).toEqual({ choice: { agent: "codex", model: "", effort: "" }, advisor: "none" });
    const noAdvisor = { ...settings, roles: { ...settings.roles, advisor: { agent: "claude", model: "none" } } } as Settings;
    expect(prefill(noAdvisor).advisor).toBe("none");
  });

  it("changing agent re-checks the model and never substitutes", () => {
    const r = changeAgent({ agent: "claude", model: "alpha", effort: "high" }, "codex", catalog);
    expect(r).toEqual({ choice: { agent: "codex", model: "", effort: "" }, errors: { model: "Choose a model available for this agent." } });
    expect(changeAgent({ agent: "codex", model: "model-1", effort: "max" }, "claude", catalog)).toEqual({
      choice: { agent: "claude", model: "model-1", effort: "max" }, errors: {},
    });
    expect(changeAgent({ agent: "codex", model: "model-2", effort: "xhigh" }, "claude", catalog).choice.effort).toBe("");
  });

  it("changing model keeps a supported level and otherwise resets with a note", () => {
    expect(changeModel({ agent: "claude", model: "model-1", effort: "high" }, "model-2", catalog)).toEqual({
      choice: { agent: "claude", model: "model-2", effort: "high" },
    });
    expect(changeModel({ agent: "claude", model: "model-1", effort: "xhigh" }, "model-2", catalog)).toEqual({
      choice: { agent: "claude", model: "model-2", effort: "" },
      note: "xhigh isn't available for Model Two; using the default.",
    });
    expect(changeModel({ agent: "claude", model: "model-1", effort: "" }, "model-3", catalog)).toEqual({
      choice: { agent: "claude", model: "model-3", effort: "" },
    });
  });

  it("validates agent state, model presence and the advisor", () => {
    const ok = { agent: "claude", model: "alpha", effort: "" } as const;
    expect(validateChoice(ok, "none", catalog, ["claude"], "orchestrator")).toEqual({});
    expect(isValid({})).toBe(true);
    expect(validateChoice({ ...ok, model: "" }, "none", catalog, ["claude"], "orchestrator")).toEqual({ model: "Choose a model available for this agent." });
    expect(validateChoice({ ...ok, model: "gone-1" }, "none", catalog, ["claude"], "orchestrator")).toEqual({ model: "gone-1 is no longer offered by Claude." });
    expect(validateChoice(ok, { agent: "claude", model: "gone-2" }, catalog, ["claude"], "orchestrator")).toEqual({ advisor: "gone-2 is no longer offered by Claude." });
    const broken = [entry({ kind: "claude", models: catalog[0]!.models, installed: false })];
    expect(validateChoice(ok, "none", broken, ["claude"], "orchestrator")).toEqual({ agent: "Claude isn't installed on this Mac." });
    const signedOut = [entry({ kind: "claude", models: catalog[0]!.models, auth_ok: false })];
    expect(validateChoice(ok, "none", signedOut, ["claude"], "orchestrator")).toEqual({ agent: "Claude isn't signed in. Run `claude` in a terminal." });
    const noSp = [entry({ kind: "claude", models: catalog[0]!.models, superpowers: false })];
    expect(validateChoice(ok, "none", noSp, ["claude"], "orchestrator")).toEqual({ agent: "Install the superpowers plugin for Claude to run orchestrators." });
    expect(validateChoice(ok, "none", noSp, ["claude"], "coder")).toEqual({});
    expect(isValid(validateChoice({ agent: "", model: "", effort: "" }, "none", catalog, ["claude"], "orchestrator"))).toBe(false);
  });

  it("notes a stale catalog", () => {
    const stale = entry({ kind: "claude", models: [], catalog_stale: true, catalog_error: "timeout", catalog_fetched_at: 0 });
    expect(catalogNote(stale, 3 * 3_600_000)).toBe("Model list from 3h ago. Couldn't refresh: timeout");
    expect(catalogNote(catalog[0])).toBeUndefined();
  });

  it("re-checks the Settings advisor effort against the chosen advisor model", () => {
    // Settings guarantees the level only for the Settings model; the user may pick another pair.
    expect(advisorPayload({ agent: "claude", model: "gamma" }, settings, catalog)).toEqual({ agent: "claude", model: "gamma" });
    expect(advisorPayload({ agent: "cursor", model: "model-7" }, settings, catalog)).toEqual({
      agent: "cursor", model: "model-7", effort: "medium",
    });
    expect(advisorPayload({ agent: "claude", model: "gone-2" }, settings, catalog)).toEqual({ agent: "claude", model: "gone-2" });
  });

  it("builds advisor options, codecs and payloads", () => {
    expect(advisorOptions(catalog, ["claude", "codex"]).map((o) => o.label)).toEqual([
      "Claude · Alpha (latest)", "Claude · Delta (latest)", "Claude · Model One", "Claude · Model Two",
      "Codex · Model Five", "No advisor",
    ]);
    expect(encodeAdvisor({ agent: "claude", model: "alpha" })).toBe("claude:alpha");
    expect(encodeAdvisor("none")).toBe("none");
    expect(decodeAdvisor("codex:model-5")).toEqual({ agent: "codex", model: "model-5" });
    expect(decodeAdvisor("none")).toBe("none");
    expect(advisorPayload({ agent: "claude", model: "alpha" }, settings, catalog)).toEqual({ agent: "claude", model: "alpha", effort: "high" });
    expect(advisorPayload("none", settings, catalog)).toBe("none");
    expect(choicePayload({ agent: "claude", model: "alpha", effort: "" })).toEqual({ agent: "claude", model: "alpha" });
    expect(choicePayload({ agent: "codex", model: "model-5", effort: "high" })).toEqual({ agent: "codex", model: "model-5", effort: "high" });
  });
});
