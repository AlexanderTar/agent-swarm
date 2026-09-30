import { C, T } from "../copy";
import type { ItemMerge } from "../types";

const CHECKS: Record<ItemMerge["checks"], string> = {
  "": C.noChecks, pending: C.checksPending, passing: C.checksPassing, failing: C.checksFailing,
};

function status(m: ItemMerge): string {
  if (m.kind === "local") return T.mergedLocally(m.merged_sha ?? "");
  if (m.state === "merged") return C.merged;
  const extra = m.auto_merge ? C.autoMergeOn : m.checks === "failing" ? C.orchestratorFixing : "";
  return [CHECKS[m.checks], extra].filter(Boolean).join(" · ");
}

export function MergeList({ merges }: { merges: ItemMerge[] }) {
  return (
    <section aria-label={C.awaitingMerge} className="border-t border-border pt-3">
      <h3 className="mb-1 font-semibold">{C.awaitingMerge}</h3>
      <ul>
        {merges.map((m) => (
          <li key={m.repo} className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="min-w-0 break-words font-medium">{m.repo}</span>
            {m.number !== undefined && <span>#{m.number}</span>}
            <span className="text-muted-foreground">{status(m)}</span>
            {m.url && <a href={m.url} target="_blank" rel="noreferrer" aria-label={`Open ${m.repo} pull request${m.number === undefined ? "" : ` #${m.number}`}`} className="inline-flex size-8 shrink-0 items-center justify-center rounded text-link">↗</a>}
          </li>
        ))}
      </ul>
    </section>
  );
}
