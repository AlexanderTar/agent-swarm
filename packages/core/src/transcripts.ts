import { existsSync, readdirSync, statSync, openSync, readSync, closeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { HookPlatform, NormalizedHookInput } from "./hooks.js";
import { encodeCursorProjectPath, resolveCursorSession } from "./cursorSessions.js";
import { extractAntigravityTranscriptText, resolveAntigravitySession } from "./antigravitySessions.js";

export function encodeClaudeProjectPath(cwd: string): string {
  return cwd.replace(/\//g, "-");
}

export function claudeTranscriptPath(sessionId: string, cwd: string, home = homedir()): string {
  return join(home, ".claude", "projects", encodeClaudeProjectPath(cwd), `${sessionId}.jsonl`);
}

/** Find a Claude jsonl across all project dirs (main session or subagent). */
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
        // Nested: <parentSession>/subagents/agent-<id>.jsonl
        for (const name of names) {
          const nested = join(projectDir, entry, "subagents", name);
          if (existsSync(nested)) return nested;
        }
        // Also: subagents/ directly under the project (rare)
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
    const hit = tryDir(join(home, ".claude", "projects", encodeClaudeProjectPath(cwd)));
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

export function resolveTranscriptPath(
  input: Pick<NormalizedHookInput, "platform" | "sessionId" | "agentId" | "cwd" | "transcriptPath">,
  boardSessionId: string,
): string | undefined {
  if (input.transcriptPath && existsSync(input.transcriptPath)) return input.transcriptPath;

  const sessionKey = boardSessionId || input.agentId || input.sessionId;
  if (!sessionKey) return undefined;

  if (input.platform === "claude") {
    const path = findClaudeTranscriptPath(sessionKey, input.cwd);
    if (path) return path;
    if (input.sessionId && input.sessionId !== sessionKey) {
      const parent = findClaudeTranscriptPath(input.sessionId, input.cwd);
      if (parent) return parent;
    }
  }

  if (input.platform === "cursor") {
    const ref = resolveCursorSession(sessionKey, input.cwd);
    if (ref?.transcriptPath && existsSync(ref.transcriptPath)) return ref.transcriptPath;

    const projectDir = join(homedir(), ".cursor", "projects", encodeCursorProjectPath(input.cwd), "agent-transcripts");
    const candidates = [
      join(projectDir, sessionKey, `${sessionKey}.jsonl`),
      join(projectDir, input.sessionId, `${input.sessionId}.jsonl`),
      join(projectDir, input.sessionId, "subagents", `${sessionKey}.jsonl`),
      join(projectDir, sessionKey, "subagents", `${sessionKey}.jsonl`),
    ];
    for (const path of candidates) {
      if (existsSync(path)) return path;
    }
    if (existsSync(projectDir)) {
      for (const folder of readdirSync(projectDir)) {
        const direct = join(projectDir, folder, `${sessionKey}.jsonl`);
        if (existsSync(direct)) return direct;
        const sub = join(projectDir, folder, "subagents", `${sessionKey}.jsonl`);
        if (existsSync(sub)) return sub;
      }
    }
  }

  if (input.platform === "antigravity") {
    const ref = resolveAntigravitySession(sessionKey);
    if (ref?.transcriptPath && existsSync(ref.transcriptPath)) return ref.transcriptPath;
  }

  return undefined;
}

export function readFileTail(path: string, maxBytes = 512 * 1024): string {
  try {
    const stat = statSync(path);
    if (stat.size === 0) return "";
    const readLen = Math.min(stat.size, maxBytes);
    const start = Math.max(0, stat.size - readLen);
    const fd = openSync(path, "r");
    const buf = Buffer.alloc(readLen);
    readSync(fd, buf, 0, readLen, start);
    closeSync(fd);
    return buf.toString("utf8");
  } catch {
    return "";
  }
}

function stripNoise(text: string): string {
  return text
    .replace(/<\/?[a-zA-Z0-9_-]+>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function extractTextFromCursorLine(obj: Record<string, unknown>): string {
  const message = obj.message as { content?: unknown } | undefined;
  const content = message?.content;
  if (typeof content === "string") return stripNoise(content);
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (!part || typeof part !== "object") return "";
        const p = part as { type?: string; text?: string };
        return p.type === "text" && typeof p.text === "string" ? stripNoise(p.text) : "";
      })
      .filter(Boolean)
      .join("\n");
  }
  return "";
}

function extractTextFromClaudeLine(obj: Record<string, unknown>): string {
  if (obj.type === "user" || obj.type === "assistant") {
    const message = obj.message as { content?: unknown } | undefined;
    if (!message) return "";
    if (typeof message.content === "string") return stripNoise(message.content);
    if (Array.isArray(message.content)) {
      return message.content
        .map((part) => {
          if (!part || typeof part !== "object") return "";
          const p = part as { type?: string; text?: string };
          if (p.type === "text" && typeof p.text === "string") return stripNoise(p.text);
          return "";
        })
        .filter(Boolean)
        .join("\n");
    }
  }
  return "";
}

export function extractTranscriptText(platform: HookPlatform, sourcePath: string, maxChars = 24_000): string {
  if (platform === "antigravity" && existsSync(sourcePath)) {
    return extractAntigravityTranscriptText(sourcePath, maxChars);
  }

  const lines: string[] = [];
  const tail = readFileTail(sourcePath);
  if (!tail) return "";

  for (const line of tail.split("\n")) {
    if (!line.trim()) continue;
    try {
      const obj = JSON.parse(line) as Record<string, unknown>;
      let text = "";
      if (platform === "cursor" || obj.role === "user" || obj.role === "assistant") {
        text = extractTextFromCursorLine(obj);
        if (!text && typeof obj.role === "string") {
          const msg = obj as { role?: string; message?: { content?: string } };
          text = stripNoise(String(msg.message?.content ?? ""));
        }
      } else {
        text = extractTextFromClaudeLine(obj);
      }
      if (text) {
        const role =
          obj.role === "user" || obj.type === "user"
            ? "User"
            : obj.role === "assistant" || obj.type === "assistant"
              ? "Assistant"
              : "Agent";
        lines.push(`${role}: ${text.slice(0, 2000)}`);
      }
    } catch {
      /* skip malformed jsonl */
    }
  }

  const joined = lines.join("\n");
  return joined.length > maxChars ? joined.slice(joined.length - maxChars) : joined;
}
