import { C } from "../copy";
import type { AgentNode, InboxFilter, Request } from "../types";
import { flattenAgents } from "./agentActions";
import { ageCompact } from "./format";
import { requestTitle } from "./requestTitle";

// Every request waiting on the user, minus an approval whose native prompt is already open in a
// terminal (spec 2.2.1): the bound question row already represents it.
export const needsYou = (reqs: Request[]): Request[] =>
  reqs.filter((r) => r.state === "open" && !r.native_pending).sort((a, b) => a.created_at - b.created_at);

const QUESTIONS = new Set(["question", "prompt", "blocker"]);
const REVIEWS = new Set(["accept_epic", "accept_fix"]);

export function filterRequests(reqs: Request[], f: InboxFilter): Request[] {
  const set = needsYou(reqs);
  if (f === "questions") return set.filter((r) => QUESTIONS.has(r.kind));
  if (f === "reviews") return set.filter((r) => REVIEWS.has(r.kind));
  if (f === "approvals") return set.filter((r) => !QUESTIONS.has(r.kind) && !REVIEWS.has(r.kind));
  return set;
}

export const inboxRow = (r: Request, now = Date.now()) => ({
  title: requestTitle(r),
  sub: `${r.item_key} · ${ageCompact(r.created_at, now)}`,
});

// The generic Needs-you row, the same for every kind (spec 2.2.2): never the agent's own text.
export const needsYouRow = (r: Request): [string, string, string] => [
  `${r.item_key} · ${r.item_title}`,
  r.agent_name ?? r.terminal_agent ?? "—",
  C.needsYouMessage,
];

export const pickRequest = (reqs: Request[], id: string) => reqs.find((r) => r.id === id) ?? reqs[0];

export type RequestTarget = { kind: "terminal"; agent: string } | { kind: "unavailable"; hint: string };

// What a Needs-you row's click does. null for a request with no terminal_agent, of any kind
// (21-D5 amended: the is_hitl guard is dropped).
export function requestTarget(r: Request, agents: AgentNode[]): RequestTarget | null {
  if (!r.terminal_agent) return null;
  const a = flattenAgents(agents).find((n) => n.name === r.terminal_agent);
  if (a?.session?.tmux_alive) return { kind: "terminal", agent: a.name };
  const paused = a?.session?.state === "paused" || a?.session?.state === "interrupted";
  return { kind: "unavailable", hint: paused ? C.orchestratorPaused : C.orchestratorNotRunning };
}
