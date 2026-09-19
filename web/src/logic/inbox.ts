import type { InboxFilter, Request } from "../types";
import { ageCompact } from "./format";
import { requestTitle } from "./requestTitle";

export function filterRequests(reqs: Request[], f: InboxFilter): Request[] {
  return reqs
    .filter((r) => f === "all" || (f === "questions" ? r.kind === "question" : r.kind !== "question"))
    .sort((a, b) => a.created_at - b.created_at);
}

export const inboxRow = (r: Request, now = Date.now()) => ({
  title: requestTitle(r),
  sub: `${r.item_key} · ${ageCompact(r.created_at, now)}`,
});

export const pickRequest = (reqs: Request[], id: string) => reqs.find((r) => r.id === id) ?? reqs[0];
