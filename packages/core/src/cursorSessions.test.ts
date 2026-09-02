import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { resolveCursorSession } from "./cursorSessions.js";

describe("resolveCursorSession", () => {
  it("reads the working directory from ~/.cursor/chats meta.json", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-chat-"));
    const convId = "72370b05-0483-4ee2-9a67-9067450bee16";
    const chatDir = join(home, ".cursor", "chats", "ws-hash", convId);
    mkdirSync(chatDir, { recursive: true });
    writeFileSync(join(chatDir, "meta.json"), JSON.stringify({ cwd: "/Users/dev/endurio" }));

    const ref = resolveCursorSession(convId, undefined, home);
    expect(ref?.conversationId).toBe(convId);
    expect(ref?.cwd).toBe("/Users/dev/endurio");
    expect(ref?.transcriptPath).toBeUndefined();
  });

  it("finds agent-mode transcripts under the project dir", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-agent-"));
    const cwd = "/Users/dev/agent-swarm";
    const sessionId = "fe37b85b-0da1-40ee-95e9-e84782f16b59";
    const dir = join(home, ".cursor", "projects", "Users-dev-agent-swarm", "agent-transcripts", sessionId);
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, `${sessionId}.jsonl`), "{}\n");

    const ref = resolveCursorSession(sessionId, cwd, home);
    expect(ref?.conversationId).toBe(sessionId);
    expect(ref?.transcriptPath).toContain(`${sessionId}.jsonl`);
  });

  it("finds subagent transcripts and reports the parent conversation", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-sub-"));
    const cwd = "/Users/dev/agent-swarm";
    const parentId = "parent-conv";
    const subId = "172a0c86-0bc2-4962-bca6-ef4b02ef2fde";
    const subDir = join(
      home,
      ".cursor",
      "projects",
      "Users-dev-agent-swarm",
      "agent-transcripts",
      parentId,
      "subagents",
    );
    mkdirSync(subDir, { recursive: true });
    writeFileSync(join(subDir, `${subId}.jsonl`), "{}\n");

    const ref = resolveCursorSession(subId, cwd, home);
    expect(ref?.conversationId).toBe(parentId);
    expect(ref?.transcriptPath).toContain(`${subId}.jsonl`);
  });

  it("returns the bare session when nothing is on disk", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-empty-"));
    expect(resolveCursorSession("unknown-id", "/repo", home)).toEqual({
      conversationId: "unknown-id",
      cwd: "/repo",
    });
    expect(resolveCursorSession("  ", "/repo", home)).toBeUndefined();
  });
});
