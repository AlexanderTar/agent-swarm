import { existsSync, readFileSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function encodeCursorProjectPath(cwd: string): string {
  return cwd.replace(/^\//, "").replace(/\//g, "-");
}

export type CursorSessionKind = "chat" | "agent" | "subagent" | "unknown";

export interface CursorSessionRef {
  boardSessionId: string;
  conversationId: string;
  agentId?: string;
  kind: CursorSessionKind;
  title?: string;
  cwd?: string;
  transcriptPath?: string;
}

export function titleFromUserMessage(text: string, maxLen = 120): string | undefined {
  let t = text
    .replace(/<timestamp>[\s\S]*?<\/timestamp>/gi, "")
    .replace(/<\/?user_query>/gi, "")
    .replace(/<\/?[a-zA-Z0-9_-]+>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!t) return undefined;
  const firstLine = t.split(/(?<=[.!?])\s+|\n+/).map((l) => l.trim()).find(Boolean) ?? t;
  const line = firstLine.length > maxLen ? `${firstLine.slice(0, maxLen - 1)}…` : firstLine;
  return line.trim() || undefined;
}

function listCursorTranscriptRoots(hintCwd: string | undefined, home: string): string[] {
  const roots = new Set<string>();
  if (hintCwd) {
    roots.add(join(home, ".cursor", "projects", encodeCursorProjectPath(hintCwd), "agent-transcripts"));
  }
  const projectsRoot = join(home, ".cursor", "projects");
  if (existsSync(projectsRoot)) {
    for (const project of readdirSync(projectsRoot)) {
      roots.add(join(projectsRoot, project, "agent-transcripts"));
    }
  }
  return [...roots];
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

function readCursorPromptHistoryTitle(chatDir: string): string | undefined {
  try {
    const path = join(chatDir, "prompt_history.json");
    if (!existsSync(path)) return undefined;
    const history = JSON.parse(readFileSync(path, "utf8")) as unknown;
    if (!Array.isArray(history)) return undefined;
    for (let i = history.length - 1; i >= 0; i--) {
      const entry = history[i];
      if (typeof entry === "string" && entry.trim()) {
        return titleFromUserMessage(entry);
      }
    }
  } catch {
    /* ignore */
  }
  return undefined;
}

function readChatSession(boardSessionId: string, hintCwd: string | undefined, home: string): CursorSessionRef | undefined {
  const chatsRoot = join(home, ".cursor", "chats");
  if (!existsSync(chatsRoot)) return undefined;

  let best: CursorSessionRef | undefined;
  for (const workspaceHash of readdirSync(chatsRoot)) {
    const chatDir = join(chatsRoot, workspaceHash, boardSessionId);
    const metaPath = join(chatDir, "meta.json");
    if (!existsSync(metaPath)) continue;

    const meta = JSON.parse(readFileSync(metaPath, "utf8")) as { title?: string; cwd?: string };
    const metaTitle = typeof meta.title === "string" ? meta.title.trim() : "";
    const title = metaTitle || readCursorPromptHistoryTitle(chatDir);
    const cwd = typeof meta.cwd === "string" && meta.cwd.trim() ? meta.cwd.trim() : hintCwd;
    const ref: CursorSessionRef = {
      boardSessionId,
      conversationId: boardSessionId,
      kind: "chat",
      title: title || undefined,
      cwd,
    };

    if (hintCwd && cwd === hintCwd) return ref;
    best ??= ref;
  }
  return best;
}

function readAgentTranscriptSession(
  boardSessionId: string,
  hintCwd: string | undefined,
  home: string,
): CursorSessionRef | undefined {
  for (const projectDir of listCursorTranscriptRoots(hintCwd, home)) {
    if (!existsSync(projectDir)) continue;

    const mainPath = join(projectDir, boardSessionId, `${boardSessionId}.jsonl`);
    if (existsSync(mainPath)) {
      const raw = readFirstUserTextFromJsonl(mainPath);
      return {
        boardSessionId,
        conversationId: boardSessionId,
        kind: "agent",
        title: raw ? titleFromUserMessage(raw) : undefined,
        cwd: hintCwd,
        transcriptPath: mainPath,
      };
    }

    try {
      for (const folder of readdirSync(projectDir)) {
        const subPath = join(projectDir, folder, "subagents", `${boardSessionId}.jsonl`);
        if (!existsSync(subPath)) continue;
        const raw = readFirstUserTextFromJsonl(subPath);
        return {
          boardSessionId,
          conversationId: folder,
          agentId: boardSessionId,
          kind: "subagent",
          title: raw ? titleFromUserMessage(raw) : undefined,
          cwd: hintCwd,
          transcriptPath: subPath,
        };
      }
    } catch {
      /* skip unreadable dirs */
    }
  }
  return undefined;
}

/** Resolve any Cursor board session id (chat, agent mode, or subagent) to title/cwd/transcript. */
export function resolveCursorSession(
  boardSessionId: string,
  hintCwd?: string,
  home = homedir(),
): CursorSessionRef | undefined {
  if (!boardSessionId.trim()) return undefined;

  const chat = readChatSession(boardSessionId, hintCwd, home);
  const agent = readAgentTranscriptSession(boardSessionId, hintCwd, home);

  if (chat?.title && agent?.title) {
    if (hintCwd && chat.cwd === hintCwd) return { ...chat, transcriptPath: agent.transcriptPath ?? chat.transcriptPath };
    if (hintCwd && agent.cwd === hintCwd) return agent;
    return chat.title.length >= agent.title.length ? { ...chat, transcriptPath: agent.transcriptPath } : agent;
  }
  if (chat) return agent?.transcriptPath ? { ...chat, transcriptPath: agent.transcriptPath } : chat;
  if (agent) return agent;

  return { boardSessionId, conversationId: boardSessionId, kind: "unknown", cwd: hintCwd };
}
