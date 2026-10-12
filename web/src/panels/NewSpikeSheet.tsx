import { Loader2, TriangleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { errorText } from "../api";
import { cn } from "@/lib/utils";
import { AgentFields, type AgentFieldsValue } from "../components/AgentFields";
import { RepoPicker } from "../components/RepoPicker";
import { RequestImages, type RequestImage } from "../components/RequestImages";
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
import { addImages } from "../logic/requestImages";
import { useMediaQuery } from "../state/media";
import { startedToast } from "../logic/toasts";
import {
  type SubmitFailure, mapSubmitError, nameError, newRequestId, orchestratorsBusy, spikePayload, submitLabel,
} from "../logic/spawnForm";
import type { AgentCatalogEntry, AgentNode, CreateSpikeBody, CreateSpikeResponse, Settings, SpikeIntent } from "../types";

const hasFiles = (e: { dataTransfer: DataTransfer | null }) => Array.from(e.dataTransfer?.types ?? []).includes("Files");

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
  const [nameTouched, setNameTouched] = useState(false);
  const [intent, setIntent] = useState<SpikeIntent>(p.intent ?? "chore");
  const [repos, setRepos] = useState<string[]>([]);
  const [fields, setFields] = useState<AgentFieldsValue>(() => prefill(p.settings, "orchestrator", p.catalog));
  const [request, setRequest] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const [failure, setFailure] = useState<SubmitFailure>({});
  const [images, setImages] = useState<RequestImage[]>([]);
  const [pending, setPending] = useState(0);
  const [imageError, setImageError] = useState<string>();
  const [dragging, setDragging] = useState(false);
  const [started, setStarted] = useState<CreateSpikeResponse>();
  const notSaved = useRef<HTMLDivElement>(null);
  const imagesRef = useRef<RequestImage[]>([]);
  const queue = useRef<Promise<void>>(Promise.resolve());
  const alive = useRef(true);
  const zone = useRef<HTMLDivElement>(null);
  const depth = useRef(0);
  const finePointer = useMediaQuery("(hover: hover) and (pointer: fine)");
  const hint = finePointer ? (/Mac|iPhone|iPad/.test(navigator.userAgent) ? C.requestImageHintMac : C.requestImageHint) : undefined;
  const create = useMutation((api, body: CreateSpikeBody) => api.createSpike(body), ["agents", "items", "item:"]);
  const busy = orchestratorsBusy(p.agents, p.settings);
  const tName = name.trim();
  const tReq = request.trim();
  const clientNameErr = tName === "" ? (tReq === "" && nameTouched ? C.nameOrRequestRequired : undefined) : nameError(name);
  const nameErr = failure.name ?? clientNameErr;
  const agentChoiceValid = isValid(validateChoice(fields.choice, fields.advisor, p.catalog, p.settings.enabled_agents, "orchestrator"));
  const canStart = connected && !create.pending && pending === 0 && !started && !nameErr && agentChoiceValid && (tName !== "" || tReq !== "");
  const nameKebab = kebab(name);

  // Images-not-saved: the alert is the first child of the scrolling sheet body; bring it into view.
  useEffect(() => {
    const body = started ? notSaved.current?.closest<HTMLElement>(".overflow-y-auto") : null;
    if (body) body.scrollTop = 0;
  }, [started]);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  // Window guard: a file dropped outside the zone would make the browser open it and lose the form.
  useEffect(() => {
    const guard = (e: DragEvent) => {
      if (!hasFiles(e) || zone.current?.contains(e.target as Node)) return;
      e.preventDefault();
      if (e.type === "dragover" && e.dataTransfer) e.dataTransfer.dropEffect = "none";
    };
    window.addEventListener("dragover", guard);
    window.addEventListener("drop", guard);
    return () => {
      window.removeEventListener("dragover", guard);
      window.removeEventListener("drop", guard);
    };
  }, []);

  const addFiles = (files: File[], fallbackName: string, usable: (n: string) => boolean = (n) => n !== "") => {
    if (files.length === 0) return;
    setPending((n) => n + files.length);
    queue.current = queue.current.then(async () => {
      try {
        const r = await addImages(imagesRef.current.length, files.map((f) => ({ blob: f, name: usable(f.name) ? f.name : fallbackName })));
        if (!alive.current) return;
        const fresh = r.added.map((a) => ({ id: crypto.randomUUID(), name: a.name, blob: a.blob, url: URL.createObjectURL(a.blob) }));
        imagesRef.current = [...imagesRef.current, ...fresh];
        setImages(imagesRef.current);
        setImageError(r.error);
      } finally {
        setPending((n) => n - files.length);
      }
    });
  };
  const removeImage = (id: string) => {
    imagesRef.current = imagesRef.current.filter((i) => i.id !== id);
    setImages(imagesRef.current);
    setImageError(undefined);
  };
  const onPaste = (e: React.ClipboardEvent) => {
    const files = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (files.length === 0) return;
    e.preventDefault();
    // Browsers name every clipboard screenshot "image.png".
    addFiles(files, C.pastedImageName, (n) => n !== "" && n !== "image.png");
  };
  const zoneHandlers = {
    onDragEnter: (e: React.DragEvent) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      depth.current += 1;
      setDragging(true);
    },
    onDragOver: (e: React.DragEvent) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    },
    onDragLeave: (e: React.DragEvent) => {
      if (!hasFiles(e.nativeEvent)) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) setDragging(false);
    },
    onDrop: (e: React.DragEvent) => {
      if (!hasFiles(e.nativeEvent)) return;
      e.preventDefault();
      depth.current = 0;
      setDragging(false);
      addFiles(Array.from(e.dataTransfer.files), C.droppedImageName);
    },
  };

  const finish = (r: CreateSpikeResponse) => {
    toast.success(startedToast(r.agent, r.item.key));
    p.onCreated(r.item.key);
  };

  const submit = async () => {
    if (create.pending) return;
    setFailure({});
    try {
      const r = await create.run(await spikePayload({ name, intent, repos, request, images, fields }, p.settings, p.catalog, requestId));
      if (r.attachments_failed) setStarted(r);
      else finish(r);
    } catch (e) {
      setFailure(mapSubmitError(e));
      setRequestId(newRequestId());
    }
  };

  return (
    <Sheet
      title={C.newOrchestrator}
      width={900}
      onClose={started ? () => finish(started) : p.onClose}
      footer={
        <>
          {busy && <span className="mr-auto self-center text-xs text-muted-foreground max-sm:order-last max-sm:mr-0 max-sm:self-start">{C.queuedCaption}</span>}
          <Button variant="secondary" disabled={!!started} onClick={p.onClose}>{C.cancel}</Button>
          {started ? (
            <Button autoFocus onClick={() => finish(started)}>{C.done}</Button>
          ) : (
            <Button disabled={!canStart} onClick={() => void submit()}>
              {create.pending && <Loader2 className="animate-spin" />}
              {failure.banner ? C.tryAgain : submitLabel(busy)}
            </Button>
          )}
        </>
      }
    >
      {started && (
        <Alert ref={notSaved} className="border-warning/40 text-warning">
          <TriangleAlert />
          <div>{C.imagesNotSaved}</div>
        </Alert>
      )}
      {failure.banner && (
        <Alert variant="destructive" role="alert">
          <p>{failure.banner}</p>
          {failure.detail && <p>{failure.detail}</p>}
        </Alert>
      )}
      <div className={cn("space-y-4", started && "opacity-50")} inert={!!started} aria-disabled={!!started} data-started={!!started}>
      <div className="space-y-1.5">
        <Label htmlFor={nameId}>{C.name}</Label>
        <Input id={nameId}
          value={name}
          placeholder={C.nameOptionalPlaceholder}
          onChange={(e) => { setName(e.target.value); if (e.target.value.trim() !== "") setNameTouched(true); setFailure((f) => ({ ...f, name: undefined })); }}
        />
        {nameErr ? <p className="text-xs text-destructive">{nameErr}</p>
          : nameKebab !== "" && <p className="font-mono text-xs text-muted-foreground">{T.agentName(nameKebab)}</p>}
      </div>
      <div className="grid grid-cols-[72px_1fr] items-start gap-x-2 gap-y-1 max-sm:grid-cols-1">
          <Label className="h-7 leading-7">{C.intent}</Label>
          <Segmented<SpikeIntent>
            label={C.intent}
            value={intent}
            onChange={setIntent}
            options={[{ value: "chore", label: C.choreIntent }, { value: "feature", label: C.featureSpike }, { value: "debug", label: C.debugSpike }]}
          />
          <p className="col-start-2 text-xs text-muted-foreground max-sm:col-start-1">{intent === "chore" ? C.choreCaption : intent === "feature" ? C.featureCaption : C.debugCaption}</p>
      </div>
      <RepoPicker label={C.repositoriesOptional} caption={intent === "chore" ? C.choreReposCaption : C.reposCaption} selected={repos} onChange={setRepos} />
      <AgentFields value={fields} onChange={setFields} settings={p.settings} catalog={p.catalog} />
      <div ref={zone} data-drop-zone className="space-y-1.5" {...zoneHandlers}>
        <Label htmlFor={requestIdField}>{C.requestOptional}</Label>
        <div className="relative">
          <Textarea id={requestIdField} rows={5} className="min-h-[108px]" placeholder={hint} value={request} onChange={(e) => setRequest(e.target.value)} onPaste={onPaste} />
          {dragging && (
            <div className="pointer-events-none absolute inset-0 grid place-items-center rounded-md border-2 border-dashed border-ring bg-background text-sm font-medium text-link">
              {C.dropImages}
            </div>
          )}
        </div>
        <RequestImages images={images} pending={pending} error={imageError} onAdd={(files) => addFiles(files, C.droppedImageName)} onRemove={removeImage} />
      </div>
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
