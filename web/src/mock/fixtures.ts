import { makeAgent } from "../logic/agentActions";
import { makeItem } from "../logic/tree";
import type {
  Advice, AgentCatalogEntry, AgentNode, Artifact, CatalogModel, Checkpoint, Item, Repo, ReposResponse, Request, SessionInfo, Settings,
} from "../types";

export const NOW = Date.UTC(2026, 8, 17, 12, 0);
const MIN = 60_000;

export interface ArtifactRecord {
  artifact: Artifact;
  revisions: Record<number, { markdown: string; sections: Record<string, string> }>;
}

export interface MockDb {
  token: string;
  items: Item[];
  deps: { item: string; blocked_by: string }[];
  agents: AgentNode[];
  requests: Request[];
  artifacts: ArtifactRecord[];
  checkpoints: Checkpoint[];
  advice: Record<string, Advice[]>;
  settings: Settings;
  catalog: AgentCatalogEntry[];
  repos: ReposResponse;
}

const ses = (state: SessionInfo["state"], p: Partial<SessionInfo> = {}): SessionInfo => ({
  id: `ses_${Math.random().toString(36).slice(2, 8)}`, state, attempt: 1, generation: 1, waiting: false, stale: false,
  tmux_alive: true, started_at: NOW - 40 * MIN, ended_at: null, ...p,
});

function items(): Item[] {
  const child = (key: string, parent: string, root: string, p: Partial<Item> = {}) =>
    makeItem({ key, parent_key: parent, root_key: root, created_at: NOW - 60 * MIN, ...p });
  return [
    makeItem({ key: "EPIC-12", title: "Authentication", status: "in_progress", priority: 1, created_at: NOW - 3000 * MIN, repos: ["repo_chat"], repos_version: 1, origin_spike_key: "SPIKE-2", progress: { done: 0, total: 2, unit: "stories" }, active_agents: 3, open_requests: 1, revision: 7, brief: "Sign-in for the chat app.", acceptance: ["Users can log in", "Sessions persist"] }),
    child("STORY-40", "EPIC-12", "EPIC-12", { title: "Login", status: "in_progress", sort_order: 1, progress: { done: 0, total: 3, unit: "tasks" }, active_agents: 2 }),
    child("TASK-101", "STORY-40", "EPIC-12", { title: "Build login form", status: "in_progress", sort_order: 1, active_agents: 1, role_hint: "coder" }),
    // blocked_by is left off here: createMockDaemon() derives it from `deps` below (13.5), the way
    // the daemon computes it from unresolved dependencies only (store.go:445).
    child("TASK-102", "STORY-40", "EPIC-12", { title: "Persist session", status: "blocked", status_before_block: "in_progress", sort_order: 2 }),
    child("TASK-104", "STORY-40", "EPIC-12", { title: "Validate inputs", status: "in_review", sort_order: 3, active_agents: 1, open_requests: 1 }),
    child("STORY-41", "EPIC-12", "EPIC-12", { title: "Password reset", status: "ready", sort_order: 2, progress: { done: 0, total: 1, unit: "tasks" } }),
    child("TASK-103", "STORY-41", "EPIC-12", { title: "Password reset form", status: "ready" }),
    makeItem({ key: "BUG-7", title: "Login crash", status: "blocked", status_before_block: "in_progress", priority: 2, created_at: NOW - 2000 * MIN, progress: { done: 1, total: 2, unit: "tasks" } }),
    child("TASK-98", "BUG-7", "BUG-7", { title: "Fix token refresh race", status: "done", sort_order: 1 }),
    child("TASK-110", "BUG-7", "BUG-7", { title: "Add crash regression test", status: "ready", sort_order: 2 }),
    makeItem({ key: "BUG-8", title: "Upload retry", status: "in_review", priority: 2, created_at: NOW - 1500 * MIN, open_requests: 1, revision: 4 }),
    makeItem({ key: "SPIKE-3", title: "Offline mode", status: "awaiting_approval", priority: 2, created_at: NOW - 100 * MIN, spike_intent: "feature", open_requests: 4, active_agents: 1, repos: ["repo_chat"] }),
    makeItem({ key: "SPIKE-4", title: "Retry banner copy", status: "in_progress", priority: 2, created_at: NOW - 90 * MIN, spike_intent: "feature", open_requests: 1, active_agents: 1 }),
    makeItem({ key: "SPIKE-5", title: "Crash on resume", status: "awaiting_approval", priority: 2, created_at: NOW - 80 * MIN, spike_intent: "debug", open_requests: 1 }),
    makeItem({ key: "EPIC-20", title: "Billing", status: "draft", priority: 2, created_at: NOW - 70 * MIN }),
    makeItem({ key: "EPIC-30", title: "Legacy cleanup", status: "done", priority: 3, created_at: NOW - 9000 * MIN, progress: { done: 1, total: 1, unit: "stories" } }),
    child("STORY-50", "EPIC-30", "EPIC-30", { title: "Remove v1 routes", status: "done" }),
    child("TASK-150", "STORY-50", "EPIC-30", { title: "Delete handlers", status: "done", sort_order: 1 }),
    child("TASK-151", "STORY-50", "EPIC-30", { title: "Delete docs", status: "cancelled", sort_order: 2 }),
    // mock-only (F15): TASK-999's parent STORY-404 doesn't exist, so a real daemon can't store it.
    // It exercises the client-side Unassigned lane and stays out of `make dev-seed`.
    child("TASK-999", "STORY-404", "EPIC-404", { title: "Imported orphan", status: "ready" }),
  ];
}

