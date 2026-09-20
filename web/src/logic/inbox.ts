import type { InboxFilter, Request } from "../types";
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
