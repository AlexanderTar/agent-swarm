import { useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { Segmented } from "../components/Segmented";
import { Sheet } from "../components/Sheet";
import { C, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { kebab } from "../logic/kebab";
import {
  type SubmitFailure, mapSubmitError, nameError, newRequestId, orchestratorsBusy, spikePayload, submitLabel,
} from "../logic/spawnForm";
import type { AgentCatalogEntry, AgentNode, CreateSpikeBody, Settings } from "../types";

function Form(p: {
  settings: Settings;
  catalog: AgentCatalogEntry[];
  agents: AgentNode[];
  caption?: string;
  onClose(): void;
  onCreated(key: string): void;
}) {
  const { connected } = useConnection();
  const [name, setName] = useState("");
  const [intent, setIntent] = useState<"feature" | "debug">("feature");
  const [repos, setRepos] = useState<string[]>([]);
  const [fields, setFields] = useState<AgentFieldsValue>(() => prefill(p.settings, "orchestrator", p.catalog));
  const [request, setRequest] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const create = useMutation((api, body: CreateSpikeBody) => api.createSpike(body), ["agents", "items", "item:"]);
  const busy = orchestratorsBusy(p.agents, p.settings);
  const nameErr = failure.name ?? (name !== "" ? nameError(name) : undefined);
  const valid = !nameError(name) && isValid(validateChoice(fields.choice, fields.advisor, p.catalog, p.settings.enabled_agents, "orchestrator"));

  const submit = async () => {
    if (create.pending) return;
    setFailure({});
    try {
      const r = await create.run(spikePayload({ name, intent, repos, request, fields }, p.settings, p.catalog, requestId));
      p.onCreated(r.item.key);
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
    }
  };

  return (
    <Sheet
      title={C.newSpike}
      onClose={p.onClose}
      footer={
        <>
          {busy && <span className="mr-auto self-center text-muted">{C.queuedCaption}</span>}
          <button type="button" onClick={p.onClose} className="rounded border border-line px-3 py-1">{C.cancel}</button>
          <button type="button" disabled={!valid || !connected || create.pending} onClick={() => void submit()} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
            {failure.banner ? C.tryAgain : submitLabel(busy)}
          </button>
        </>
      }
    >
      {p.caption && <p className="rounded bg-raised p-2">{p.caption}</p>}
      {failure.banner && (
        <div role="alert" className="rounded bg-bad/10 p-2 text-bad">
          <p>{failure.banner}</p>
          {failure.detail && <p>{failure.detail}</p>}
        </div>
      )}
      <label className="block">
        <span>{C.name}</span>
        <input
          aria-label={C.name}
          value={name}
          onChange={(e) => { setName(e.target.value); setFailure((f) => ({ ...f, name: undefined })); }}
          className="mt-1 w-full rounded border border-line bg-canvas px-2 py-1"
        />
        <span className="block text-muted">{T.agentName(kebab(name))}</span>
        {nameErr && <span className="block text-bad">{nameErr}</span>}
      </label>
      <div className="space-y-1">
        <span className="mr-2">{C.intent}</span>
        <Segmented<"feature" | "debug">
          label={C.intent}
          value={intent}
          onChange={setIntent}
          options={[{ value: "feature" as const, label: C.featureSpike }, { value: "debug" as const, label: C.debugSpike }]}
        />
        <p className="text-muted">{intent === "feature" ? C.featureCaption : C.debugCaption}</p>
      </div>
      <RepoPicker label={C.repositoriesOptional} caption={C.reposCaption} selected={repos} onChange={setRepos} />
      <AgentFields value={fields} onChange={setFields} settings={p.settings} catalog={p.catalog} />
      <label className="block">
        <span>{C.requestOptional}</span>
        <textarea aria-label={C.requestOptional} rows={3} value={request} onChange={(e) => setRequest(e.target.value)} className="mt-1 w-full rounded border border-line bg-canvas px-2 py-1" />
      </label>
    </Sheet>
  );
}

export function NewSpikeSheet(p: { caption?: string; onClose(): void; onCreated(key: string): void }) {
  const settings = useSettings();
  const catalog = useCatalog();
  const agents = useAgents();
  // Standing rule: a failed load gets a message + retry, never a silently-empty sheet.
  const err = settings.error ?? catalog.error ?? agents.error;
  if (err) {
    return (
      <Sheet title={C.newSpike} onClose={p.onClose}>
        <p className="text-bad">
          {errorText(err)}{" "}
          <button
            type="button"
            className="text-accent underline"
            onClick={() => {
              settings.reload();
              catalog.reload();
              agents.reload();
            }}
          >
            {C.retry}
          </button>
        </p>
      </Sheet>
    );
  }
  if (!settings.data || !catalog.data || !agents.data) return null;
  return <Form settings={settings.data} catalog={catalog.data} agents={agents.data} {...p} />;
}
