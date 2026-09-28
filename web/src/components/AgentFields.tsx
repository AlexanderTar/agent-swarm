import { useState } from "react";
import { ChevronDown } from "lucide-react";
import { C, ROLE_LABEL } from "../copy";
import { Button } from "./ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./ui/collapsible";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import {
  type AdvisorChoice, type AgentChoice, type Option, advisorAgentOptions, advisorModelOptions, agentOptions,
  catalogNote, changeAdvisorAgent, changeAgent, changeModel, effortOptions, entryFor, modelOptions,
  normalizeEffort, resolveModel, validateChoice,
} from "../logic/catalog";
import type { AgentCatalogEntry, AgentKind, RoleDefault, Settings, SettingsRole } from "../types";

export interface AgentFieldsValue {
  choice: AgentChoice;
  advisor: AdvisorChoice;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}

const WORKER_ROLES: SettingsRole[] = ["coder", "reviewer", "ui_reviewer", "designer", "researcher", "debugger", "mechanical"];
const ROW = "grid grid-cols-[72px_150px_52px_minmax(0,1fr)] items-center gap-x-2 gap-y-1";
const DEFAULT = "__default";

function Pick(p: { label: string; value: string; options: Option[]; onChange(v: string): void; disabled?: boolean; placeholder?: string }) {
  const known = p.options.some((o) => o.value === p.value);
  return (
    <Select value={p.disabled ? "" : p.value || DEFAULT} onValueChange={(v) => p.onChange(v === DEFAULT ? "" : v)} disabled={p.disabled}>
      <SelectTrigger aria-label={p.label} className="w-full"><SelectValue placeholder={p.placeholder} /></SelectTrigger>
      <SelectContent>
        {!known && p.value && <SelectItem value={p.value}>{p.value}</SelectItem>}
        {p.options.map((o) => <SelectItem key={o.value} value={o.value || DEFAULT} disabled={o.disabled}>{o.label}</SelectItem>)}
      </SelectContent>
    </Select>
  );
}