function agents(): AgentNode[] {
  const coder = makeAgent({ name: "login-form-coder", kind: "claude", model: "sonnet", role: "coder", item_key: "TASK-101", item_title: "Build login form", root_key: "EPIC-12", parent_name: "auth-epic-orchestrator", session: ses("running"), advisor: { kind: "claude", model: "fable", effort: null, mode: "native" } });
  const review = makeAgent({ name: "login-review", kind: "codex", model: "gpt-6-astra", role: "reviewer", item_key: "TASK-104", item_title: "Validate inputs", root_key: "EPIC-12", parent_name: "auth-epic-orchestrator", session: ses("running", { waiting: true }) });
  // F14: parent_name must match the orchestrator so detail() doesn't list this finished node twice
  // (once as a top-level orphan, once inside the orchestrator's `finished` list).
  const old = makeAgent({ name: "login-form-coder-1", role: "coder", item_key: "TASK-101", root_key: "EPIC-12", parent_name: "auth-epic-orchestrator", state: "finished", session: ses("completed", { tmux_alive: false, ended_at: NOW - 50 * MIN }) });
  return [
    makeAgent({ name: "auth-epic-orchestrator", model: "opus", role: "orchestrator", item_key: "EPIC-12", item_title: "Authentication", root_key: "EPIC-12", session: ses("running"), children: [coder, review], finished: [old] }),
    makeAgent({ name: "offline-spike-orchestrator", model: "opus", role: "orchestrator", item_key: "SPIKE-3", item_title: "Offline mode", root_key: "SPIKE-3", session: ses("running", { waiting: true }) }),
    makeAgent({ name: "retry-copy-orchestrator", model: "opus", role: "orchestrator", item_key: "SPIKE-4", item_title: "Retry banner copy", root_key: "SPIKE-4", session: ses("running") }),
    makeAgent({ name: "crash-debug-orchestrator", model: "opus", role: "orchestrator", item_key: "BUG-7", item_title: "Login crash", root_key: "BUG-7", session: ses("paused", { tmux_alive: false }) }),
  ];
}

function request(p: Partial<Request> & Pick<Request, "id" | "kind" | "item_key" | "item_title" | "root_key">): Request {
  const is_hitl = p.is_hitl ?? (p.kind === "question" || p.kind === "prompt" || p.kind === "blocker");
  return {
    is_hitl,
    agent_name: null, terminal_agent: null, artifact_id: null, artifact_revision: null, section_id: null, section_title: null, section_sha256: null,
    prompt: "", options: [], state: "open", confirmed: null, binding: null, response_text: null, responded_via: null,
    responded_at: null, created_at: NOW, ...p,
  };
}

