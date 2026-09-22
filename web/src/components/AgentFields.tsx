import { useId, useState } from "react";
import { C, ROLE_LABEL } from "../copy";
import {
  type AdvisorChoice, type AgentChoice, type Option, advisorOptions, agentOptions, catalogNote, changeAgent, changeModel,
  decodeAdvisor, effortOptions, encodeAdvisor, entryFor, modelOptions, normalizeEffort, resolveModel, validateChoice,
} from "../logic/catalog";
import type { AgentCatalogEntry, AgentKind, RoleDefault, Settings, SettingsRole } from "../types";

export interface AgentFieldsValue {
  choice: AgentChoice;
  advisor: AdvisorChoice;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}

const WORKER_ROLES: SettingsRole[] = [
  "coder",
  "reviewer",
  "ui_reviewer",
  "researcher",
  "debugger",
  "mechanical",
];

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
  const [openRoles, setOpenRoles] = useState(false);
  const entry = entryFor(p.catalog, choice.agent);
  const model = resolveModel(entry, choice.model);
  const efforts = effortOptions(choice.agent, model);
  const errors = validateChoice(choice, advisor, p.catalog, p.settings.enabled_agents, p.role ?? "orchestrator");
  const stale = catalogNote(entry);

  const updateRole = (role: SettingsRole, nextAgent: AgentKind, nextModel: string, nextEffort?: string) => {
    const roleDef: RoleDefault = nextEffort
      ? { agent: nextAgent, model: nextModel, effort: nextEffort }
      : { agent: nextAgent, model: nextModel };
    p.onChange({
      ...p.value,
      roles: {
        ...p.value.roles,
        [role]: roleDef,
      },
    });
  };

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

      <details
        open={openRoles}
        onToggle={(e) => setOpenRoles(e.currentTarget.open)}
        className="rounded border border-line p-2"
      >
        <summary
          className="cursor-pointer text-sm font-medium"
          onClick={(e) => {
            e.preventDefault();
            setOpenRoles((o) => !o);
          }}
        >
          {C.workerRoles}
        </summary>
        {openRoles && (
          <div className="mt-2 space-y-4">
            <p className="text-xs text-muted">{C.customizeWorkerRoles}</p>
            {WORKER_ROLES.map((role) => {
              const current = p.value.roles?.[role] ?? p.settings.roles[role];
              const roleAgent: AgentKind = current?.agent ?? p.settings.enabled_agents[0] ?? "claude";
              const roleModel: string = current?.model ?? "";
              const roleEffort: string = current?.effort ?? "";
              const roleEntry = entryFor(p.catalog, roleAgent);
              const m = resolveModel(roleEntry, roleModel);
              const roleEfforts = effortOptions(roleAgent, m);

              return (
                <fieldset key={role} aria-label={ROLE_LABEL[role]} className="space-y-2 border-t border-line/50 pt-2">
                  <legend className="text-sm font-semibold">{ROLE_LABEL[role]}</legend>
                  <Field
                    label={C.agent}
                    value={roleAgent}
                    options={agentOptions(p.settings.enabled_agents)}
                    onChange={(v) => {
                      const newAgent = v as AgentKind;
                      const newEntry = entryFor(p.catalog, newAgent);
                      const validModel = resolveModel(newEntry, roleModel);
                      const nextModel = validModel ? roleModel : (newEntry?.default_model ?? newEntry?.models[0]?.id ?? "");
                      const nextEffort = normalizeEffort(newAgent, resolveModel(newEntry, nextModel), roleEffort);
                      updateRole(role, newAgent, nextModel, nextEffort || undefined);
                    }}
                  />
                  <Field
                    label={C.model}
                    value={roleModel}
                    options={modelOptions(roleEntry)}
                    onChange={(v) => {
                      const newModelObj = resolveModel(roleEntry, v);
                      const nextEffort = normalizeEffort(roleAgent, newModelObj, roleEffort);
                      updateRole(role, roleAgent, v, nextEffort || undefined);
                    }}
                  />
                  {roleEfforts && (
                    <Field
                      label={C.effort}
                      value={roleEffort}
                      options={roleEfforts}
                      onChange={(v) => {
                        updateRole(role, roleAgent, roleModel, v || undefined);
                      }}
                    />
                  )}
                </fieldset>
              );
            })}
          </div>
        )}
      </details>
    </div>
  );
}
