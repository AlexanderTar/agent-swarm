import { C, T } from "../copy";
import type { Repo, ReposResponse } from "../types";
import { ageLine } from "./format";

export function parentFolder(path: string): string {
  const dir = path.replace(/\/[^/]*\/?$/, "");
  return dir.replace(/^\/Users\/[^/]+/, "~") || "/";
}

export const repoSubtitle = (r: Repo) => [parentFolder(r.path), r.remote_owner ?? ""].filter(Boolean).join(" · ");

export const shortPath = (path: string) => path.replace(/^\/Users\/[^/]+/, "~");

export function chooserRows(r: ReposResponse): Repo[] {
  const byPath = new Map<string, Repo>();
  for (const repo of r.all) if (!repo.missing && !byPath.has(repo.path)) byPath.set(repo.path, repo);
  return [...byPath.values()].sort((a, b) => a.name.localeCompare(b.name) || a.path.localeCompare(b.path));
}

export function reconcileSelection(sel: string[], rows: Repo[]): { selection: string[]; removed: number } {
  const ids = new Set(rows.map((r) => r.id));
  const selection = sel.filter((id) => ids.has(id));
  return { selection, removed: sel.length - selection.length };
}

export function knownRepos(r: ReposResponse): Repo[] {
  const byId = new Map<string, Repo>();
  for (const repo of [...r.recent, ...r.groups.flatMap((g) => g.repos), ...r.all]) byId.set(repo.id, repo);
  return [...byId.values()];
}

export const toggleRepo = (sel: string[], id: string) => (sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id]);

export function selectedLine(sel: string[], known: Repo[]): string {
  if (sel.length === 0) return "";
  return T.selected(sel.map((id) => known.find((r) => r.id === id)?.name ?? id).join(", "));
}

export const scanLine = (r: ReposResponse, now = Date.now()) =>
  r.scanning ? C.scanning : ageLine(r.scanned_at, C.neverScanned, T.scannedAgo, now);
