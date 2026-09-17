import { ApiError, errorText } from "../api";
import { C, STATUS_LABEL, T } from "../copy";
import { ITEM_STATUSES } from "../types";
import type { Item, ItemStatus } from "../types";

export type Movable = Pick<Item, "key" | "type" | "status">;
export type MoveCheck = { ok: true } | { ok: false; reason: string; special?: "accept" | "spike" };
export interface MoveOption { status: ItemStatus; label: string; check: MoveCheck }

const OK: MoveCheck = { ok: true };
const isClosed = (s: ItemStatus) => s === "done" || s === "cancelled";

export function checkMove(item: Movable, to: ItemStatus): MoveCheck {
  const from = item.status;
  const generic: MoveCheck = { ok: false, reason: T.genericStatus(STATUS_LABEL[from]) };
  if (to === from) return generic;
  if (isClosed(from)) return to === "ready" ? OK : generic; // reopen only
  if (to === "awaiting_approval") return item.type === "spike" ? generic : { ok: false, reason: C.awaitingNonSpike };
  if (to === "done") {
    switch (item.type) {
      case "epic":
        return { ok: false, reason: C.epicDone, special: "accept" };
      case "bug":
        return { ok: false, reason: C.bugDone, special: "accept" };
      case "spike":
        return { ok: false, reason: C.spikeDone, special: "spike" };
      case "story":
        return { ok: false, reason: T.storyDoneDenied(item.key) };
      case "task":
        return from === "in_review" ? OK : { ok: false, reason: T.taskDoneDenied(item.key) };
    }
  }
  if (to === "cancelled" || to === "blocked") return OK;
  if (item.type === "story") return generic; // derived (I1)
  if (from === "blocked") return OK; // the daemon restores status_before_block and refuses anything else
  if (from === "draft" && to === "ready") return OK;
  return generic; // everything else is daemon- or orchestrator-only
}

export function moveOptions(item: Movable): MoveOption[] {
  return ITEM_STATUSES.filter((s) => s !== item.status).map((s) => ({
    status: s,
    label: STATUS_LABEL[s],
    check: checkMove(item, s),
  }));
}

export function failureMessage(err: unknown, item: Movable): string {
  if (err instanceof ApiError) {
    if (err.code === "transition_denied") return errorText(err);
    if (err.code === "conflict") return C.staleRevision;
  }
  return T.genericStatus(STATUS_LABEL[item.status]);
}
