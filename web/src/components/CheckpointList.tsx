import { errorText } from "../api";
import { C } from "../copy";
import { useQuery } from "../data/hooks";
import { qk, useCheckpoints } from "../data/queries";
import { formatTime } from "../logic/format";
import { adviceTitle, adviceTotals, gitLine, kindLabel, mergeTimeline, verifyLine } from "../logic/timeline";
import type { Checkpoint } from "../types";

function Lines({ title, lines }: { title: string; lines: string[] }) {
  if (lines.length === 0) return null;
  return (
    <div>
      <p className="text-muted">{title}</p>
      <ul className="list-disc pl-5">{lines.map((l) => <li key={l}>{l}</li>)}</ul>
    </div>
  );
}

function CheckpointRow({ c }: { c: Checkpoint }) {
  return (
    <details className="rounded border border-line p-2">
      <summary className="cursor-pointer">
        <span className="text-muted">{formatTime(c.created_at)}</span>{" "}
        <span>{c.agent_name ?? ""}</span>{" "}
        <span className="rounded bg-raised px-1 text-[12px]">{kindLabel(c.kind)}</span>{" "}
        {c.daemon_written && <span className="text-[12px] text-warn">{C.writtenBySwarm}</span>}{" "}
        <span>{c.summary}</span>
      </summary>
      <div className="mt-2 space-y-1">
        <Lines title="Next" lines={c.next} />
        <Lines title="Blockers" lines={c.blockers} />
        <Lines title="Git" lines={c.git.map(gitLine)} />
        <Lines title="Verification" lines={c.verification.map(verifyLine)} />
      </div>
    </details>
  );
}

export function CheckpointList({ itemKey, agentNames }: { itemKey: string; agentNames: string[] }) {
  const cps = useCheckpoints(itemKey);
  const names = agentNames.join(",");
  const advice = useQuery(`${qk.advice(itemKey)}:${names}`, async (api) => {
    const lists = await Promise.all(agentNames.map((n) => api.advice(n)));
    return lists.flat().filter((a) => a.item_key === itemKey);
  });
  // R1 Important: a failed load must not look like a slow one — surface it with the daemon's own
  // reason (via errorText, contracts §2) and a way to try again, the same pattern T24 will copy for
  // the details panel.
  const err = cps.error ?? advice.error;
  if (err) {
    return (
      <p className="text-bad">
        {errorText(err)}{" "}
        <button
          type="button"
          className="text-accent underline"
          onClick={() => {
            cps.reload();
            advice.reload();
          }}
        >
          {C.retry}
        </button>
      </p>
    );
  }
  if (!cps.data || !advice.data) return <p className="text-muted">…</p>;
  const entries = mergeTimeline(cps.data, advice.data);
  if (entries.length === 0) return <p className="text-muted">{C.noCheckpoints}</p>;
  const totals = adviceTotals(advice.data);
  return (
    <div className="space-y-2">
      {totals && <p className="text-muted">{totals}</p>}
      {entries.map((e) =>
        e.kind === "checkpoint" ? (
          <CheckpointRow key={e.checkpoint.id} c={e.checkpoint} />
        ) : (
          <details key={e.advice.id} className="rounded border border-line p-2">
            <summary className="cursor-pointer">{adviceTitle(e.advice)}</summary>
            <p className="mt-2 font-medium">{e.advice.question}</p>
            <p>{e.advice.answer ?? e.advice.error ?? ""}</p>
          </details>
        ),
      )}
    </div>
  );
}
