import { ApiError, errorText } from "../api";
import { C, STATUS_LABEL, T } from "../copy";
import { ITEM_STATUSES } from "../types";
import type { Item, ItemStatus } from "../types";

export type Movable = Pick<Item, "key" | "type" | "status" | "status_before_block">;
export type MoveCheck = { ok: true } | { ok: false; reason: string; special?: "accept" | "spike" };
export interface MoveOption { status: ItemStatus; label: string; check: MoveCheck }

const OK: MoveCheck = { ok: true };
const isClosed = (s: ItemStatus) => s === "done" || s === "cancelled";

// Mirrors the daemon's `check()` (internal/items/transition.go) for a user actor, in the same order.
export function checkMove(item: Movable, to: ItemStatus): MoveCheck {
  const from = item.status;
  const generic: MoveCheck = { ok: false, reason: T.genericStatus(STATUS_LABEL[from]) };
  if (to === "awaiting_approval" && item.type !== "spike") return { ok: false, reason: C.awaitingNonSpike };
  if (to === from) return generic;
  if (to === "cancelled") return from === "done" ? generic : OK;
  if (to === "blocked") return isClosed(from) ? generic : OK;
  if (from === "blocked") return to === item.status_before_block ? OK : generic; // only the saved status
  if (isClosed(from)) return to === "ready" ? OK : generic; // reopen only
  if (from === "draft" && to === "ready") return OK;
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
        // in_review means a completed checkpoint exists, so the daemon refuses the user generically
        return from === "in_review" ? generic : { ok: false, reason: T.taskDoneDenied(item.key) };
    }
  }
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
