// Wire types for the daemon HTTP API (spec §5–§7) plus board UI state types.
// Contract for P1/P2: ~/.superpowers/specs/2026-09-17-agent-swarm-contracts.md. JSON is snake_case,
// timestamps are integer ms since the epoch, unset optional fields are null (never absent),
// arrays are never null, and items are addressed by key everywhere the board sees them.

export type ItemType = "epic" | "story" | "task" | "bug" | "spike" | "chore";
export type ItemStatus =
  | "draft" | "ready" | "in_progress" | "blocked" | "in_review" | "awaiting_approval" | "done" | "cancelled";
export const ITEM_STATUSES: readonly ItemStatus[] = [
  "draft", "ready", "in_progress", "blocked", "in_review", "awaiting_approval", "done", "cancelled",
];
export type Priority = 0 | 1 | 2 | 3;
export type TddExempt = "docs" | "config" | "mechanical-rename" | "spike-research";

export interface Progress { done: number; total: number; unit: "tasks" | "stories" }

// Workflow/Unit mirror internal/workflow.Spec and items.Unit (spec B2/C1);
// the board's own workflow UI lands in a later package, so these are just
// enough shape for Item to round-trip the field. Optional here (rather than
// `| null`) so existing fixtures.ts item literals keep typechecking without
// every one of them naming these five new wire fields.
export interface WorkflowLoop { fix: string; max_rounds?: number; on_exhausted?: string }
export interface WorkflowStep {
  id: string;
  run?: string;
  gates?: string[];
  review?: string[];
  of?: string;
  loop?: WorkflowLoop;
}
export interface WorkflowIntegration { merge_order?: string[]; verify?: string[]; final_review?: string[] }
export interface Workflow {
  template?: string;
  steps?: WorkflowStep[];
  max_rounds?: number;
  retries?: number;
  after_tasks?: WorkflowStep;
  integration?: WorkflowIntegration;
}
export interface Unit { title: string; steps: string[] }

export interface Item {
  id: string;
  key: string;
  type: ItemType;
  parent_id: string | null;
  parent_key: string | null;
  root_id: string;
  root_key: string;
  title: string;
  brief: string;
  acceptance: string[];
  status: ItemStatus;
  status_before_block: ItemStatus | null;
  priority: Priority;
  role_hint: string | null;
  tdd_exempt: TddExempt | null;
  workflow?: Workflow | null;
  steps?: string[];
  units?: Unit[];
  solo?: string | null;
  verify?: string[];
  repos: string[];                 // top-level: confirmed repo ids; children: repo hints
  repos_version: number;           // top-level only (I13); 0 elsewhere
  suggested_repos: string[];
  spike_intent: "feature" | "debug" | null;
  origin_spike_id: string | null;
  origin_spike_key: string | null;
  legacy_key: string | null;
  sort_order: number;
  revision: number;
  archived_at: number | null;
  created_at: number;
  updated_at: number;
  // computed by the daemon
  blocked_by: string[];            // keys of unresolved dependencies
  progress: Progress | null;       // direct children
  active_agents: number;           // live sessions in the subtree
  open_requests: number;           // open requests on this item
  context: boolean;                // view=tree context ancestor; always false for view=flat
}

export interface ItemsResponse { items: Item[]; matches: number }

export interface ItemDetail {
  item: Item;
  ancestors: Item[];               // root first
  children: Item[];
  deps: { blocked_by: Item[]; blocks: Item[] };
  agents: AgentNode[];             // agents working on this item or its subtree (tree form)
  requests: Request[];             // open requests on this item
  artifacts: Artifact[];
  workflow_state?: WorkflowState;
  crew?: WorkflowCrewMember[];
}

export interface WorkflowFinding { severity: string; file: string; line?: number; unit?: number; summary: string; reviewer?: string }
export interface WorkflowRun {
  id?: string; step: string; role: string; agent_id?: string; agent: string;
  state: string; verdict: "" | "pass" | "changes_requested" | "blocked";
  sha: string; round: number; auto_retries?: number; findings: WorkflowFinding[];
}
export interface WorkflowState { state: string; round: number; escalation: string; runs: WorkflowRun[] }
export interface WorkflowCrewMember { agent: string; role: string; step: string; state: string }

export interface CreateItemBody {
  request_id: string;
  type: "epic" | "bug" | "story" | "task";
  title: string;
  brief: string;
  acceptance: string[];
  parent_key?: string;
}

export interface PatchItemBody {
  title?: string;
  brief?: string;
  acceptance?: string[];
  priority?: Priority;
  status?: ItemStatus;
  revision: number;
}

