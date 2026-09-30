import { Badge } from "@/components/ui/badge";
import { STATUS_LABEL } from "../copy";
import { type DisplayState, stateLabel, stateTone } from "../logic/agentActions";
import type { ItemStatus } from "../types";

const TONE: Record<ItemStatus, "outline" | "warning" | "info" | "success"> = {
  draft: "outline",
  ready: "outline",
  in_progress: "info",
  blocked: "warning",
  in_review: "outline",
  awaiting_approval: "warning",
  done: "success",
  cancelled: "outline",
};

export const StatusPill = ({ status }: { status: ItemStatus }) => (
  <Badge variant={TONE[status]} data-tone={TONE[status]} className={status === "cancelled" ? "line-through" : undefined}>
    {STATUS_LABEL[status]}
  </Badge>
);

const DOT = {
  green: "bg-success",
  "green-hollow": "border-2 border-success",
  grey: "bg-muted-foreground",
  "grey-pulse": "bg-muted-foreground animate-pulse motion-reduce:animate-none",
  amber: "bg-warning",
  hollow: "border-2 border-muted-foreground",
  red: "bg-destructive",
} as const;

export function StateDot({ state, withLabel = false }: { state: DisplayState; withLabel?: boolean }) {
  const label = stateLabel(state);
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-label={withLabel ? undefined : label} role={withLabel ? undefined : "img"} className={`inline-block size-2 rounded-full ${DOT[stateTone(state)]}`} />
      {withLabel && <span className="text-muted-foreground">{label}</span>}
    </span>
  );
}
