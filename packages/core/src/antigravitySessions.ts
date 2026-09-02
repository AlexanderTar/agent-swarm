import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export interface AntigravitySessionRef {
  conversationId: string;
  cwd?: string;
  transcriptPath?: string;
}

function brainRoot(home = homedir()): string {
  return join(home, ".gemini", "antigravity-cli", "brain");
}

function extractUserRequest(content: string): string {
  const match = content.match(/<USER_REQUEST>\s*([\s\S]*?)\s*<\/USER_REQUEST>/i);
  return (match?.[1] ?? content).trim();
}

function inferCwdFromTranscriptLine(obj: Record<string, unknown>): string | undefined {
  const toolCalls = obj.tool_calls as Array<{ args?: Record<string, unknown> }> | undefined;
  if (Array.isArray(toolCalls)) {
    for (const call of toolCalls) {
      const dir = call.args?.DirectoryPath ?? call.args?.Cwd;
      if (typeof dir === "string" && dir.startsWith("/")) return dir.replace(/"/g, "");
    }
  }
  return undefined;
}

function readTranscriptCwd(path: string): string | undefined {
  try {
    if (!existsSync(path)) return undefined;
    const head = readFileSync(path, "utf8").slice(0, 128 * 1024);
    for (const line of head.split("\n")) {
      if (!line.trim()) continue;
      try {
        const cwd = inferCwdFromTranscriptLine(JSON.parse(line) as Record<string, unknown>);
        if (cwd) return cwd;
      } catch {
        /* skip */
      }
    }
  } catch {
    /* ignore */
  }
  return undefined;
}

function resolveTranscriptFile(conversationId: string, home = homedir()): string | undefined {
  const base = join(brainRoot(home), conversationId, ".system_generated", "logs");
  const full = join(base, "transcript_full.jsonl");
  if (existsSync(full)) return full;
  const compact = join(base, "transcript.jsonl");
  if (existsSync(compact)) return compact;
  const messages = join(brainRoot(home), conversationId, ".system_generated", "messages");
  if (existsSync(messages)) return messages;
  return undefined;
}

/** Resolve an Antigravity brain conversation to its working directory and transcript path. */
export function resolveAntigravitySession(conversationId: string, home = homedir()): AntigravitySessionRef | undefined {
  if (!conversationId.trim()) return undefined;
  const transcriptPath = resolveTranscriptFile(conversationId, home);
  if (!transcriptPath) return { conversationId };
  return {
    conversationId,
    cwd: transcriptPath.endsWith(".jsonl") ? readTranscriptCwd(transcriptPath) : undefined,
    transcriptPath,
  };
}

export function extractAntigravityTranscriptText(sourcePath: string, maxChars = 24_000): string {
  if (!existsSync(sourcePath)) return "";
  if (statIsDirectory(sourcePath)) {
    return extractAntigravityMessagesDir(sourcePath, maxChars);
  }
  return extractAntigravityJsonlFile(sourcePath, maxChars);
}

function statIsDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

function extractAntigravityMessagesDir(dir: string, maxChars: number): string {
  const lines: string[] = [];
  for (const file of readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .slice(-80)) {
    try {
      const obj = JSON.parse(readFileSync(join(dir, file), "utf8")) as {
        content?: string;
        renderDetails?: { messageTitle?: string };
      };
      const title = obj.renderDetails?.messageTitle?.trim();
      const body = typeof obj.content === "string" ? obj.content.slice(0, 800) : "";
      if (title || body) lines.push([title ? `[${title}]` : "", body].filter(Boolean).join(" "));
    } catch {
      /* skip */
    }
  }
  const joined = lines.join("\n");
  return joined.length > maxChars ? joined.slice(joined.length - maxChars) : joined;
}

function extractAntigravityJsonlFile(path: string, maxChars: number): string {
  const lines: string[] = [];
  try {
    const tail = readFileSync(path, "utf8").slice(-maxChars * 2);
    for (const line of tail.split("\n")) {
      if (!line.trim()) continue;
      try {
        const obj = JSON.parse(line) as { type?: string; content?: string; thinking?: string };
        if (obj.type === "USER_INPUT" && typeof obj.content === "string") {
          lines.push(`User: ${extractUserRequest(obj.content).slice(0, 500)}`);
        } else if (typeof obj.content === "string" && obj.content.trim()) {
          lines.push(`Agent: ${obj.content.slice(0, 400)}`);
        } else if (typeof obj.thinking === "string" && obj.thinking.trim()) {
          lines.push(`Agent: ${obj.thinking.slice(0, 300)}`);
        }
      } catch {
        /* skip */
      }
    }
  } catch {
    /* ignore */
  }
  const joined = lines.join("\n");
  return joined.length > maxChars ? joined.slice(joined.length - maxChars) : joined;
}