export function AgentFields(p: {
  value: AgentFieldsValue;
  onChange(v: AgentFieldsValue): void;
  settings: Settings;
  catalog: AgentCatalogEntry[];
  role?: SettingsRole;
}) {
  const { choice, advisor } = p.value;
  const [agentError, setAgentError] = useState<string | undefined>();
  const [note, setNote] = useState<string | undefined>();
  const entry = entryFor(p.catalog, choice.agent);
  const model = resolveModel(entry, choice.model);
  const efforts = effortOptions(choice.agent, model);
  const errors = validateChoice(choice, advisor, p.catalog, p.settings.enabled_agents, p.role ?? "orchestrator");
  const stale = catalogNote(entry);

  const updateRole = (role: SettingsRole, nextAgent: AgentKind, nextModel: string, nextEffort?: string) => {
    const roleDef: RoleDefault = nextEffort ? { agent: nextAgent, model: nextModel, effort: nextEffort } : { agent: nextAgent, model: nextModel };
    p.onChange({ ...p.value, roles: { ...p.value.roles, [role]: roleDef } });
  };

  return (
    <div className="space-y-2">
      <div className={ROW}>
        <Label>{C.agent}</Label>
        <Pick label={C.agent} value={choice.agent} options={agentOptions(p.settings.enabled_agents)} onChange={(v) => {
          const r = changeAgent(choice, v as AgentKind, p.catalog);
          setAgentError(r.errors.model);
          setNote(undefined);
          p.onChange({ ...p.value, choice: r.choice });
        }} />
        <Label>{C.model}</Label>
        <Pick label={C.model} value={choice.model} options={modelOptions(entry)} onChange={(v) => {
          const r = changeModel(choice, v, p.catalog);
          setAgentError(undefined);
          setNote(r.note);
          p.onChange({ ...p.value, choice: r.choice });
        }} />
        {(errors.agent || agentError || (choice.model ? errors.model : undefined)) && <p className="col-start-2 col-end-5 text-xs text-destructive">{errors.agent || agentError || errors.model}</p>}
        {efforts && <>
          <Label>{C.effort}</Label><span className="col-span-2" />
          <Pick label={C.effort} value={choice.effort} options={efforts} onChange={(v) => { setNote(undefined); p.onChange({ ...p.value, choice: { ...choice, effort: v } }); }} />
        </>}
        {note && <p className="col-start-4 text-xs text-muted-foreground">{note}</p>}
        <Label>{C.advisor}</Label>
        <Pick label={C.advisor} value={advisor === "none" ? "none" : advisor.agent} options={advisorAgentOptions(p.settings.enabled_agents)} onChange={(v) => p.onChange({ ...p.value, advisor: changeAdvisorAgent(v as AgentKind | "none", p.settings, p.catalog) })} />
        <Label>{C.model}</Label>
        <Pick label={C.advisorModel} value={advisor === "none" ? "" : advisor.model} placeholder="—" disabled={advisor === "none"} options={advisor === "none" ? [] : advisorModelOptions(p.catalog, advisor.agent)} onChange={(m) => advisor !== "none" && p.onChange({ ...p.value, advisor: { agent: advisor.agent, model: m } })} />
        {errors.advisor && <p className="col-start-2 col-end-5 text-xs text-destructive">{errors.advisor}</p>}
      </div>
      <p className="text-xs text-muted-foreground">{C.defaultsFromSettings}</p>
      {stale && <p className="text-xs text-warning">{stale}</p>}
      <Collapsible className="rounded-md border border-border p-2">
        <CollapsibleTrigger asChild><Button variant="ghost" size="sm" className="group"><ChevronDown className="size-4 transition-transform group-data-[state=open]:rotate-180" />{C.workerRoles}</Button></CollapsibleTrigger>
        <CollapsibleContent className="mt-2 space-y-4">
          <p className="text-xs text-muted-foreground">{C.customizeWorkerRoles}</p>
          {WORKER_ROLES.map((role) => {
            const current = p.value.roles?.[role] ?? p.settings.roles[role];
            const roleAgent: AgentKind = current?.agent ?? p.settings.enabled_agents[0] ?? "claude";
            const roleModel = current?.model ?? "";
            const roleEffort = current?.effort ?? "";
            const roleEntry = entryFor(p.catalog, roleAgent);
            const roleEfforts = effortOptions(roleAgent, resolveModel(roleEntry, roleModel));
            return (
              <fieldset key={role} aria-label={ROLE_LABEL[role]} className={`${ROW} border-t border-border pt-2`}>
                <legend className="col-span-4 text-sm font-semibold">{ROLE_LABEL[role]}</legend>
                <Label>{C.agent}</Label>
                <Pick label={`${ROLE_LABEL[role]} ${C.agent}`} value={roleAgent} options={agentOptions(p.settings.enabled_agents)} onChange={(v) => {
                  const newAgent = v as AgentKind;
                  const newEntry = entryFor(p.catalog, newAgent);
                  const nextModel = resolveModel(newEntry, roleModel) ? roleModel : (newEntry?.default_model ?? newEntry?.models[0]?.id ?? "");
                  const nextEffort = normalizeEffort(newAgent, resolveModel(newEntry, nextModel), roleEffort);
                  updateRole(role, newAgent, nextModel, nextEffort || undefined);
                }} />
                <Label>{C.model}</Label>
                <Pick label={`${ROLE_LABEL[role]} ${C.model}`} value={roleModel} options={modelOptions(roleEntry)} onChange={(v) => updateRole(role, roleAgent, v, normalizeEffort(roleAgent, resolveModel(roleEntry, v), roleEffort) || undefined)} />
                {roleEfforts && <>
                  <Label>{C.effort}</Label><span className="col-span-2" />
                  <Pick label={`${ROLE_LABEL[role]} ${C.effort}`} value={roleEffort} options={roleEfforts} onChange={(v) => updateRole(role, roleAgent, roleModel, v || undefined)} />
                </>}
              </fieldset>
            );
          })}
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}
