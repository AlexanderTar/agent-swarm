import { STATUS_LABEL } from "../copy";
import { type DisplayState, stateLabel, stateTone } from "../logic/agentActions";
import type { ItemStatus } from "../types";

const PILL: Record<ItemStatus, string> = {
  draft: "text-muted border-line",
  ready: "text-ink border-line",
  in_progress: "text-accent border-accent/40",
  blocked: "text-bad border-bad/40",
  in_review: "text-warn border-warn/40",
  awaiting_approval: "text-warn border-warn/40",
  done: "text-ok border-ok/40",
  cancelled: "text-muted border-line line-through",
};

export const StatusPill = ({ status }: { status: ItemStatus }) => (
  <span className={`inline-flex items-center rounded border px-1.5 py-px text-[12px] ${PILL[status]}`}>{STATUS_LABEL[status]}</span>
);

const DOT = {
  green: "bg-ok",
  "green-hollow": "border-2 border-ok",
  grey: "bg-muted",
  "grey-pulse": "bg-muted animate-pulse",
  amber: "bg-warn",
  hollow: "border-2 border-muted",
  red: "bg-bad",
} as const;

export function StateDot({ state, withLabel = false }: { state: DisplayState; withLabel?: boolean }) {
  const label = stateLabel(state);
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-label={withLabel ? undefined : label} role={withLabel ? undefined : "img"} className={`inline-block size-2 rounded-full ${DOT[stateTone(state)]}`} />
      {withLabel && <span className="text-muted">{label}</span>}
    </span>
  );
}
