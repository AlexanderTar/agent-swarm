import type { AgentNode, CardLevel, Filter, Grouping, Item, Request } from "../types";

export interface ViewProps {
  items: Item[];
  loaded: boolean; // false until the first GET /api/items returns; views show no empty state before that
  filter: Filter;
  selected: string;
  onSelect(key: string, focus?: "agents"): void;
  connected: boolean;
}

export interface HierarchyProps extends ViewProps {
  onAddChild(parentKey: string, type: "story" | "task"): void;
  onNewItem(): void;
  onClearFilters(): void;
}

export interface KanbanProps extends ViewProps {
  agents: AgentNode[];
  requests: Request[];
  level: CardLevel;
  group: Grouping;
  onLevel(level: CardLevel): void;
  onReview(requestId: string): void;
  onClearFilters(): void;
  onNewItem(): void;
}

export type DependenciesProps = ViewProps;

export interface DetailsProps {
  itemKey: string;
  focus?: "agents";
  connected: boolean;
  onClose(): void;
  onSelect(key: string): void;
  onReview(requestId: string): void;
  onStartOrchestrator(item: Item): void;
}

export type SheetState =
  | null
  | { kind: "spike"; caption?: string }
  | { kind: "item"; type: "epic" | "bug" | "story" | "task"; parentKey?: string }
  | { kind: "spawn"; itemKey: string };
