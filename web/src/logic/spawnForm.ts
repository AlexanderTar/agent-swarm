import { ApiError, errorText } from "../api";
import type { AgentFieldsValue } from "../components/AgentFields";
import { C } from "../copy";
import type { AgentCatalogEntry, AgentNode, CreateSpikeBody, Item, Settings, StartOrchestratorBody } from "../types";
import { flattenAgents } from "./agentActions";
import { advisorPayload, choicePayload } from "./catalog";
import { kebab } from "./kebab";

export function orchestratorsBusy(agents: AgentNode[], settings: Settings): boolean {
  const live = flattenAgents(agents).filter((a) => a.role === "orchestrator" && (a.state === "queued" || a.state === "active"));
  return live.length >= settings.max_orchestrators;
}

export const submitLabel = (busy: boolean) => (busy ? C.queueOrchestrator : C.startOrchestrator);

export const nameError = (name: string) => (kebab(name) === "" ? C.nameEmpty : undefined);

export interface SubmitFailure { name?: string; banner?: string; detail?: string }

export function mapSubmitError(e: unknown): SubmitFailure {
  if (e instanceof ApiError) {
    // Standing rule: route every daemon error surface through errorText (reason ?? message), not a
    // raw e.message — the daemon's current name/preflight refusals never set `reason` so this is
    // behaviorally identical today, but it stops working silently the day one does.
    const msg = errorText(e);
    if (msg === C.nameTaken || msg === C.nameEmpty) return { name: msg };
    return { banner: C.launchFailure, detail: msg };
  }
  return { banner: C.launchFailure };
}

export interface SpawnFormState { name: string; repos: string[]; fields: AgentFieldsValue }
export interface SpikeFormState extends SpawnFormState { intent: "feature" | "debug"; request: string }

// Deviation from the brief: advisorPayload (logic/catalog.ts) takes a `catalog` argument in this
// codebase -- normalizing the advisor's stored effort against the model the user actually has
// selected, per its own doc comment. The brief's orchestratorPayload/spikePayload signatures had no
// catalog parameter and called advisorPayload with only 2 args, which doesn't typecheck against the
// real 3-arg function. Threading catalog through here (rather than hardcoding `[]` inside this file)
// keeps a real, non-empty settings.roles.advisor.effort from being silently dropped on every submit.
export function orchestratorPayload(f: SpawnFormState, item: Item, settings: Settings, catalog: AgentCatalogEntry[], requestId: string): StartOrchestratorBody {
  return {
    request_id: requestId,
    ...choicePayload(f.fields.choice),
    advisor: advisorPayload(f.fields.advisor, settings, catalog),
    repos: f.repos,
    repos_version: item.repos_version,
    name: f.name,
  };
}

export function spikePayload(f: SpikeFormState, settings: Settings, catalog: AgentCatalogEntry[], requestId: string): CreateSpikeBody {
  const body: CreateSpikeBody = {
    request_id: requestId,
    name: f.name,
    intent: f.intent,
    repos: f.repos,
    ...choicePayload(f.fields.choice),
    advisor: advisorPayload(f.fields.advisor, settings, catalog),
  };
  return f.request.trim() ? { ...body, request: f.request } : body;
}

export const newRequestId = () => crypto.randomUUID();
