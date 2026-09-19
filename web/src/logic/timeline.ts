import { T } from "../copy";
import type { Advice, Checkpoint, CheckpointKind, GitRef, VerifyEntry } from "../types";
import { formatCost, formatDuration, formatTokens, sha7 } from "./format";

export type TimelineEntry =
  | { kind: "checkpoint"; at: number; checkpoint: Checkpoint }
  | { kind: "advice"; at: number; advice: Advice };

export function mergeTimeline(cps: Checkpoint[], advice: Advice[]): TimelineEntry[] {
  const out: TimelineEntry[] = [
    ...cps.map((c) => ({ kind: "checkpoint" as const, at: c.created_at, checkpoint: c })),
    ...advice.map((a) => ({ kind: "advice" as const, at: a.created_at, advice: a })),
  ];
  return out.sort((a, b) => b.at - a.at);
}

const tokens = (a: Advice) => (a.input_tokens ?? 0) + (a.output_tokens ?? 0);

export const advisorName = (a: Advice) =>
  `${a.advisor_kind}/${a.advisor_model}${a.advisor_effort ? ` (${a.advisor_effort})` : ""}`;

export const adviceTitle = (a: Advice) =>
  T.advice(
    advisorName(a),
    a.duration_ms === null ? "" : formatDuration(a.duration_ms),
    tokens(a) > 0 ? formatTokens(tokens(a)) : "",
    a.cost_usd === null ? "" : formatCost(a.cost_usd),
  );

export function adviceTotals(advice: Advice[]): string | null {
  if (advice.length === 0) return null;
  const t = advice.reduce((n, a) => n + tokens(a), 0);
  const known = advice.filter((a) => a.cost_usd !== null);
  const cost = known.reduce((n, a) => n + (a.cost_usd ?? 0), 0);
  return T.advisorTotals(formatTokens(t), known.length ? formatCost(cost) : "");
}

export const gitLine = (g: GitRef) => `${g.repo} ${g.branch} ${sha7(g.sha)}${g.dirty ? " dirty" : ""}`;
export const verifyLine = (v: VerifyEntry) => `${v.ok ? "✓" : "✗"} ${v.cmd}${v.note ? ` — ${v.note}` : ""}`;
export const kindLabel = (k: CheckpointKind) => k.charAt(0).toUpperCase() + k.slice(1);
