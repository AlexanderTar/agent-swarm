import { useId, useState } from "react";
import { C } from "../copy";
import {
  type AdvisorChoice, type AgentChoice, type Option, advisorOptions, agentOptions, catalogNote, changeAgent, changeModel,
  decodeAdvisor, effortOptions, encodeAdvisor, entryFor, modelOptions, resolveModel, validateChoice,
} from "../logic/catalog";
import type { AgentCatalogEntry, AgentKind, Settings, SettingsRole } from "../types";

export interface AgentFieldsValue { choice: AgentChoice; advisor: AdvisorChoice }

function Field(p: { label: string; value: string; options: Option[]; onChange(v: string): void; error?: string; note?: string }) {
  const id = useId();
  const known = p.options.some((o) => o.value === p.value);
  return (
    <div className="grid grid-cols-[72px_1fr] items-center gap-x-2">
      <label htmlFor={id}>{p.label}</label>
      <select id={id} value={p.value} onChange={(e) => p.onChange(e.target.value)} className="rounded border border-line bg-canvas px-2 py-1">
        {!known && <option value={p.value}>{p.value}</option>}
        {p.options.map((o) => (
          <option key={o.value} value={o.value} disabled={o.disabled}>{o.label}</option>
        ))}
      </select>
      {p.error ? <p className="col-start-2 text-bad">{p.error}</p> : null}
      {p.note ? <p className="col-start-2 text-muted">{p.note}</p> : null}
    </div>
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

  return (
    <div className="space-y-2">
      <Field
        label={C.agent}
        value={choice.agent}
        options={agentOptions(p.settings.enabled_agents)}
        error={errors.agent || undefined}
        onChange={(v) => {
          const r = changeAgent(choice, v as AgentKind, p.catalog);
          setAgentError(r.errors.model);
          setNote(undefined);
          p.onChange({ ...p.value, choice: r.choice });
        }}
      />
      <Field
        label={C.model}
        value={choice.model}
        options={modelOptions(entry)}
        error={agentError ?? (choice.model === "" ? undefined : errors.model)}
        onChange={(v) => {
          const r = changeModel(choice, v, p.catalog);
          setAgentError(undefined);
          setNote(r.note);
          p.onChange({ ...p.value, choice: r.choice });
        }}
      />
      {efforts && (
        <Field
          label={C.effort}
          value={choice.effort}
          options={efforts}
          note={note}
          onChange={(v) => {
            setNote(undefined);
            p.onChange({ ...p.value, choice: { ...choice, effort: v } });
          }}
        />
      )}
      {!efforts && note && <p className="text-muted">{note}</p>}
      <Field
        label={C.advisor}
        value={encodeAdvisor(advisor)}
        options={advisorOptions(p.catalog, p.settings.enabled_agents)}
        error={errors.advisor}
        onChange={(v) => p.onChange({ ...p.value, advisor: decodeAdvisor(v) })}
      />
      <p className="text-muted">{C.defaultsFromSettings}</p>
      {stale && <p className="text-warn">{stale}</p>}
    </div>
  );
}
