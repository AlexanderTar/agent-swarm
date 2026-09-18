import { type ReactNode, useRef } from "react";
import { errorText } from "../api";
import { Segmented } from "../components/Segmented";
import { C } from "../copy";
import { useRequests } from "../data/queries";
import { filterRequests, inboxRow, pickRequest } from "../logic/inbox";
import type { InboxFilter, Request } from "../types";

export function NeedsYou(p: {
  filter: InboxFilter;
  selected: string;
  connected: boolean;
  onFilter(f: InboxFilter): void;
  onSelectRequest(id: string): void;
  onViewItem(key: string): void;
  renderReview(r: Request): ReactNode;
}) {
  const requests = useRequests();
  const seen = useRef(new Map<string, Request>());
  for (const r of requests.data ?? []) seen.current.set(r.id, r);

  // Standing rule: a failed query gets a message and a retry, never a permanent blank placeholder.
  // The brief's version never read `requests.error`, so a failed load would leave both the list and
  // the review pane empty forever with no indication anything went wrong.
  if (requests.error) {
    return (
      <p className="p-4 text-bad">
        {errorText(requests.error)}{" "}
        <button type="button" className="text-accent underline" onClick={() => requests.reload()}>
          {C.retry}
        </button>
      </p>
    );
  }

  const list = filterRequests(requests.data ?? [], p.filter);
  const current = p.selected ? list.find((r) => r.id === p.selected) : pickRequest(list, "");
  const resolved = p.selected !== "" && !current && requests.data !== undefined;
  const known = seen.current.get(p.selected);

  return (
    <div className="flex h-full min-h-0">
      <div className="w-[280px] shrink-0 space-y-2 overflow-y-auto border-r border-line p-3">
        <h2 className="font-semibold">{C.needsYou}</h2>
        <Segmented
          label={C.needsYou}
          value={p.filter}
          onChange={p.onFilter}
          options={[{ value: "all", label: C.all }, { value: "questions", label: C.questions }, { value: "approvals", label: C.approvals }]}
        />
        {requests.data && list.length === 0 && <p className="text-muted">{C.inboxEmpty}</p>}
        <ul aria-label={C.needsYou} className="space-y-1">
          {list.map((r) => {
            const row = inboxRow(r);
            return (
              <li key={r.id}>
                <button
                  type="button"
                  aria-current={r.id === current?.id}
                  onClick={() => p.onSelectRequest(r.id)}
                  className={`w-full rounded px-2 py-1 text-left hover:bg-raised ${r.id === current?.id ? "bg-raised" : ""}`}
                >
                  <span className="block truncate">● {row.title}</span>
                  <span className="block text-muted">{row.sub}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </div>
      <div className="min-w-0 flex-1 overflow-y-auto p-4">
        {resolved ? (
          <div className="space-y-2">
            <p>{C.resolved}</p>
            {known && (
              <button type="button" onClick={() => p.onViewItem(known.item_key)} className="text-accent">{C.viewItem}</button>
            )}
          </div>
        ) : current ? (
          <div key={current.id}>{p.renderReview(current)}</div>
        ) : requests.data ? (
          <p className="text-muted">{C.inboxEmpty}</p>
        ) : null}
      </div>
    </div>
  );
}
