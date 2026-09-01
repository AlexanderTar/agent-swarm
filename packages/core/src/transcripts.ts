import { existsSync, readFileSync, readdirSync, statSync, openSync, readSync, closeSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { HookPlatform, NormalizedHookInput } from "./hooks.js";
import { resolveCursorSession } from "./cursorSessions.js";
import { extractAntigravityTranscriptText, resolveAntigravitySession } from "./antigravitySessions.js";
import { findClaudeTranscriptPath, encodeCursorProjectPath } from "./sessionTitles.js";

export { encodeCursorProjectPath } from "./sessionTitles.js";

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

export function eventsToTranscript(
  events: Array<{ eventType: string; payloadJson?: string }>,
  maxChars = 8000,
): string {
  const lines: string[] = [];
  for (const event of events.slice(-40)) {
    let payload: Record<string, unknown> = {};
    try {
      payload = event.payloadJson ? (JSON.parse(event.payloadJson) as Record<string, unknown>) : {};
    } catch {
      payload = {};
    }
    if (event.eventType === "prompt" && typeof payload.prompt === "string") {
      lines.push(`User: ${stripNoise(payload.prompt).slice(0, 800)}`);
    } else if (event.eventType === "stop_summary" && typeof payload.summary === "string") {
      lines.push(`Assistant: ${stripNoise(payload.summary).slice(0, 800)}`);
    } else if (payload.tool && typeof payload.tool === "string") {
      lines.push(`Tool (${payload.tool})`);
    }
  }
  const joined = lines.join("\n");
  return joined.length > maxChars ? joined.slice(joined.length - maxChars) : joined;
}

export function buildTranscriptContext(
  input: NormalizedHookInput,
  boardSessionId: string,
  events: Array<{ eventType: string; payloadJson?: string }>,
  lastAssistantMessage?: string,
): string {
  const parts: string[] = [];
  const path = resolveTranscriptPath(input, boardSessionId);
  if (path) {
    const fromFile = extractTranscriptText(input.platform, path);
    if (fromFile) parts.push(fromFile);
  }
  const fromEvents = eventsToTranscript(events);
  if (fromEvents) parts.push(fromEvents);
  if (lastAssistantMessage?.trim()) {
    parts.push(`Assistant (latest): ${stripNoise(lastAssistantMessage).slice(0, 1500)}`);
  }
  if (input.prompt?.trim()) {
    parts.push(`User (latest): ${stripNoise(input.prompt).slice(0, 1500)}`);
  }
  return parts.join("\n\n").slice(-24_000);
}