export type GraphScope = "root" | "neighbourhood";
export interface GraphNode { key: string; type: ItemType; title: string; status: ItemStatus; root_key: string; external: boolean }
export interface GraphEdge { from: string; to: string } // B waits for A when from=A, to=B
export interface Graph { nodes: GraphNode[]; edges: GraphEdge[] }

export type CheckpointKind = "accepted" | "progress" | "blocked" | "handoff" | "completed" | "failed" | "integrated";
export interface GitRef { repo: string; branch: string; sha: string; dirty?: boolean }
export interface VerifyEntry { cmd: string; phase: "red" | "green"; ok: boolean; note?: string }
export interface Checkpoint {
  id: string;
  item_key: string;
  agent_name: string | null;
  kind: CheckpointKind;
  attempt: number;
  resolution: string | null;
  summary: string;
  next: string[];
  blockers: string[];
  git: GitRef[];
  verification: VerifyEntry[];
  artifacts: string[];
  daemon_written: boolean;
  created_at: number;
}

export type AgentKind = "claude" | "codex" | "agy" | "cursor" | "muse" | "fake";
export type Role = "orchestrator" | "coder" | "reviewer" | "ui_reviewer" | "researcher" | "debugger" | "mechanical" | "designer";
export type AgentState = "queued" | "active" | "finished" | "acknowledged";
export type SessionState =
  | "spawning" | "running" | "pause_requested" | "quiescing" | "stopping" | "paused"
  | "interrupted" | "completed" | "failed" | "crashed" | "cancelled";

export interface SessionInfo {
  id: string;
  state: SessionState;
  attempt: number;
  generation: number;
  waiting: boolean;
  stale: boolean;          // A1: no hook call or sync for 30 min while not waiting
  tmux_alive: boolean;     // the tmux session exists
  started_at: number;
  ended_at: number | null;
}

export interface AdvisorInfo { kind: AgentKind; model: string; effort: string | null; mode: "native" | "simulated" }

export interface AgentNode {
  id: string;
  name: string;
  kind: AgentKind;
  model: string;
  effort: string | null;
  role: Role;
  item_key: string;
  item_title: string;
  root_key: string;
  parent_name: string | null;
  advisor: AdvisorInfo | null;
  state: AgentState;
  session: SessionInfo | null;     // latest generation; null when never spawned
  preflight_error: string | null;  // set when spawn preflight failed (never spawned)
  created_at: number;
  finished_at: number | null;
  children: AgentNode[];           // live and unacknowledged children
  finished: AgentNode[];           // completed, cancelled and acknowledged children
}

export type RequestKind =
  | "question" | "prompt" | "blocker" | "confirm_repos" | "approve_section" | "approve_plan" | "approve_report"
  | "accept_epic" | "accept_fix" | "close_spike";
export type RequestState = "open" | "approved" | "changes_requested" | "answered" | "withdrawn" | "stale";
export interface RepoProposal { repo: string; reason: string; source: "user" | "agent" }
export interface ConfirmReposOptions { proposed: RepoProposal[]; expansion: { repo: string; reason: string }[] }
export interface AcceptBinding { item_revision: number; integrated_checkpoint: string; git: GitRef[] }
export type RequestBinding = AcceptBinding | { repos_version: number } | { resolution: string };

export interface Request {
  id: string;
  kind: RequestKind;
  is_hitl: boolean;
  agent_name: string | null;
  terminal_agent: string | null;
  item_key: string;
  item_title: string;
  root_key: string;
  artifact_id: string | null;
  artifact_revision: number | null;
  section_id: string | null;
  section_title: string | null;
  section_sha256: string | null;
  prompt: string;
  options: string[] | ConfirmReposOptions;
  state: RequestState;
  confirmed: string[] | null;
  binding: RequestBinding | null;
  response_text: string | null;
  responded_via: "menubar" | "board" | "cli" | null;
  responded_at: number | null;
  created_at: number;
}

export interface ArtifactSection { id: string; title: string; sha256: string }
export interface Artifact {
  id: string;
  item_key: string;
  kind: "spec" | "plan" | "debug_report" | "note";
  path: string;
  head_revision: number;
  revision: number;                // the revision this object describes
  sections: ArtifactSection[];
  created_at: number;
}
export interface ArtifactResponse { artifact: Artifact; markdown: string }

