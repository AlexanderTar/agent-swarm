import { C, T } from "../copy";
import type { Repo, ReposResponse } from "../types";
import { ageCompact } from "./format";

export interface RepoSection { id: string; title: string; groupName: string | null; repos: Repo[] }

export function parentFolder(path: string): string {
  const dir = path.replace(/\/[^/]*\/?$/, "");
  return dir.replace(/^\/Users\/[^/]+/, "~") || "/";
}

export const repoSubtitle = (r: Repo) => [parentFolder(r.path), r.remote_owner ?? ""].filter(Boolean).join(" · ");

export function repoSections(r: ReposResponse): RepoSection[] {
  const merged = new Map<string, { local: boolean; repos: Map<string, Repo> }>();
  for (const g of r.groups) {
    const m = merged.get(g.name) ?? { local: false, repos: new Map<string, Repo>() };
    m.local ||= g.source !== "remote_owner";
    for (const repo of g.repos) m.repos.set(repo.id, repo);
    merged.set(g.name, m);
  }
  const groups = [...merged.entries()].sort(
    ([an, a], [bn, b]) => Number(b.local) - Number(a.local) || an.localeCompare(bn),
  );
  const out: RepoSection[] = [];
  if (r.recent.length) out.push({ id: "recent", title: C.recent, groupName: null, repos: r.recent });
  for (const [name, g] of groups) out.push({ id: `group:${name}`, title: name, groupName: name, repos: [...g.repos.values()] });
  if (r.all.length) out.push({ id: "all", title: C.all, groupName: null, repos: r.all });
  return out;
}

export function knownRepos(r: ReposResponse): Repo[] {
  const byId = new Map<string, Repo>();
  for (const repo of [...r.recent, ...r.groups.flatMap((g) => g.repos), ...r.all]) byId.set(repo.id, repo);
  return [...byId.values()];
}

export const toggleRepo = (sel: string[], id: string) => (sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id]);

export const selectAll = (sel: string[], repos: Repo[]) => [
  ...sel,
  ...repos.filter((r) => !r.missing && !sel.includes(r.id)).map((r) => r.id),
];

export function selectedLine(sel: string[], known: Repo[]): string {
  if (sel.length === 0) return "";
  return T.selected(sel.map((id) => known.find((r) => r.id === id)?.name ?? id).join(", "));
}

export const scanLine = (r: ReposResponse, now = Date.now()) =>
  r.scanning ? C.scanning : T.scannedAgo(ageCompact(r.scanned_at, now));