function requests(): Request[] {
  return [
    request({ id: "req_accept", kind: "accept_epic", item_key: "EPIC-12", item_title: "Authentication", root_key: "EPIC-12", prompt: "Review completed work and accept the epic.", created_at: NOW - 60 * MIN, binding: { item_revision: 7, integrated_checkpoint: "ckp_int", git: [{ repo: "endurio-chat", branch: "epic/epic-12-authentication", sha: "a1b2c3d4e5f6a7b8" }] } }),
    request({ id: "req_question", kind: "question", agent_name: "offline-spike-orchestrator", terminal_agent: "offline-spike-orchestrator", item_key: "SPIKE-3", item_title: "Offline mode", root_key: "SPIKE-3", prompt: "Which sync strategy?", options: ["CRDT", "Last write wins"], created_at: NOW - 20 * MIN }),
    request({ id: "req_q2", kind: "question", agent_name: "login-review", terminal_agent: "auth-epic-orchestrator", item_key: "TASK-104", item_title: "Validate inputs", root_key: "EPIC-12", prompt: "Which validation library?", created_at: NOW - 15 * MIN }),
    request({ id: "req_section", kind: "approve_section", agent_name: "offline-spike-orchestrator", item_key: "SPIKE-3", item_title: "Offline mode", root_key: "SPIKE-3", artifact_id: "art_spec", artifact_revision: 3, section_id: "data-model", section_title: "Data model", section_sha256: "sha-dm-3", prompt: "Approve the data model.", created_at: NOW - 12 * MIN }),
    request({ id: "req_plan", kind: "approve_plan", agent_name: "offline-spike-orchestrator", item_key: "SPIKE-3", item_title: "Offline mode", root_key: "SPIKE-3", artifact_id: "art_plan", artifact_revision: 1, section_sha256: "sha-plan-1", prompt: "Approve the plan.", created_at: NOW - 10 * MIN }),
    request({ id: "req_report", kind: "approve_report", item_key: "SPIKE-5", item_title: "Crash on resume", root_key: "SPIKE-5", artifact_id: "art_report", artifact_revision: 2, section_sha256: "sha-report-2", prompt: "Approve the report.", created_at: NOW - 8 * MIN }),
    request({ id: "req_close", kind: "close_spike", agent_name: "retry-copy-orchestrator", item_key: "SPIKE-4", item_title: "Retry banner copy", root_key: "SPIKE-4", prompt: "Nothing to build.", binding: { resolution: "duplicate_of:EPIC-12" }, created_at: NOW - 5 * MIN }),
    request({ id: "req_repos", kind: "confirm_repos", agent_name: "offline-spike-orchestrator", item_key: "SPIKE-3", item_title: "Offline mode", root_key: "SPIKE-3", prompt: "Offline sync needs the chat API and the app client.", options: { proposed: [{ repo: "repo_chat", reason: "the chat API stores messages.", source: "user" }, { repo: "repo_app", reason: "the sync queue lives in the app's data layer.", source: "agent" }], expansion: [{ repo: "repo_landing", reason: "pricing page lists offline mode as a feature." }] }, binding: { repos_version: 0 }, created_at: NOW - 3 * MIN }),
    request({ id: "req_fix", kind: "accept_fix", item_key: "BUG-8", item_title: "Upload retry", root_key: "BUG-8", prompt: "Review the fix and accept it.", binding: { item_revision: 4, integrated_checkpoint: "ckp_fix", git: [{ repo: "endurio-app", branch: "bug/bug-8-upload-retry", sha: "0f0e0d0c0b0a0908" }] }, created_at: NOW - 2 * MIN }),
  ];
}

const SPEC_3 = "## Overview\n\nWork offline.\n\n## Data model\n\nA local queue of pending messages.\n";
function artifacts(): ArtifactRecord[] {
  const art = (p: Partial<Artifact> & Pick<Artifact, "id" | "item_key" | "kind" | "head_revision">): Artifact => ({
    path: `/Users/alex/.swarm/specs/${p.id}.md`, revision: p.head_revision, sections: [], created_at: NOW - 30 * MIN, ...p,
  });
  const spec = {
    markdown: SPEC_3,
    sections: { overview: "## Overview\n\nWork offline.\n", "data-model": "## Data model\n\nA local queue of pending messages.\n" },
  };
  const sections = [{ id: "overview", title: "Overview", sha256: "sha-ov-3" }, { id: "data-model", title: "Data model", sha256: "sha-dm-3" }];
  return [
    { artifact: art({ id: "art_spec", item_key: "SPIKE-3", kind: "spec", head_revision: 3, sections }), revisions: { 3: spec } },
    { artifact: art({ id: "art_plan", item_key: "SPIKE-3", kind: "plan", head_revision: 1, sections: [{ id: "document", title: "document", sha256: "sha-plan-1" }] }), revisions: { 1: { markdown: "## Work breakdown\n\nTwo stories.\n", sections: {} } } },
    { artifact: art({ id: "art_report", item_key: "SPIKE-5", kind: "debug_report", head_revision: 2 }), revisions: { 2: { markdown: "## Root cause\n\nA stale token.\n", sections: {} } } },
    { artifact: art({ id: "art_epic_spec", item_key: "EPIC-12", kind: "spec", head_revision: 3, sections }), revisions: { 3: spec } },
    { artifact: art({ id: "art_epic_plan", item_key: "EPIC-12", kind: "plan", head_revision: 2 }), revisions: { 2: { markdown: "## Work breakdown\n\nLogin and reset.\n", sections: {} } } },
  ];
}

