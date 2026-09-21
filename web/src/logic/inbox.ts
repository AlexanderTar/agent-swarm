import { C } from "../copy";
import type { AgentNode, InboxFilter, Request } from "../types";
import { flattenAgents } from "./agentActions";
import { ageCompact } from "./format";
import { requestTitle } from "./requestTitle";

export function filterRequests(reqs: Request[], f: InboxFilter): Request[] {
  return reqs
    .filter((r) => {
      if (f === "all") return r.is_hitl;
      if (f === "questions") return r.is_hitl && (r.kind === "question" || r.kind === "prompt" || r.kind === "blocker");
      if (f === "approvals" || f === "reviews") return !r.is_hitl;
      return true;
    })
    .sort((a, b) => a.created_at - b.created_at);
}

export const inboxRow = (r: Request, now = Date.now()) => ({
  title: requestTitle(r),
  sub: `${r.item_key} · ${ageCompact(r.created_at, now)}`,
});

export const pickRequest = (reqs: Request[], id: string) => reqs.find((r) => r.id === id) ?? reqs[0];

export type RequestTarget = { kind: "terminal"; agent: string } | { kind: "unavailable"; hint: string };

// What a Needs-you row's click does. null for a request with no terminal_agent (approvals).
export function requestTarget(r: Request, agents: AgentNode[]): RequestTarget | null {
  if (!r.is_hitl || !r.terminal_agent) return null;
  const a = flattenAgents(agents).find((n) => n.name === r.terminal_agent);
  if (a?.session?.tmux_alive) return { kind: "terminal", agent: a.name };
  const paused = a?.session?.state === "paused" || a?.session?.state === "interrupted";
  return { kind: "unavailable", hint: paused ? C.orchestratorPaused : C.orchestratorNotRunning };
}
