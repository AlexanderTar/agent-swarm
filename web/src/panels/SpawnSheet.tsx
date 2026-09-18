import { useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { Sheet } from "../components/Sheet";
import { C } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useItemDetail, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { defaultOrchestratorName } from "../logic/kebab";
import {
  type SubmitFailure, mapSubmitError, nameError, newRequestId, orchestratorPayload, orchestratorsBusy, submitLabel,
} from "../logic/spawnForm";
import type { AgentCatalogEntry, AgentNode, Item, Settings } from "../types";

function Form(p: { item: Item; settings: Settings; catalog: AgentCatalogEntry[]; agents: AgentNode[]; onClose(): void }) {
  const { connected } = useConnection();
  const [repos, setRepos] = useState(p.item.repos);
  const [fields, setFields] = useState<AgentFieldsValue>(() => prefill(p.settings));
  const [name, setName] = useState(() => defaultOrchestratorName(p.item.title));
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const start = useMutation((api, body: ReturnType<typeof orchestratorPayload>) => api.startOrchestrator(p.item.key, body), ["agents", "item:", "items"]);
  const busy = orchestratorsBusy(p.agents, p.settings);
  const nameErr = failure.name ?? nameError(name);
  const valid = !nameError(name) && isValid(validateChoice(fields.choice, fields.advisor, p.catalog, p.settings.enabled_agents, "orchestrator"));

  const submit = async () => {
    if (start.pending) return;
    setFailure({});
    try {
      await start.run(orchestratorPayload({ name, repos, fields }, p.item, p.settings, p.catalog, requestId));
      p.onClose();
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
    }
  };

  return (
    <Sheet
      title={C.startOrchestrator}
      subtitle={`${p.item.key} · ${p.item.title}`}
      onClose={p.onClose}
      footer={
        <>
          {busy && <span className="mr-auto self-center text-muted">{C.queuedCaption}</span>}
          <button type="button" onClick={p.onClose} className="rounded border border-line px-3 py-1">{C.cancel}</button>
          <button type="button" disabled={!valid || !connected || start.pending} onClick={() => void submit()} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
            {failure.banner ? C.tryAgain : submitLabel(busy)}
          </button>
        </>
      }
    >
      {failure.banner && (
        <div role="alert" className="rounded bg-bad/10 p-2 text-bad">
          <p>{failure.banner}</p>
          {failure.detail && <p>{failure.detail}</p>}
        </div>
      )}
      <RepoPicker label={C.repositories} selected={repos} onChange={setRepos} />
      <AgentFields value={fields} onChange={setFields} settings={p.settings} catalog={p.catalog} />
      <label className="block">
        <span>{C.name}</span>
        <input
          aria-label={C.name}
          value={name}
          onChange={(e) => { setName(e.target.value); setFailure((f) => ({ ...f, name: undefined })); }}
          className="key mt-1 w-full rounded border border-line bg-canvas px-2 py-1"
        />
        {name !== "" && nameErr && <span className="text-bad">{nameErr}</span>}
      </label>
    </Sheet>
  );
}

export function SpawnSheet(p: { itemKey: string; onClose(): void }) {
  const detail = useItemDetail(p.itemKey);
  const settings = useSettings();
  const catalog = useCatalog();
  const agents = useAgents();
  // Standing rule: a failed load gets a message + retry, never a stuck sheet that silently renders
  // nothing (the brief returned `null` here on any of these four queries failing, forever).
  const err = detail.error ?? settings.error ?? catalog.error ?? agents.error;
  if (err) {
    return (
      <Sheet title={C.startOrchestrator} onClose={p.onClose}>
        <p className="text-bad">
          {errorText(err)}{" "}
          <button
            type="button"
            className="text-accent underline"
            onClick={() => {
              detail.reload();
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
  if (!detail.data || !settings.data || !catalog.data || !agents.data) return null;
  return <Form item={detail.data.item} settings={settings.data} catalog={catalog.data} agents={agents.data} onClose={p.onClose} />;
}
