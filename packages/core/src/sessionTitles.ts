import { existsSync, readFileSync, readdirSync, statSync, openSync, readSync, closeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { resolveCursorSession, titleFromUserMessage } from "./cursorSessions.js";
import { resolveAntigravitySession } from "./antigravitySessions.js";
import type { NormalizedHookInput } from "./hooks.js";
import { sessionTaskTitle } from "./hooks.js";

const FALLBACK_TITLE =
  /^(claude|cursor|codex|antigravity|opencode|Subagent|[A-Za-z][\w-]*) · .+(\s\([^)]+\))?$/;

export function isFallbackSessionTitle(title: string): boolean {
  const t = title.trim();
  if (!t || t === "Untitled") return true;
  if (/^(claude|cursor|codex|antigravity|opencode) ·(\s*\([^)]+\))?\s*$/i.test(t)) return true;
  if (/^Subagent · /i.test(t)) return true;
  if (FALLBACK_TITLE.test(t)) return true;
  // Long raw prompts pasted as titles (common for Cursor/Claude subagents)
  if (t.length > 90) return true;
  if (/^You are (implementing|a |an |the )/i.test(t)) return true;
  if (/^Explore \//i.test(t)) return true;
  return false;
}

export function encodeClaudeProjectPath(cwd: string): string {
  return cwd.replace(/\//g, "-");
}

export { encodeCursorProjectPath } from "./cursorSessions.js";

export function claudeTranscriptPath(sessionId: string, cwd: string, home = homedir()): string {
  const projectDir = join(home, ".claude", "projects", encodeClaudeProjectPath(cwd));
  return join(projectDir, `${sessionId}.jsonl`);
}

/** Find Claude jsonl across all project dirs (main session or subagent). */
export function findClaudeTranscriptPath(sessionKey: string, cwd?: string, home = homedir()): string | undefined {
  if (!sessionKey.trim()) return undefined;

  const bare = sessionKey.replace(/^agent-/i, "");
  const names = [...new Set([`${sessionKey}.jsonl`, `agent-${bare}.jsonl`, `${bare}.jsonl`])];

  const tryDir = (projectDir: string): string | undefined => {
    for (const name of names) {
      const direct = join(projectDir, name);
      if (existsSync(direct)) return direct;
    }
    try {
      for (const entry of readdirSync(projectDir)) {
        const entryPath = join(projectDir, entry);
        // Nested: <parentSession>/subagents/agent-<id>.jsonl
        for (const name of names) {
          const nested = join(entryPath, "subagents", name);
          if (existsSync(nested)) return nested;
        }
        // Also: subagents/ directly under project (rare)
        for (const name of names) {
          const sub = join(projectDir, "subagents", name);
          if (existsSync(sub)) return sub;
        }
      }
    } catch {
      /* skip unreadable dirs */
    }
    return undefined;
  };

  if (cwd) {
    const projectDir = join(home, ".claude", "projects", encodeClaudeProjectPath(cwd));
    const hit = tryDir(projectDir);
    if (hit) return hit;
  }

  const projectsRoot = join(home, ".claude", "projects");
  if (!existsSync(projectsRoot)) return undefined;

  for (const project of readdirSync(projectsRoot)) {
    const hit = tryDir(join(projectsRoot, project));
    if (hit) return hit;
  }
  return undefined;
}

function extractMessageText(content: unknown): string | undefined {
  if (typeof content === "string" && content.trim()) return content.trim();
  if (Array.isArray(content)) {
    const parts: string[] = [];
    for (const part of content) {
      if (!part || typeof part !== "object") continue;
      const p = part as { type?: string; text?: string };
      if (p.type === "text" && typeof p.text === "string" && p.text.trim()) parts.push(p.text.trim());
    }
    if (parts.length) return parts.join("\n");
  }
  return undefined;
}

/**
 * First substantive prompt from a transcript (Claude sidechain user turn,
 * Cursor user_query, or nested Agent tool prompt/description for forks).
 */
export function readFirstPromptFromTranscript(transcriptPath: string): string | undefined {
  try {
    if (!transcriptPath || !existsSync(transcriptPath)) return undefined;
    const head = readFileSync(transcriptPath, "utf8").slice(0, 192 * 1024);
    let forkDescription: string | undefined;
    let forkPrompt: string | undefined;

    for (const line of head.split("\n")) {
      if (!line.trim()) continue;
      let obj: Record<string, unknown>;
      try {
        obj = JSON.parse(line) as Record<string, unknown>;
      } catch {
        continue;
      }

      const type = typeof obj.type === "string" ? obj.type : "";
      if (type === "fork-context-ref" || type === "attachment" || type === "ai-title") continue;

      // Claude Code: type=user with message.content string | blocks
      if (type === "user" || obj.role === "user") {
        const message = obj.message as Record<string, unknown> | string | undefined;
        const content =
          typeof message === "string"
            ? message
            : message && typeof message === "object"
              ? message.content
              : obj.content;
        const text = extractMessageText(content);
        if (text) return text;
      }

      // Fork / nested spawn: assistant tool_use Agent with description + prompt
      if (type === "assistant" || obj.role === "assistant") {
        const message = obj.message as Record<string, unknown> | undefined;
        const content = message?.content ?? obj.content;
        if (Array.isArray(content)) {
          for (const part of content) {
            if (!part || typeof part !== "object") continue;
            const p = part as { type?: string; name?: string; input?: Record<string, unknown> };
            if (p.type !== "tool_use") continue;
            if (p.name !== "Agent" && p.name !== "Task") continue;
            const desc = typeof p.input?.description === "string" ? p.input.description.trim() : "";
            const prompt = typeof p.input?.prompt === "string" ? p.input.prompt.trim() : "";
            if (desc && !forkDescription) forkDescription = desc;
            if (prompt && !forkPrompt) forkPrompt = prompt;
          }
        }
      }
    }

    return forkPrompt || forkDescription;
  } catch {
    return undefined;
  }
}

export function readClaudeTranscriptTitle(transcriptPath: string): string | undefined {
  try {
    if (!transcriptPath || !existsSync(transcriptPath)) return undefined;
    const stat = statSync(transcriptPath);
    if (stat.size === 0) return undefined;

    const maxTail = 256 * 1024;
    const readLen = Math.min(stat.size, maxTail);
    const start = Math.max(0, stat.size - readLen);
    const fd = openSync(transcriptPath, "r");
    const buf = Buffer.alloc(readLen);
    readSync(fd, buf, 0, readLen, start);
    closeSync(fd);

    const lines = buf.toString("utf8").split("\n").filter(Boolean);
    for (let i = lines.length - 1; i >= 0; i--) {
      try {
        const obj = JSON.parse(lines[i]!) as { type?: string; aiTitle?: string };
        const title = obj.type === "ai-title" && typeof obj.aiTitle === "string" ? obj.aiTitle.trim() : "";
        if (title) return title;
      } catch {
        /* skip malformed lines */
      }
    }
  } catch {
    /* ignore read errors */
  }
  return undefined;
}

/**
 * Best-effort model id from a Claude transcript (message.model on assistant lines).
 * Scans lines in reverse (cap ~2000) so the most recent model wins.
 */
export function readModelFromClaudeTranscript(transcriptPath: string): string | undefined {
  try {
    if (!transcriptPath || !existsSync(transcriptPath)) return undefined;
    const stat = statSync(transcriptPath);
    if (stat.size === 0) return undefined;

    const maxTail = 512 * 1024;
    const readLen = Math.min(stat.size, maxTail);
    const start = Math.max(0, stat.size - readLen);
    const fd = openSync(transcriptPath, "r");
    const buf = Buffer.alloc(readLen);
    readSync(fd, buf, 0, readLen, start);
    closeSync(fd);

    const lines = buf.toString("utf8").split("\n").filter(Boolean).slice(-2000);
    for (let i = lines.length - 1; i >= 0; i--) {
      try {
        const obj = JSON.parse(lines[i]!) as { type?: string; message?: { model?: unknown } };
        const model = obj.message?.model;
        if (typeof model === "string" && model.trim()) return model.trim();
      } catch {
        /* skip malformed lines */
      }
    }
  } catch {
    /* ignore read errors */
  }
  return undefined;
}

export function readCursorChatTitle(conversationId: string, home = homedir()): string | undefined {
  return resolveCursorSession(conversationId, undefined, home)?.title;
}

export function findCursorParentConversationId(
  sessionKey: string,
  cwd?: string,
  home = homedir(),
): string | undefined {
  const ref = resolveCursorSession(sessionKey, cwd, home);
  if (!ref || ref.kind === "unknown") return undefined;
  if (ref.kind === "subagent") return ref.conversationId;
  if (ref.kind === "agent" || ref.kind === "chat") return ref.conversationId;
  return undefined;
}

/** @deprecated Use resolveCursorSession instead */
export function resolveCursorSessionIds(
  boardSessionId: string,
  cwd?: string,
  home = homedir(),
): { conversationId: string; agentId?: string } {
  const ref = resolveCursorSession(boardSessionId, cwd, home);
  if (!ref) return { conversationId: boardSessionId };
  return { conversationId: ref.conversationId, agentId: ref.agentId };
}

export function readCursorAgentTranscriptTitle(sessionKey: string, cwd?: string, home = homedir()): string | undefined {
  const ref = resolveCursorSession(sessionKey, cwd, home);
  if (ref?.title) return ref.title;
  if (ref?.transcriptPath) {
    const raw = readFirstUserTextFromJsonl(ref.transcriptPath);
    return raw ? titleFromUserMessage(raw) : undefined;
  }
  return undefined;
}

function readFirstUserTextFromJsonl(path: string): string | undefined {
  try {
    if (!existsSync(path)) return undefined;
    const head = readFileSync(path, "utf8").slice(0, 96 * 1024);
    for (const line of head.split("\n")) {
      if (!line.trim()) continue;
      try {
        const obj = JSON.parse(line) as { role?: string; message?: { content?: unknown } };
        if (obj.role !== "user") continue;
        const content = obj.message?.content;
        if (typeof content === "string" && content.trim()) return content.trim();
        if (Array.isArray(content)) {
          for (const part of content) {
            if (part && typeof part === "object" && (part as { type?: string }).type === "text") {
              const text = (part as { text?: string }).text?.trim();
              if (text) return text;
            }
          }
        }
      } catch {
        /* skip */
      }
    }
  } catch {
    /* ignore */
  }
  return undefined;
}

export interface ResolvedSessionTitle {
  title: string;
  fromSession: boolean;
}

export function resolveSessionTaskTitle(input: NormalizedHookInput): ResolvedSessionTitle {
  if (input.sessionTitle?.trim()) {
    return { title: input.sessionTitle.trim(), fromSession: true };
  }

  const boardSessionId = input.agentId?.trim() || input.sessionId;

  let transcriptPath = input.transcriptPath;
  if (!transcriptPath && input.platform === "claude" && boardSessionId) {
    transcriptPath = findClaudeTranscriptPath(boardSessionId, input.cwd);
  }
  if (!transcriptPath && input.platform === "cursor" && boardSessionId) {
    transcriptPath = resolveCursorSession(boardSessionId, input.cwd)?.transcriptPath;
  }
  if (!transcriptPath && input.platform === "antigravity" && boardSessionId) {
    transcriptPath = resolveAntigravitySession(boardSessionId)?.transcriptPath;
  }

  if (transcriptPath) {
    const fromAiTitle = readClaudeTranscriptTitle(transcriptPath);
    if (fromAiTitle) return { title: fromAiTitle, fromSession: true };

    const firstPrompt = readFirstPromptFromTranscript(transcriptPath);
    if (firstPrompt) {
      const titled = titleFromUserMessage(firstPrompt, 100);
      if (titled) return { title: titled, fromSession: true };
    }
  }

  if (input.platform === "cursor" && boardSessionId) {
    const ref = resolveCursorSession(boardSessionId, input.cwd);
    if (ref?.title) return { title: ref.title, fromSession: true };
  }

  if (input.platform === "antigravity" && boardSessionId) {
    const ref = resolveAntigravitySession(boardSessionId);
    if (ref?.title) return { title: ref.title, fromSession: true };
  }

  if (input.agentId && input.task?.trim()) {
    const titled = titleFromUserMessage(input.task, 100) ?? input.task.trim();
    return { title: titled, fromSession: true };
  }

  if (input.prompt?.trim()) {
    const fromPrompt = titleFromUserMessage(input.prompt);
    if (fromPrompt) return { title: fromPrompt, fromSession: true };
  }

  return { title: sessionTaskTitle(input), fromSession: false };
}

export function shouldReplaceSessionTitle(current: string | undefined, next: string, fromSession: boolean): boolean {
  const cur = current?.trim() ?? "";
  if (!next.trim()) return false;
  if (!cur || cur === "Untitled") return true;
  if (!fromSession) return false;
  return isFallbackSessionTitle(cur) || cur !== next.trim();
}
