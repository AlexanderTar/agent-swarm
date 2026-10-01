import { useId, useRef, useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { Sheet } from "../components/Sheet";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { C } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useItemDetail, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { defaultOrchestratorName } from "../logic/kebab";
import { startedToast } from "../logic/toasts";
import {
  type SubmitFailure, mapSubmitError, nameError, newRequestId, orchestratorPayload, orchestratorsBusy, submitLabel,
} from "../logic/spawnForm";
import type { AgentCatalogEntry, AgentNode, Item, Settings } from "../types";

function Form(p: { item: Item; settings: Settings; catalog: AgentCatalogEntry[]; agents: AgentNode[]; onClose(): void }) {
  const { connected } = useConnection();
  const toast = useToast();
  const nameId = useId();
  const submitting = useRef(false);
  const [repos, setRepos] = useState(p.item.repos);
  const [fields, setFields] = useState<AgentFieldsValue>(() => prefill(p.settings, "orchestrator", p.catalog));
  const [name, setName] = useState(() => defaultOrchestratorName(p.item.title));
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const start = useMutation((api, body: ReturnType<typeof orchestratorPayload>) => api.startOrchestrator(p.item.key, body), ["agents", "item:", "items"]);
  const busy = orchestratorsBusy(p.agents, p.settings);
  const nameErr = failure.name ?? nameError(name);
  const valid = !nameError(name) && isValid(validateChoice(fields.choice, fields.advisor, p.catalog, p.settings.enabled_agents, "orchestrator"));

  const submit = async () => {
    if (submitting.current) return;
    submitting.current = true;
    setFailure({});
    try {
      const agent = await start.run(orchestratorPayload({ name, repos, fields }, p.item, p.settings, p.catalog, requestId));
      toast.success(startedToast(agent, p.item.key));
      p.onClose();
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
      submitting.current = false;
    }
  };

  return (
    <Sheet
      title={C.startOrchestrator}
      width={900}
      subtitle={`${p.item.key} · ${p.item.title}`}
      onClose={p.onClose}
      footer={
        <>
          {busy && <span className="mr-auto self-center text-xs text-muted-foreground">{C.queuedCaption}</span>}
          <Button variant="secondary" onClick={p.onClose}>{C.cancel}</Button>
          <Button disabled={!valid || !connected || start.pending} onClick={() => void submit()}>
            {failure.banner ? C.tryAgain : submitLabel(busy)}
          </Button>
        </>
      }
    >
      {failure.banner && (
        <Alert variant="destructive" role="alert">
          <p>{failure.banner}</p>
          {failure.detail && <p>{failure.detail}</p>}
        </Alert>
      )}
      <RepoPicker label={C.repositories} selected={repos} onChange={setRepos} />
      <AgentFields value={fields} onChange={setFields} settings={p.settings} catalog={p.catalog} />
      <div className="space-y-1.5">
        <Label htmlFor={nameId}>{C.name}</Label>
        <Input id={nameId}
          value={name}
          onChange={(e) => { setName(e.target.value); setFailure((f) => ({ ...f, name: undefined })); }}
          className="key"
        />
        {name !== "" && nameErr && <p className="text-xs text-destructive">{nameErr}</p>}
      </div>
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
      <Sheet title={C.startOrchestrator} width={900} onClose={p.onClose}>
        <Alert variant="destructive">
          {errorText(err)}{" "}
          <Button variant="link"
            onClick={() => {
              detail.reload();
              settings.reload();
              catalog.reload();
              agents.reload();
            }}
          >
            {C.retry}
          </Button>
        </Alert>
      </Sheet>
    );
  }
  if (!detail.data || !settings.data || !catalog.data || !agents.data) return null;
  return <Form item={detail.data.item} settings={settings.data} catalog={catalog.data} agents={agents.data} onClose={p.onClose} />;
}