function checkpoint(p: Partial<Checkpoint> & Pick<Checkpoint, "id" | "item_key" | "kind" | "summary">): Checkpoint {
  return {
    agent_name: null, attempt: 1, resolution: null, next: [], blockers: [], git: [], verification: [], artifacts: [],
    daemon_written: false, created_at: NOW, ...p,
  };
}

function checkpoints(): Checkpoint[] {
  return [
    checkpoint({ id: "ckp_int", item_key: "EPIC-12", kind: "integrated", agent_name: "auth-epic-orchestrator", summary: "Merged login work.", git: [{ repo: "endurio-chat", branch: "epic/epic-12-authentication", sha: "a1b2c3d4e5f6a7b8", dirty: false }], verification: [{ cmd: "go test ./...", phase: "green", ok: true }], created_at: NOW - 61 * MIN }),
    checkpoint({ id: "ckp_101a", item_key: "TASK-101", kind: "accepted", agent_name: "login-form-coder", summary: "Starting the login form.", created_at: NOW - 40 * MIN }),
    checkpoint({ id: "ckp_101p", item_key: "TASK-101", kind: "progress", agent_name: "login-form-coder", summary: "Form renders; wiring submit.", next: ["Wire submit"], blockers: ["Waiting on API shape"], git: [{ repo: "endurio-chat", branch: "task/task-101-login-form", sha: "9f8e7d6c5b4a", dirty: true }], verification: [{ cmd: "pnpm test login", phase: "red", ok: false, note: "expected failure" }], created_at: NOW - 10 * MIN }),
    checkpoint({ id: "ckp_bug7", item_key: "BUG-7", kind: "handoff", summary: "Paused by daemon; orchestrator did not respond.", blockers: ["crash-debug-coder"], daemon_written: true, created_at: NOW - 30 * MIN }),
    checkpoint({ id: "ckp_spike4", item_key: "SPIKE-4", kind: "completed", agent_name: "retry-copy-orchestrator", resolution: "duplicate_of:EPIC-12", summary: "EPIC-12 already covers the retry banner.", created_at: NOW - 6 * MIN }),
    checkpoint({ id: "ckp_fix", item_key: "BUG-8", kind: "integrated", summary: "Retry merged.", git: [{ repo: "endurio-app", branch: "bug/bug-8-upload-retry", sha: "0f0e0d0c0b0a0908" }], verification: [{ cmd: "pnpm test", phase: "green", ok: false, note: "1 flaky test" }], created_at: NOW - 3 * MIN }),
  ];
}

const advice = (): Record<string, Advice[]> => ({
  "login-form-coder": [{
    id: "adv_1", session_id: "ses_x", item_key: "TASK-101", advisor_kind: "claude", advisor_model: "claude-fable-5-1", advisor_effort: null,
    question: "Is a form library worth it?", answer: "No. Two fields don't need one.", error: null, state: "answered", mode: "native",
    duration_ms: 9_400, input_tokens: 12_000, output_tokens: 345, cache_read_tokens: null, cache_write_tokens: null, cost_usd: 0.04,
    created_at: NOW - 20 * MIN, finished_at: NOW - 20 * MIN + 9_400,
  }],
});

const model = (p: Partial<CatalogModel> & Pick<CatalogModel, "id" | "label">): CatalogModel => ({
  efforts: [], default_effort: "", effort_encoding: "flag", advisor_capable: false, ...p,
});
const ALL = ["low", "medium", "high", "xhigh", "max"];