export interface Advice {
  id: string;
  session_id: string;
  item_key: string;
  advisor_kind: AgentKind;
  advisor_model: string;
  advisor_effort: string | null;
  question: string;
  answer: string | null;
  error: string | null;
  state: "queued" | "running" | "answered" | "failed" | "timed_out";
  mode: "native" | "simulated";
  duration_ms: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  cache_read_tokens: number | null;
  cache_write_tokens: number | null;
  cost_usd: number | null;
  created_at: number;
  finished_at: number | null;
}

export type SettingsRole = Role | "advisor";
export interface RoleDefault { agent: AgentKind; model: string; effort?: string }
export interface Settings {
  enabled_agents: AgentKind[];
  roles: Partial<Record<SettingsRole, RoleDefault>>;
  // The agent+model substituted when a role's configured agent is confirmed
  // out of usage (docs/specs/2026-09-19-usage-fallback-agent.md). Read-side
  // type parity only -- there is no Settings-editing screen in web/ (the
  // menubar app is the only editor); see the spec's "Web UI" section.
  fallback_default: RoleDefault;
  notifications: Record<string, { center: boolean; sound: boolean }>;
  // Single global admission ceiling shared by every role, orchestrator
  // included (docs/specs/2026-09-24-unify-agent-limits.md; replaces the old,
  // separately-counted max_orchestrators/max_agents pair).
  max_concurrent_agents: number;
  max_agents_per_root: number;
  scan_excludes: string[];
  scan_interval_sec: number;
  menubar_compact: boolean;
  usage_poll_sec: number;
  pause_deadline_sec: number;
}

export interface CatalogModel {
  id: string;
  label: string;
  aliases?: string[];
  efforts: string[];
  default_effort: string;
  effort_encoding: "flag" | "slug";
  launch_ids?: Record<string, string>; // slug encoding: level → exact catalog id; bare id under "default"
  advisor_capable: boolean;
  hidden?: boolean;
  is_default?: boolean;
}

export interface AgentCatalogEntry {
  kind: AgentKind;
  installed: boolean;
  version: string;
  auth_ok: boolean;
  auth_error: string;
  superpowers: boolean;
  models: CatalogModel[];
  default_model: string;
  catalog_source: string;
  catalog_fetched_at: number;
  catalog_stale: boolean;
  catalog_error: string;
}

export interface Repo {
  id: string;
  name: string;
  path: string;
  remote_url: string | null;
  remote_owner: string | null;
  default_branch: string | null;
  source: "scan" | "manual";
  groups: string[];
  missing: boolean;
  dirty: boolean;
  last_used_at: number | null;
}
export type RepoGroupSource = "remote_owner" | "workspace_dir" | "code_workspace";
export interface RepoGroup { name: string; source: RepoGroupSource; repos: Repo[] }
export interface ReposResponse { recent: Repo[]; groups: RepoGroup[]; all: Repo[]; scanned_at: number; scanning: boolean }

export type AdvisorPayload = { agent: AgentKind; model: string; effort?: string } | "none";
export interface StartOrchestratorBody {
  request_id: string;
  agent: AgentKind;
  model: string;
  effort?: string;
  advisor?: AdvisorPayload;
  repos: string[];
  repos_version: number;
  name?: string;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}
export interface CreateSpikeBody {
  request_id: string;
  name: string;
  intent: "feature" | "debug";
  repos?: string[];
  agent: AgentKind;
  model: string;
  effort?: string;
  advisor?: AdvisorPayload;
  request?: string;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}
export interface CreateSpikeResponse { item: Item; agent: AgentNode; queued: boolean }

export type AgentEndpoint = "pause" | "resume" | "cancel" | "ack" | "retry" | "terminal";
export interface ApproveBody { section_sha256?: string; artifact_revision?: number; binding?: AcceptBinding }
export interface ConfirmReposBody { repos: string[]; comment?: string; repos_version: number }

export type ApiErrorCode =
  | "bad_request" | "unauthorized" | "not_found" | "conflict" | "transition_denied"
  | "limit_reached" | "preflight_failed" | "internal";
export interface ApiErrorBody { error: { code: ApiErrorCode; message: string; reason?: string } }

// SSE
export interface BoardEvent { seq: number | null; type: string; data: unknown }
export type ConnState = "connecting" | "open" | "closed";

// Board UI state
export type View = "hierarchy" | "kanban" | "dependencies" | "inbox";
export type CardLevel = "tasks" | "stories" | "top";
export type Grouping = "root" | "flat";
export type InboxFilter = "all" | "questions" | "approvals" | "reviews";
export interface Filter { q: string; type: ItemType | ""; status: ItemStatus | "" }
