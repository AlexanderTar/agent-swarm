import { AGENT_LABEL, AGENT_LOGIN_CMD, C, T } from "../copy";
import type { AdvisorPayload, AgentCatalogEntry, AgentKind, CatalogModel, Settings, SettingsRole } from "../types";
import { ageCompact } from "./format";

export interface Option { value: string; label: string; disabled?: boolean }
export interface AgentChoice { agent: AgentKind | ""; model: string; effort: string }
export type AdvisorChoice = { agent: AgentKind; model: string } | "none";
export interface FieldErrors { agent?: string; model?: string; advisor?: string }

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

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
  return [...aliases, ...visible.map((m) => ({ value: m.id, label: m.label }))];
}

export function defaultEffortLabel(kind: AgentKind, model: CatalogModel): string {
  if (model.default_effort !== "") return T.defaultLevel(model.default_effort);
  if (kind === "claude") return model.efforts.includes("high") ? T.defaultLevel("high") : C.defaultClaudeCode;
  return T.defaultLevel(model.efforts.includes("high") ? "high" : (model.efforts.at(-1) ?? ""));
}

// §16.4: the model's own levels in the order the daemon ranked them, plus "Default ({level})" on top.
// Slug agents (§6.7) list a literal "default" level for the bare slug; it stays where the daemon put it.
export function effortOptions(kind: AgentKind | "", model: CatalogModel | undefined): Option[] | null {
  if (kind === "" || !model || model.efforts.length === 0) return null;
  return [{ value: "", label: defaultEffortLabel(kind, model) }, ...model.efforts.map((e) => ({ value: e, label: e }))];
}

export function prefill(settings: Settings, role: SettingsRole = "orchestrator"): { choice: AgentChoice; advisor: AdvisorChoice } {
  const r = settings.roles[role];
  const a = settings.roles.advisor;
  return {
    choice: r
      ? { agent: r.agent, model: r.model, effort: r.effort ?? "" }
      : { agent: settings.enabled_agents[0] ?? "", model: "", effort: "" },
    advisor: a && a.model !== "none" ? { agent: a.agent, model: a.model } : "none", // contracts D-11
  };
}

export function changeAgent(choice: AgentChoice, agent: AgentKind, catalog: AgentCatalogEntry[]) {
  const model = resolveModel(entryFor(catalog, agent), choice.model);
  if (model) {
    const effort = model.efforts.includes(choice.effort) ? choice.effort : "";
    return { choice: { agent, model: choice.model, effort }, errors: {} as FieldErrors };
  }
  return { choice: { agent, model: "", effort: "" }, errors: { model: C.modelUnavailable } as FieldErrors };
}

export function changeModel(choice: AgentChoice, model: string, catalog: AgentCatalogEntry[]): { choice: AgentChoice; note?: string } {
  const m = resolveModel(entryFor(catalog, choice.agent), model);
  if (choice.effort !== "" && !m?.efforts.includes(choice.effort)) {
    const next = { ...choice, model, effort: "" };
    return m ? { choice: next, note: T.effortUnavailable(choice.effort, m.label) } : { choice: next };
  }
  return { choice: { ...choice, model } };
}

export function validateChoice(
  choice: AgentChoice,
  advisor: AdvisorChoice,
  catalog: AgentCatalogEntry[],
  enabled: AgentKind[],
  role: SettingsRole,
): FieldErrors {
  if (choice.agent === "" || !enabled.includes(choice.agent)) return { agent: "" };
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
  return T.catalogStale(ageCompact(entry.catalog_fetched_at, now), entry.catalog_error);
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

export function advisorPayload(a: AdvisorChoice, settings: Settings): AdvisorPayload {
  if (a === "none") return "none";
  const effort = settings.roles.advisor?.effort;
  return effort ? { ...a, effort } : { ...a };
}
