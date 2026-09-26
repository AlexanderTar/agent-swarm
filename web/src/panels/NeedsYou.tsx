import { type ReactNode, useRef } from "react";
import { errorText } from "../api";
import { Segmented } from "../components/Segmented";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useAgents, useRequests } from "../data/queries";
import { filterRequests, needsYouRow, pickRequest, requestTarget } from "../logic/inbox";
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
  const agents = useAgents();
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));
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

  const filtered = filterRequests(requests.data ?? [], p.filter);
  const selectedReq = p.selected ? (requests.data ?? []).find((r) => r.id === p.selected) : undefined;
  const list = (p.filter === "all" && selectedReq && !filtered.some((r) => r.id === selectedReq.id))
    ? [selectedReq, ...filtered]
    : filtered;
  const current = (p.filter === "all" ? selectedReq : undefined) ?? list.find((r) => r.id === p.selected) ?? pickRequest(list, "");
  const resolved = p.selected !== "" && !selectedReq && requests.data !== undefined;
  const known = seen.current.get(p.selected);

  return (
    <div className="flex h-full min-h-0">
      <div className="w-[280px] shrink-0 space-y-2 overflow-y-auto border-r border-line p-3">
        <h2 className="font-semibold">{C.needsYou}</h2>
        <Segmented
          label={C.needsYou}
          value={p.filter}
          onChange={p.onFilter}
          options={[
            { value: "all", label: C.all },
            { value: "questions", label: C.questions },
            { value: "approvals", label: C.approvals },
            { value: "reviews", label: C.reviews },
          ]}
        />
        {requests.data && list.length === 0 && <p className="text-muted">{C.inboxEmpty}</p>}
        <ul aria-label={C.needsYou} className="space-y-1">
          {list.map((r) => {
            const [line1, line2, line3] = needsYouRow(r);
            const target = requestTarget(r, agents.data ?? []);
            const isCurrent = r.id === current?.id;
            return (
              <li
                key={r.id}
                className={`rounded bg-warn/15 px-2 py-1 ${isCurrent ? "ring-1 ring-warn" : ""}`}
              >
                <div className="flex items-start gap-2">
                  <button
                    type="button"
                    aria-current={isCurrent}
                    onClick={() => p.onSelectRequest(r.id)}
                    className="min-w-0 flex-1 text-left"
                  >
                    <span className="block truncate text-muted">{line1}</span>
                    <span className="block truncate">{line2}</span>
                    <span className="block truncate">{line3}</span>
                  </button>
                  {target && (
                    <button
                      type="button"
                      aria-label={C.openAgentTerminal}
                      disabled={target.kind === "unavailable"}
                      onClick={() => { if (target.kind === "terminal") void terminal.run(target.agent).catch(() => undefined); }}
                      className="shrink-0 disabled:opacity-40"
                    >
                      ▶
                    </button>
                  )}
                </div>
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
