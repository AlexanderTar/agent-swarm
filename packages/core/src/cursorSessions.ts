import { existsSync, readFileSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export function encodeCursorProjectPath(cwd: string): string {
  return cwd.replace(/^\//, "").replace(/\//g, "-");
}

export interface CursorSessionRef {
  conversationId: string;
  cwd?: string;
  transcriptPath?: string;
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

/** Working directory recorded by a regular Cursor chat, when there is one. */
function readChatCwd(conversationId: string, home: string): string | undefined {
  const chatsRoot = join(home, ".cursor", "chats");
  if (!existsSync(chatsRoot)) return undefined;
  for (const workspaceHash of readdirSync(chatsRoot)) {
    const metaPath = join(chatsRoot, workspaceHash, conversationId, "meta.json");
    if (!existsSync(metaPath)) continue;
    try {
      const meta = JSON.parse(readFileSync(metaPath, "utf8")) as { cwd?: string };
      if (typeof meta.cwd === "string" && meta.cwd.trim()) return meta.cwd.trim();
    } catch {
      /* skip unreadable meta */
    }
  }
  return undefined;
}

/** Locate a Cursor session's transcript (agent mode or subagent) and its working directory. */
export function resolveCursorSession(
  boardSessionId: string,
  hintCwd?: string,
  home = homedir(),
): CursorSessionRef | undefined {
  if (!boardSessionId.trim()) return undefined;

  for (const projectDir of listCursorTranscriptRoots(hintCwd, home)) {
    if (!existsSync(projectDir)) continue;

    const mainPath = join(projectDir, boardSessionId, `${boardSessionId}.jsonl`);
    if (existsSync(mainPath)) {
      return { conversationId: boardSessionId, cwd: hintCwd, transcriptPath: mainPath };
    }

    try {
      for (const folder of readdirSync(projectDir)) {
        const subPath = join(projectDir, folder, "subagents", `${boardSessionId}.jsonl`);
        if (existsSync(subPath)) {
          return { conversationId: folder, cwd: hintCwd, transcriptPath: subPath };
        }
      }
    } catch {
      /* skip unreadable dirs */
    }
  }

  return { conversationId: boardSessionId, cwd: readChatCwd(boardSessionId, home) ?? hintCwd };
}
