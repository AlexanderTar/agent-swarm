import { C, SESSION_LABEL, T } from "../copy";
import type { AgentEndpoint, AgentNode, SessionState } from "../types";

export type DisplayState =
  | SessionState
  | "queued"
  | "waiting"
  | "stale"
  | "busy"
  | "preflight_failed"
  | "capacity_paused"
  | "resume_queued";
export type Tone = "green" | "green-hollow" | "grey" | "grey-pulse" | "amber" | "hollow" | "red";
export interface AgentAction {
  endpoint: AgentEndpoint;
  label: string;
  disabled?: boolean;
  body?: { scope: "session" | "subtree" };
  confirm?: string;
}

export function displayState(a: AgentNode): DisplayState {
  // contracts §3.2: no session + a preflight error is "failed at preflight", whatever agent.state says
  const s = a.session;
  if (!s) return a.preflight_error !== null ? "preflight_failed" : "queued";
  const r = a.replacement;
  if (r?.reason && r.phase !== "starting")
    return r.reason === "capacity" ? "capacity_paused" : "resume_queued";
  if (a.state === "queued") return "queued";
  if (s.state === "running") {
    if (s.waiting) return "waiting";
    if (s.busy_since != null) return "busy";
    return s.stale ? "stale" : "running";
  }
  return s.state;
}

export const stateLabel = (s: DisplayState): string => SESSION_LABEL[s];

// stateLabel plus, for a busy worker, the messages waiting for it and how long it has been busy.
// compact is the short form for a narrow card. The daemon only sends busy_since once the worker
// has been busy for minutes, so a short tool call still reads Running.
export function stateText(
  a: AgentNode,
  now = Date.now(),
  compact: boolean | "line" = false,
): string {
  const state = displayState(a);
  const s = a.session;
  if (state !== "busy" || !s || s.busy_since == null) return stateLabel(state);
  const min = Math.floor(Math.max(0, now - s.busy_since) / 60_000);
  const waiting = s.messages_waiting ?? 0;
  if (compact === "line") return T.busyLine(waiting, min);
  if (compact) return T.busyCompact(waiting, min);
  const parts = [stateLabel(state)];
  if (waiting) parts.push(T.messagesWaiting(waiting));
  parts.push(T.busyFor(min));
  return parts.join(" · ");
}

const TONE: Record<DisplayState, Tone> = {
  running: "green",
  busy: "green",
  waiting: "green-hollow",
  spawning: "grey-pulse",
  queued: "grey",
  pause_requested: "amber",
  quiescing: "amber",
  stopping: "amber",
  stale: "amber",
  paused: "hollow",
  interrupted: "red",
  crashed: "red",
  failed: "red",
  preflight_failed: "red",
  completed: "grey",
  cancelled: "grey",
  capacity_paused: "hollow",
  resume_queued: "grey",
};
export const stateTone = (s: DisplayState): Tone => TONE[s];

export const isFinished = (a: AgentNode) => a.state === "finished" || a.state === "acknowledged";

export const countLive = (a: AgentNode): number =>
  a.children.reduce((n, c) => n + 1 + countLive(c), 0);

export const flattenAgents = (nodes: AgentNode[]): AgentNode[] =>
  nodes.flatMap((n) => [n, ...flattenAgents(n.children)]);

export function agentActions(a: AgentNode): AgentAction[] {
  if (isFinished(a)) return [];
  const orch = a.role === "orchestrator";
  const live = countLive(a);
  const terminal: AgentAction = { endpoint: "terminal", label: C.terminal };
  const cancel: AgentAction =
    orch && live > 0
      ? { endpoint: "cancel", label: C.cancel, confirm: T.cancelOrchestrator(a.name, live) }
      : { endpoint: "cancel", label: C.cancel };
  const ack: AgentAction = { endpoint: "ack", label: C.acknowledge };
  const retry: AgentAction = { endpoint: "retry", label: C.retry };
  const resume: AgentAction = { endpoint: "resume", label: C.resume };
  switch (displayState(a)) {
    case "queued":
      return [cancel];
    case "spawning":
      return [terminal, cancel];
    case "running":
    case "busy":
    case "waiting":
    case "stale":
      return [
        terminal,
        {
          endpoint: "pause",
          label: orch ? C.pauseGroup : C.pause,
          body: { scope: orch ? "subtree" : "session" },
        },
        cancel,
      ];
    case "pause_requested":
    case "quiescing":
      return [terminal, { endpoint: "pause", label: C.pausing, disabled: true }, cancel];
    case "stopping":
      return [
        terminal,
        { endpoint: "pause", label: C.pausing, disabled: true },
        { ...cancel, disabled: true },
      ];
    case "paused":
      return [resume, cancel];
    case "interrupted":
      return [resume, ack, cancel];
    case "crashed":
    case "failed":
      return a.session?.tmux_alive ? [retry, ack, terminal] : [retry, ack];
    case "preflight_failed":
      return [retry, cancel];
    case "completed":
    case "cancelled":
      return [];
    case "capacity_paused":
    case "resume_queued":
      return [cancel];
  }
}

let seq = 0;
export function makeAgent(p: Partial<AgentNode> = {}): AgentNode {
  seq += 1;
  return {
    id: `agt_${seq}`,
    name: `agent-${seq}`,
    kind: "claude",
    model: "model-1", // L26: no real model names in src/logic
    effort: null,
    role: "coder",
    item_key: "TASK-1",
    item_title: "Task",
    root_key: "EPIC-1",
    parent_name: null,
    advisor: null,
    state: "active",
    session: {
      id: `ses_${seq}`,
      state: "running",
      attempt: 1,
      generation: 1,
      waiting: false,
      stale: false,
      tmux_alive: true,
      started_at: 0,
      ended_at: null,
    },
    preflight_error: null,
    kind_reason: null,
    created_at: 0,
    finished_at: null,
    children: [],
    finished: [],
    ...p,
  };
}