function catalog(): AgentCatalogEntry[] {
  const base = { installed: true, auth_ok: true, auth_error: "", superpowers: true, catalog_fetched_at: NOW - 180 * MIN, catalog_stale: false, catalog_error: "" };
  return [
    { ...base, kind: "claude", version: "2.1.274", default_model: "opus", catalog_source: "api.anthropic.com/v1/models", models: [
      model({ id: "claude-fable-5-1", label: "Fable 5.1", aliases: ["fable"], efforts: ALL, advisor_capable: true }),
      model({ id: "claude-opus-5", label: "Opus 5", aliases: ["opus"], efforts: ALL, advisor_capable: true }),
      model({ id: "claude-sonnet-5", label: "Sonnet 5", aliases: ["sonnet"], efforts: ALL, advisor_capable: true }),
      model({ id: "claude-sonnet-4-6", label: "Sonnet 4.6", efforts: ["low", "medium", "high", "max"], advisor_capable: true }),
      model({ id: "claude-haiku-4-5-20251001", label: "Haiku 4.5", aliases: ["haiku"] }),
    ] },
    { ...base, kind: "codex", version: "0.154.0", default_model: "gpt-6-astra", catalog_source: "codex app-server model/list", models: [
      model({ id: "gpt-6-astra", label: "GPT-6 Astra", efforts: ["low", "medium", "high", "xhigh"], default_effort: "medium", is_default: true }),
    ] },
  ];
}

const settings = (): Settings => ({
  enabled_agents: ["claude", "codex"],
  roles: {
    orchestrator: { agent: "claude", model: "opus" },
    coder: { agent: "claude", model: "sonnet" },
    reviewer: { agent: "claude", model: "opus" },
    ui_reviewer: { agent: "claude", model: "opus" },
    researcher: { agent: "claude", model: "sonnet" },
    debugger: { agent: "claude", model: "opus" },
    mechanical: { agent: "claude", model: "haiku" },
    advisor: { agent: "claude", model: "fable" },
  },
  fallback_default: { agent: "claude", model: "sonnet" },
  notifications: { info: { center: true, sound: true }, attention: { center: true, sound: true }, action: { center: true, sound: true } },
  max_concurrent_agents: 4,
  max_agents_per_root: 4,
  scan_excludes: ["~/Library", "~/.Trash", "~/Downloads"],
  scan_interval_sec: 21600,
  menubar_compact: false,
  usage_poll_sec: 300,
  pause_deadline_sec: 120,
});

function repos(): ReposResponse {
  const r = (id: string, name: string, p: Partial<Repo> = {}): Repo => ({
    id, name, path: `/Users/alex/GitHub/${name}`, remote_url: `git@github.com:${p.remote_owner ?? "AlexanderTar"}/${name}.git`,
    remote_owner: "AlexanderTar", default_branch: "main", source: "scan", groups: [], missing: false, dirty: false,
    last_used_at: null, ...p,
  });
  const chat = r("repo_chat", "endurio-chat", { remote_owner: "EndurioApp", groups: ["endurio", "EndurioApp"] });
  const app = r("repo_app", "endurio-app", { remote_owner: "EndurioApp", groups: ["endurio", "EndurioApp"], dirty: true });
  const landing = r("repo_landing", "endurio-landing", { groups: ["endurio", "AlexanderTar"] });
  const swarm = r("repo_swarm", "agent-swarm", { groups: ["AlexanderTar"] });
  const gone = r("repo_gone", "old-api", { missing: true });
  return {
    recent: [chat],
    groups: [
      { name: "endurio", source: "workspace_dir", repos: [chat, app, landing] },
      { name: "EndurioApp", source: "remote_owner", repos: [chat, app] },
      { name: "AlexanderTar", source: "remote_owner", repos: [landing, swarm] },
    ],
    all: [swarm, app, chat, landing, gone],
    scanned_at: NOW - 120 * MIN,
    scanning: false,
  };
}

export function seed(): MockDb {
  return {
    token: "mock-token",
    items: items(),
    deps: [{ item: "TASK-102", blocked_by: "TASK-98" }, { item: "TASK-104", blocked_by: "TASK-102" }],
    agents: agents(),
    requests: requests(),
    artifacts: artifacts(),
    checkpoints: checkpoints(),
    advice: advice(),
    settings: settings(),
    catalog: catalog(),
    repos: repos(),
  };
}
