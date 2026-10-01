import { C, T } from "../copy";
import type { ItemMerge } from "../types";

const CHECKS: Record<ItemMerge["checks"], string> = {
  "": C.noChecks, pending: C.checksPending, passing: C.checksPassing, failing: C.checksFailing,
};

function status(m: ItemMerge): string {
  if (m.kind === "kept") return [C.kept, m.note].filter(Boolean).join(" · ");
  if (m.kind === "local") return T.mergedLocally(m.merged_sha ?? "");
  if (m.state === "merged") return C.merged;
  const extra = m.auto_merge ? C.autoMergeOn : m.checks === "failing" ? C.orchestratorFixing : "";
  return [CHECKS[m.checks], extra].filter(Boolean).join(" · ");
}

export function MergeList({ merges }: { merges: ItemMerge[] }) {
  // Kept and merged rows are not awaiting anything; a neutral heading once none is.
  const heading = merges.some((m) => m.state !== "merged") ? C.awaitingMerge : C.repos;
  return (
    <section aria-label={heading} className="border-t border-border pt-3">
      <h3 className="mb-1 font-semibold">{heading}</h3>
      <ul>
        {merges.map((m) => (
          <li key={m.repo} className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="min-w-0 break-words font-medium">{m.repo}</span>
            {m.number !== undefined && <span>#{m.number}</span>}
            <span className="min-w-0 break-words text-muted-foreground">{status(m)}</span>
            {m.url && <a href={m.url} target="_blank" rel="noreferrer" aria-label={`Open ${m.repo} pull request${m.number === undefined ? "" : ` #${m.number}`}`} className="inline-flex size-8 shrink-0 items-center justify-center rounded text-link">↗</a>}
          </li>
        ))}
      </ul>
    </section>
  );
}
