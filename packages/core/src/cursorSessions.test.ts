import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { resolveCursorSession, titleFromUserMessage } from "./cursorSessions.js";

describe("titleFromUserMessage", () => {
  it("strips cursor envelope tags", () => {
    const text =
      "<timestamp>Friday</timestamp><user_query>Build agent orchestration board</user_query>";
    expect(titleFromUserMessage(text)).toBe("Build agent orchestration board");
  });
});

describe("resolveCursorSession", () => {
  it("reads regular chat titles from ~/.cursor/chats meta.json", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-chat-"));
    const convId = "72370b05-0483-4ee2-9a67-9067450bee16";
    const chatDir = join(home, ".cursor", "chats", "ws-hash", convId);
    mkdirSync(chatDir, { recursive: true });
    writeFileSync(
      join(chatDir, "meta.json"),
      JSON.stringify({ title: "Style Consistency Sweep", cwd: "/Users/dev/endurio" }),
    );

    const ref = resolveCursorSession(convId, undefined, home);
    expect(ref?.kind).toBe("chat");
    expect(ref?.title).toBe("Style Consistency Sweep");
    expect(ref?.cwd).toBe("/Users/dev/endurio");
  });

  it("reads agent-mode sessions from agent-transcripts jsonl", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-agent-"));
    const cwd = "/Users/dev/agent-swarm";
    const sessionId = "fe37b85b-0da1-40ee-95e9-e84782f16b59";
    const dir = join(home, ".cursor", "projects", "Users-dev-agent-swarm", "agent-transcripts", sessionId);
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, `${sessionId}.jsonl`),
      `${JSON.stringify({
        role: "user",
        message: { content: [{ type: "text", text: "<user_query>Build local agent board</user_query>" }] },
      })}\n`,
    );

    const ref = resolveCursorSession(sessionId, cwd, home);
    expect(ref?.kind).toBe("agent");
    expect(ref?.title).toBe("Build local agent board");
    expect(ref?.transcriptPath).toContain(`${sessionId}.jsonl`);
  });

  it("reads subagent sessions under parent conversation folder", () => {
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
    writeFileSync(
      join(subDir, `${subId}.jsonl`),
      `${JSON.stringify({
        role: "user",
        message: { content: [{ type: "text", text: "Explore hook routes for summaries" }] },
      })}\n`,
    );

    const ref = resolveCursorSession(subId, cwd, home);
    expect(ref?.kind).toBe("subagent");
    expect(ref?.conversationId).toBe(parentId);
    expect(ref?.agentId).toBe(subId);
    expect(ref?.title).toBe("Explore hook routes for summaries");
  });

  it("falls back to prompt_history when chat meta has no title", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-history-"));
    const convId = "ba158e7d-7e37-4273-96e6-8557802aac19";
    const chatDir = join(home, ".cursor", "chats", "ws-hash", convId);
    mkdirSync(chatDir, { recursive: true });
    writeFileSync(join(chatDir, "meta.json"), JSON.stringify({ cwd: "/Users/dev/app" }));
    writeFileSync(join(chatDir, "prompt_history.json"), JSON.stringify(["Fix save button in settings"]));

    const ref = resolveCursorSession(convId, "/Users/dev/app", home);
    expect(ref?.title).toBe("Fix save button in settings");
  });
});
