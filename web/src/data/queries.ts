import type { GraphScope } from "../types";
import { useQuery } from "./hooks";

export const qk = {
  items: "items" as const,
  item: (k: string) => `item:${k}`,
  checkpoints: (k: string) => `checkpoints:${k}`,
  graph: (k: string, s: GraphScope, h: number) => `graph:${k}:${s}:${h}`,
  agents: "agents" as const,
  requests: "requests" as const,
  settings: "settings" as const,
  catalog: "catalog" as const,
  repos: (q: string) => `repos:${q}`,
  artifact: (id: string, rev?: number, section?: string) => `artifact:${id}:${rev ?? ""}:${section ?? ""}`,
  advice: (name: string) => `advice:${name}`,
};

export const useItems = () => useQuery(qk.items, (a) => a.items());
export const useItemDetail = (key: string | null) => useQuery(key && qk.item(key), (a) => a.item(key ?? ""));
export const useCheckpoints = (key: string | null) => useQuery(key && qk.checkpoints(key), (a) => a.checkpoints(key ?? ""));
export const useGraph = (key: string | null, scope: GraphScope, hops: number) =>
  useQuery(key && qk.graph(key, scope, hops), (a) => a.graph(key ?? "", scope, hops));
export const useAgents = () => useQuery(qk.agents, (a) => a.agents("active"));
export const useRequests = () => useQuery(qk.requests, (a) => a.requests());
export const useSettings = () => useQuery(qk.settings, (a) => a.settings());
export const useCatalog = () => useQuery(qk.catalog, (a) => a.catalog());
export const useRepos = (q: string) => useQuery(qk.repos(q), (a) => a.repos(q));
export const useArtifact = (id: string | null, rev?: number, section?: string) =>
  useQuery(id && qk.artifact(id, rev, section), (a) => a.artifact(id ?? "", { revision: rev, section }));
export const useAdvice = (name: string | null) => useQuery(name && qk.advice(name), (a) => a.advice(name ?? ""));
