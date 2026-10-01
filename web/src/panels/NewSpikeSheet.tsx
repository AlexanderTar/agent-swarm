import { useId, useState } from "react";
import { errorText } from "../api";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { Segmented } from "../components/Segmented";
import { Sheet } from "../components/Sheet";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Textarea } from "../components/ui/textarea";
import { C, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents, useCatalog, useSettings } from "../data/queries";
import { isValid, prefill, validateChoice } from "../logic/catalog";
import { kebab } from "../logic/kebab";
import { startedToast } from "../logic/toasts";
import {
  type SubmitFailure, mapSubmitError, nameError, newRequestId, orchestratorsBusy, spikePayload, submitLabel,
} from "../logic/spawnForm";
import type { AgentCatalogEntry, AgentNode, CreateSpikeBody, Settings, SpikeIntent } from "../types";

function Form(p: {
  settings: Settings;
  catalog: AgentCatalogEntry[];
  agents: AgentNode[];
  intent?: SpikeIntent;
  onClose(): void;
  onCreated(key: string): void;
}) {
  const { connected } = useConnection();
  const toast = useToast();
  const nameId = useId();
  const requestIdField = useId();
  const [name, setName] = useState("");
  const [intent, setIntent] = useState<SpikeIntent>(p.intent ?? "feature");
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
      toast.success(startedToast(r.agent, r.item.key));
      p.onCreated(r.item.key);
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
    }
  };

  return (
    <Sheet
      title={C.newOrchestrator}
      width={900}
      onClose={p.onClose}
      footer={
        <>
          {busy && <span className="mr-auto self-center text-xs text-muted-foreground">{C.queuedCaption}</span>}
          <Button variant="secondary" onClick={p.onClose}>{C.cancel}</Button>
          <Button disabled={!valid || !connected || create.pending} onClick={() => void submit()}>
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
      <div className="space-y-1.5">
        <Label htmlFor={nameId}>{C.name}</Label>
        <Input id={nameId}
          value={name}
          onChange={(e) => { setName(e.target.value); setFailure((f) => ({ ...f, name: undefined })); }}
        />
        <p className="text-xs text-muted-foreground">{T.agentName(kebab(name))}</p>
        {nameErr && <p className="text-xs text-destructive">{nameErr}</p>}
      </div>
      <div className="grid grid-cols-[72px_1fr] items-start gap-x-2 gap-y-1">
          <Label className="h-7 leading-7">{C.intent}</Label>
          <Segmented<SpikeIntent>
            label={C.intent}
            value={intent}
            onChange={setIntent}
            options={[{ value: "chore", label: C.choreIntent }, { value: "feature", label: C.featureSpike }, { value: "debug", label: C.debugSpike }]}
          />
          <p className="col-start-2 text-xs text-muted-foreground">{intent === "chore" ? C.choreCaption : intent === "feature" ? C.featureCaption : C.debugCaption}</p>
      </div>
      <RepoPicker label={C.repositoriesOptional} caption={intent === "chore" ? C.choreReposCaption : C.reposCaption} selected={repos} onChange={setRepos} />
      <AgentFields value={fields} onChange={setFields} settings={p.settings} catalog={p.catalog} />
      <div className="space-y-1.5">
        <Label htmlFor={requestIdField}>{C.requestOptional}</Label>
        <Textarea id={requestIdField} rows={5} value={request} onChange={(e) => setRequest(e.target.value)} />
      </div>
    </Sheet>
  );
}

export function NewSpikeSheet(p: { intent?: SpikeIntent; onClose(): void; onCreated(key: string): void }) {
  const settings = useSettings();
  const catalog = useCatalog();
  const agents = useAgents();
  // Standing rule: a failed load gets a message + retry, never a silently-empty sheet.
  const err = settings.error ?? catalog.error ?? agents.error;
  if (err) {
    return (
      <Sheet title={C.newOrchestrator} width={900} onClose={p.onClose}>
        <Alert variant="destructive">
          {errorText(err)}{" "}
          <Button variant="link"
            onClick={() => {
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
  if (!settings.data || !catalog.data || !agents.data) return null;
  return <Form settings={settings.data} catalog={catalog.data} agents={agents.data} {...p} />;
}
