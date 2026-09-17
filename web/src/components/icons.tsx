import { BookOpen, Bug, FlaskConical, Layers, Square } from "lucide-react";
import type { ComponentType } from "react";
import agyUrl from "../assets/agents/agy.svg";
import claudeUrl from "../assets/agents/claude.svg";
import codexUrl from "../assets/agents/codex.svg";
import cursorUrl from "../assets/agents/cursor.svg";
import { AGENT_LABEL, TYPE_LABEL } from "../copy";
import type { AgentKind, ItemType } from "../types";

const AGENT_URL: Record<AgentKind, string> = { claude: claudeUrl, codex: codexUrl, agy: agyUrl, cursor: cursorUrl, fake: claudeUrl };

export function AgentIcon({ kind, className = "" }: { kind: AgentKind; className?: string }) {
  const mask = `url("${AGENT_URL[kind]}") center / contain no-repeat`;
  return (
    <span
      role="img"
      aria-label={AGENT_LABEL[kind]}
      className={`inline-block size-3.5 shrink-0 bg-current ${className}`}
      style={{ mask, WebkitMask: mask }}
    />
  );
}

const TYPE_ICON: Record<ItemType, ComponentType<{ className?: string; "aria-label"?: string; role?: string }>> = {
  epic: Layers, story: BookOpen, task: Square, bug: Bug, spike: FlaskConical,
};

export function TypeIcon({ type, className = "" }: { type: ItemType; className?: string }) {
  const Icon = TYPE_ICON[type];
  return <Icon role="img" aria-label={TYPE_LABEL[type]} className={`size-3.5 shrink-0 text-muted ${className}`} />;
}

export const Key = ({ children }: { children: string }) => <span className="key">{children}</span>;
