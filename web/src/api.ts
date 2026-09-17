import type {
  Advice, AgentCatalogEntry, AgentEndpoint, AgentNode, ApiErrorBody, ApiErrorCode, ApproveBody,
  ArtifactResponse, Checkpoint, ConfirmReposBody, CreateItemBody, CreateSpikeBody, CreateSpikeResponse,
  Graph, GraphScope, Item, ItemDetail, ItemsResponse, PatchItemBody, Repo, ReposResponse, Request,
  Settings, StartOrchestratorBody,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode;
  readonly reason: string | undefined;
  constructor(status: number, code: ApiErrorCode, message: string, reason?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.reason = reason;
  }
}

// Contracts §2: clients show `reason ?? message`.
export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.reason ?? e.message;
  return e instanceof Error ? e.message : String(e);
}

async function toError(res: Response): Promise<ApiError> {
  try {
    const body = (await res.json()) as ApiErrorBody;
    return new ApiError(res.status, body.error.code, body.error.message, body.error.reason);
  } catch {
    return new ApiError(res.status, "internal", res.statusText || `HTTP ${res.status}`);
  }
}

function query(params: Record<string, string | number | undefined>): string {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") s.set(k, String(v));
  const t = s.toString();
  return t ? `?${t}` : "";
}

const enc = encodeURIComponent;
const via = { via: "board" as const };

export interface ApiOptions { base?: string; fetchFn?: typeof fetch }

export function createApi({ base = "", fetchFn = (...a) => fetch(...a) }: ApiOptions = {}) {
  let token: Promise<string> | null = null;

  function getToken(): Promise<string> {
    token ??= (async () => {
      const res = await fetchFn(`${base}/api/bootstrap`);
      if (!res.ok) throw await toError(res);
      return ((await res.json()) as { token: string }).token;
    })().catch((e: unknown) => {
      token = null;
      throw e;
    });
    return token;
  }

  async function authHeaders(): Promise<Record<string, string>> {
    return { Authorization: `Bearer ${await getToken()}` };
  }

  async function call<T>(method: string, path: string, body?: unknown, retried = false): Promise<T> {
    const headers = await authHeaders();
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetchFn(`${base}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 401 && !retried) {
      token = null;
      return call<T>(method, path, body, true);
    }
    if (!res.ok) throw await toError(res);
    if (res.status === 202 || res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  return {
    authHeaders,
    items: () => call<ItemsResponse>("GET", "/api/items?view=flat"),
    item: (key: string) => call<ItemDetail>("GET", `/api/items/${enc(key)}`),
    createItem: (b: CreateItemBody) => call<Item>("POST", "/api/items", b),
    patchItem: (key: string, b: PatchItemBody) => call<Item>("PATCH", `/api/items/${enc(key)}`, b),
    addDep: (key: string, blockedBy: string) =>
      call<void>("POST", `/api/items/${enc(key)}/deps`, { blocked_by: blockedBy }),
    graph: (key: string, scope: GraphScope, hops: number) =>
      call<Graph>("GET", `/api/items/${enc(key)}/graph${query({ scope, hops: scope === "neighbourhood" ? hops : undefined })}`),
    checkpoints: (key: string, limit = 50) =>
      call<Checkpoint[]>("GET", `/api/items/${enc(key)}/checkpoints${query({ limit })}`),
    startOrchestrator: (key: string, b: StartOrchestratorBody) =>
      call<AgentNode>("POST", `/api/items/${enc(key)}/orchestrator`, b),
    createSpike: (b: CreateSpikeBody) => call<CreateSpikeResponse>("POST", "/api/spikes", b),
    agents: (state: "active" | "all" = "active") => call<AgentNode[]>("GET", `/api/agents${query({ state })}`),
    agentAction: (name: string, action: AgentEndpoint, body?: object) =>
      call<unknown>("POST", `/api/agents/${enc(name)}/${action}`, body ?? {}),
    advice: (name: string) => call<Advice[]>("GET", `/api/agents/${enc(name)}/advice`),
    requests: () => call<Request[]>("GET", "/api/requests?state=open"),
    answer: (id: string, text: string) => call<Request>("POST", `/api/requests/${enc(id)}/answer`, { text, ...via }),
    approve: (id: string, b: ApproveBody) => call<Request>("POST", `/api/requests/${enc(id)}/approve`, { ...b, ...via }),
    requestChanges: (id: string, comment: string) =>
      call<Request>("POST", `/api/requests/${enc(id)}/request-changes`, { comment, ...via }),
    confirmRepos: (id: string, b: ConfirmReposBody) =>
      call<Request>("POST", `/api/requests/${enc(id)}/confirm-repos`, { ...b, ...via }),
    closeSpike: (id: string) => call<Request>("POST", `/api/requests/${enc(id)}/close-spike`, { ...via }),
    artifact: (id: string, o: { revision?: number; section?: string } = {}) =>
      call<ArtifactResponse>("GET", `/api/artifacts/${enc(id)}${query({ revision: o.revision, section: o.section })}`),
    settings: () => call<Settings>("GET", "/api/settings"),
    catalog: () => call<AgentCatalogEntry[]>("GET", "/api/catalog"),
    repos: (q = "") => call<ReposResponse>("GET", `/api/repos${query({ q })}`),
    addRepo: (path: string) => call<Repo>("POST", "/api/repos", { path }),
    rescanRepos: () => call<{ found: number; missing: number }>("POST", "/api/repos/rescan", {}),
  };
}

export type Api = ReturnType<typeof createApi>;
