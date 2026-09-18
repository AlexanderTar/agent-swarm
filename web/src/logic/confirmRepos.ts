import { C } from "../copy";
import type { ConfirmReposBody, ConfirmReposOptions, Repo, Request } from "../types";
import { repoSubtitle } from "./repos";

export interface ConfirmRow { id: string; repo: Repo | null; name: string; subtitle: string; reason: string | null; youSelected: boolean }
export interface ConfirmModel { proposed: ConfirmRow[]; additions: ConfirmRow[]; initial: string[]; version: number }

function row(id: string, reason: string | null, youSelected: boolean, known: Repo[]): ConfirmRow {
  const repo = known.find((r) => r.id === id) ?? null;
  return { id, repo, name: repo?.name ?? id, subtitle: repo ? repoSubtitle(repo) : "", reason, youSelected };
}

export function confirmModel(r: Request, known: Repo[]): ConfirmModel {
  const o = r.options as ConfirmReposOptions;
  const proposed = o.proposed.map((p) => row(p.repo, p.reason || null, p.source === "user", known));
  const additions = o.expansion.map((e) => row(e.repo, e.reason || null, false, known));
  const version = (r.binding as { repos_version?: number } | null)?.repos_version ?? 0;
  return { proposed, additions, initial: proposed.map((p) => p.id), version };
}

export const confirmError = (checked: string[]) => (checked.length === 0 ? C.chooseRepo : undefined);

export function confirmPayload(checked: string[], comment: string, version: number): ConfirmReposBody {
  const c = comment.trim();
  return c ? { repos: checked, comment: c, repos_version: version } : { repos: checked, repos_version: version };
}
