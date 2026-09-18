import { AGENT_LABEL, AGENT_LOGIN_CMD, C, T } from "../copy";
import type { AdvisorPayload, AgentCatalogEntry, AgentKind, CatalogModel, Settings, SettingsRole } from "../types";
import { ageLine } from "./format";

export interface Option { value: string; label: string; disabled?: boolean }
export interface AgentChoice { agent: AgentKind | ""; model: string; effort: string }
export type AdvisorChoice = { agent: AgentKind; model: string } | "none";
export interface FieldErrors { agent?: string; model?: string; advisor?: string }

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// catalog.DefaultLevel: the bare slug of a slug-encoded model (§6.7).
const DEFAULT_LEVEL = "default";

// The levels a user can actually pick. When "default" is also the model's default_effort, the
// daemon resolves it and "" to the same launch id, so offering both would be two identical rows.
const offeredEfforts = (model: CatalogModel): string[] =>
  model.default_effort === DEFAULT_LEVEL ? model.efforts.filter((e) => e !== DEFAULT_LEVEL) : model.efforts;

// The bare slug has no level name of its own, so it is named after the agent wherever it is shown.
const levelLabel = (kind: AgentKind, level: string) =>
  level === DEFAULT_LEVEL ? T.defaultLevel(AGENT_LABEL[kind]) : level;

export const entryFor = (catalog: AgentCatalogEntry[], kind: AgentKind | "") => catalog.find((e) => e.kind === kind);

export function resolveModel(entry: AgentCatalogEntry | undefined, value: string): CatalogModel | undefined {
  if (!entry || value === "") return undefined;
  return entry.models.find((m) => m.id === value) ?? entry.models.find((m) => m.aliases?.includes(value));
}

export function modelLabel(entry: AgentCatalogEntry | undefined, value: string): string {
  const m = resolveModel(entry, value);
  if (!m) return value;
  return m.id === value ? m.label : `${capitalize(value)} (latest)`;
}

export const agentOptions = (enabled: AgentKind[]): Option[] => enabled.map((k) => ({ value: k, label: AGENT_LABEL[k] }));

export function modelOptions(entry: AgentCatalogEntry | undefined, advisorOnly = false): Option[] {
  if (!entry) return [];
  const visible = entry.models.filter((m) => !m.hidden && (!advisorOnly || m.advisor_capable));
  const aliases = visible.flatMap((m) => (m.aliases ?? []).map((a) => ({ value: a, label: `${capitalize(a)} (latest)` })));
  // The offline alias fallback (§6.7) ships models whose id is their own alias, so the alias pass
  // and the full-name pass collide. One row per launch id, first occurrence wins, order preserved.
  const seen = new Set<string>();
  const out: Option[] = [];
  for (const o of [...aliases, ...visible.map((m) => ({ value: m.id, label: m.label }))]) {
    if (seen.has(o.value)) continue;
    seen.add(o.value);
    out.push(o);
  }
  return out;
}

export function defaultEffortLabel(kind: AgentKind, model: CatalogModel): string {
  if (model.default_effort === DEFAULT_LEVEL) return T.defaultLevel(AGENT_LABEL[kind]);
  if (model.default_effort !== "") return T.defaultLevel(model.default_effort);
  if (kind === "claude") return model.efforts.includes("high") ? T.defaultLevel("high") : C.defaultClaudeCode;
  // Belt and braces: every Go parser sets default_effort for a non-Claude agent, so this is unreachable.
  return T.defaultLevel(model.efforts.includes("high") ? "high" : (model.efforts.at(-1) ?? ""));
}

// §16.4: the model's own levels in the order the daemon ranked them, plus "Default ({level})" on top.
// Slug agents (§6.7) list a literal "default" level for the bare slug; it keeps the daemon's position
// and is named after the agent, and it drops out when "" already resolves to it.
export function effortOptions(kind: AgentKind | "", model: CatalogModel | undefined): Option[] | null {
  if (kind === "" || !model || model.efforts.length === 0) return null;
  return [
    { value: "", label: defaultEffortLabel(kind, model) },
    ...offeredEfforts(model).map((e) => ({ value: e, label: levelLabel(kind, e) })),
  ];
}

// The one seam for "is this level still offered?". A stored level the model's menu doesn't show
// means the agent default (L27): either the model dropped it, or it is a bare level that merged
// into the "" row. Consumers must reuse this rather than re-deriving the rule.
export const normalizeEffort = (kind: AgentKind | "", model: CatalogModel | undefined, effort: string): string =>
  effortOptions(kind, model)?.some((o) => o.value === effort) ? effort : "";

export function prefill(
  settings: Settings,
  role: SettingsRole = "orchestrator",
  catalog: AgentCatalogEntry[] = [],
): { choice: AgentChoice; advisor: AdvisorChoice } {
  const r = settings.roles[role];
  const a = settings.roles.advisor;
  // A stored level is only as good as the model it was set against. With a catalog that resolves
  // the stored model, re-validate it through the same seam every other consumer uses (progress.md:77
  // carry). Without a catalog the level is left alone rather than guessed away.
  const stored = r?.effort ?? "";
  const model = resolveModel(entryFor(catalog, r?.agent ?? ""), r?.model ?? "");
  const effort = model ? normalizeEffort(r?.agent ?? "", model, stored) : stored;
  return {
    choice: r
      ? { agent: r.agent, model: r.model, effort }
      : { agent: settings.enabled_agents[0] ?? "", model: "", effort: "" },
    advisor: a && a.model !== "none" ? { agent: a.agent, model: a.model } : "none", // contracts D-11
  };
}

export function changeAgent(choice: AgentChoice, agent: AgentKind, catalog: AgentCatalogEntry[]) {
  const model = resolveModel(entryFor(catalog, agent), choice.model);
  if (model) {
    const effort = normalizeEffort(agent, model, choice.effort);
    return { choice: { agent, model: choice.model, effort }, errors: {} as FieldErrors };
  }
  return { choice: { agent, model: "", effort: "" }, errors: { model: C.modelUnavailable } as FieldErrors };
}

export function changeModel(choice: AgentChoice, model: string, catalog: AgentCatalogEntry[]): { choice: AgentChoice; note?: string } {
  const m = resolveModel(entryFor(catalog, choice.agent), model);
  const next = { ...choice, model, effort: normalizeEffort(choice.agent, m, choice.effort) };
  // Silent only when the bare level merged into the NEW model's "" row: it launches the same way.
  // A target with no bare level at all really did lose the choice, so it gets the note.
  const merged = choice.effort === DEFAULT_LEVEL && m?.default_effort === DEFAULT_LEVEL;
  if (next.effort === "" && choice.effort !== "" && !merged && m && choice.agent !== "") {
    return { choice: next, note: T.effortUnavailable(levelLabel(choice.agent, choice.effort), m.label) };
  }
  return { choice: next };
}

export function validateChoice(
  choice: AgentChoice,
  advisor: AdvisorChoice,
  catalog: AgentCatalogEntry[],
  enabled: AgentKind[],
  role: SettingsRole,
): FieldErrors {
  if (choice.agent === "" || !enabled.includes(choice.agent)) return { agent: C.chooseAgent };
  const errors: FieldErrors = {};
  const entry = entryFor(catalog, choice.agent);
  const name = AGENT_LABEL[choice.agent];
  if (!entry?.installed) errors.agent = T.agentNotInstalled(name);
  else if (!entry.auth_ok) errors.agent = T.agentNotSignedIn(name, AGENT_LOGIN_CMD[choice.agent]);
  else if (role === "orchestrator" && !entry.superpowers) errors.agent = T.superpowersMissing(name);
  if (choice.model === "") errors.model = C.modelUnavailable;
  else if (!resolveModel(entry, choice.model)) errors.model = T.modelGone(choice.model, name);
  if (advisor !== "none" && !resolveModel(entryFor(catalog, advisor.agent), advisor.model)) {
    errors.advisor = T.modelGone(advisor.model, AGENT_LABEL[advisor.agent]);
  }
  return errors;
}

export const isValid = (e: FieldErrors) => Object.keys(e).length === 0;

export function catalogNote(entry: AgentCatalogEntry | undefined, now = Date.now()): string | undefined {
  if (!entry?.catalog_stale) return undefined;
  const never = entry.catalog_error ? T.catalogNeverFetched(entry.catalog_error) : C.neverFetched;
  return ageLine(entry.catalog_fetched_at, never, (age) => T.catalogStale(age, entry.catalog_error), now);
}

export function advisorOptions(catalog: AgentCatalogEntry[], enabled: AgentKind[]): Option[] {
  const out: Option[] = [];
  for (const kind of enabled) {
    for (const o of modelOptions(entryFor(catalog, kind), kind === "claude")) {
      out.push({ value: `${kind}:${o.value}`, label: `${AGENT_LABEL[kind]} · ${o.label}` });
    }
  }
  return [...out, { value: "none", label: C.noAdvisor }];
}

export const encodeAdvisor = (a: AdvisorChoice) => (a === "none" ? "none" : `${a.agent}:${a.model}`);

export function decodeAdvisor(v: string): AdvisorChoice {
  if (v === "none") return "none";
  const i = v.indexOf(":");
  return { agent: v.slice(0, i) as AgentKind, model: v.slice(i + 1) };
}

export function choicePayload(c: AgentChoice): { agent: AgentKind; model: string; effort?: string } {
  const base = { agent: c.agent as AgentKind, model: c.model };
  return c.effort ? { ...base, effort: c.effort } : base;
}

// §16.3: the advisor's effort comes from Settings — but Settings only validated that level against
// the Settings advisor model, so re-check it against the model the user actually picked (the
// daemon's SupportsEffort rule). A level that model doesn't offer is left out entirely: the daemon
// substitutes its own default before the launch-id lookup, so the launch id is the same either way.
export function advisorPayload(a: AdvisorChoice, settings: Settings, catalog: AgentCatalogEntry[]): AdvisorPayload {
  if (a === "none") return "none";
  const model = resolveModel(entryFor(catalog, a.agent), a.model);
  const effort = normalizeEffort(a.agent, model, settings.roles.advisor?.effort ?? "");
  return effort ? { ...a, effort } : { ...a };
}
