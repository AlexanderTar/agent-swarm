import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { normalizeHookInput } from "./hooks.js";
import {
  claudeTranscriptPath,
  encodeClaudeProjectPath,
  findClaudeTranscriptPath,
  isFallbackSessionTitle,
  readClaudeTranscriptTitle,
  readCursorChatTitle,
  readFirstPromptFromTranscript,
  readModelFromClaudeTranscript,
  resolveCursorSessionIds,
  resolveSessionTaskTitle,
  shouldReplaceSessionTitle,
} from "./sessionTitles.js";

describe("sessionTitles", () => {
  it("detects fallback titles", () => {
    expect(isFallbackSessionTitle("claude · agent-swarm (startup)")).toBe(true);
    expect(isFallbackSessionTitle("cursor · agent-swarm")).toBe(true);
    expect(isFallbackSessionTitle("cursor · ")).toBe(true);
    expect(isFallbackSessionTitle("cursor ·")).toBe(true);
    expect(isFallbackSessionTitle("Explore · myproject")).toBe(true);
    expect(isFallbackSessionTitle("HITL approval sheet redesign")).toBe(false);
  });

  it("encodes Claude project paths", () => {
    expect(encodeClaudeProjectPath("/Users/dev/repo")).toBe("-Users-dev-repo");
    expect(claudeTranscriptPath("sess-1", "/Users/dev/repo", "/home")).toBe(
      "/home/.claude/projects/-Users-dev-repo/sess-1.jsonl",
    );
  });

  it("reads Claude ai-title from transcript tail", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-transcript-"));
    const path = join(dir, "sess.jsonl");
    writeFileSync(
      path,
      [
        '{"type":"user","message":"hello"}',
        '{"type":"ai-title","aiTitle":"Board session naming","sessionId":"sess"}',
      ].join("\n"),
    );
    expect(readClaudeTranscriptTitle(path)).toBe("Board session naming");
  });

  it("reads Cursor chat title from meta.json", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-"));
    const convId = "conv-abc";
    const metaDir = join(home, ".cursor", "chats", "workspace-hash", convId);
    mkdirSync(metaDir, { recursive: true });
    writeFileSync(join(metaDir, "meta.json"), JSON.stringify({ title: "Skill Handoff Update" }));
    expect(readCursorChatTitle(convId, home)).toBe("Skill Handoff Update");
  });

  it("maps cursor subagent ids to parent conversation", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-cursor-map-"));
    const cwd = "/Users/dev/agent-swarm";
    const parentId = "parent-conv";
    const subId = "sub-agent-1";
    const transcripts = join(home, ".cursor", "projects", "Users-dev-agent-swarm", "agent-transcripts");
    mkdirSync(join(transcripts, parentId, "subagents"), { recursive: true });
    writeFileSync(join(transcripts, parentId, "subagents", `${subId}.jsonl`), '{"role":"user","message":{"content":[{"type":"text","text":"Find auth code"}]}}\n');

    expect(resolveCursorSessionIds(subId, cwd, home)).toEqual({
      conversationId: parentId,
      agentId: subId,
    });
  });

  it("prefers hook session_title", () => {
    const input = normalizeHookInput(
      {
        session_id: "abc",
        cwd: "/repo/app",
        session_title: "Fix login bug",
        hook_event_name: "SessionStart",
      },
      "claude",
    );
    expect(resolveSessionTaskTitle(input)).toEqual({ title: "Fix login bug", fromSession: true });
  });

  it("uses subagent task when no session title", () => {
    const input = normalizeHookInput(
      {
        parent_conversation_id: "parent",
        subagent_id: "sub-1",
        subagent_type: "explore",
        task: "Search the codebase",
        workspace_roots: ["/repo/app"],
        hook_event_name: "subagentStart",
      },
      "cursor",
    );
    expect(resolveSessionTaskTitle(input)).toEqual({ title: "Search the codebase", fromSession: true });
  });

  it("replaces fallback titles when a session name arrives", () => {
    expect(shouldReplaceSessionTitle("cursor · agent-swarm", "Skill Handoff Update", true)).toBe(true);
    expect(shouldReplaceSessionTitle("Skill Handoff Update", "Skill Handoff Update", true)).toBe(false);
    expect(shouldReplaceSessionTitle("Custom user title", "claude · repo", false)).toBe(false);
  });

  it("finds Claude subagent transcripts as agent-<id>.jsonl", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-claude-sub-"));
    const cwd = "/Users/dev/repo";
    const parent = "parent-sess-uuid";
    const agentId = "abc123deadbeef";
    const project = join(home, ".claude", "projects", encodeClaudeProjectPath(cwd));
    mkdirSync(join(project, parent, "subagents"), { recursive: true });
    writeFileSync(
      join(project, parent, "subagents", `agent-${agentId}.jsonl`),
      JSON.stringify({
        type: "user",
        isSidechain: true,
        agentId,
        message: { role: "user", content: "Survey all liquid glass button usages in src/" },
      }) + "\n",
    );

    const path = findClaudeTranscriptPath(agentId, cwd, home);
    expect(path).toContain(`agent-${agentId}.jsonl`);
    expect(readFirstPromptFromTranscript(path!)).toContain("liquid glass");
  });

  it("extracts fork Agent description when no user turn yet", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-fork-"));
    const path = join(dir, "fork.jsonl");
    writeFileSync(
      path,
      [
        '{"type":"fork-context-ref","agentId":"x"}',
        JSON.stringify({
          type: "assistant",
          message: {
            role: "assistant",
            content: [
              {
                type: "tool_use",
                name: "Agent",
                input: {
                  description: "Investigate memory architecture",
                  prompt: "Map User.attributes vs AthleteMemory across the codebase.",
                },
              },
            ],
          },
        }),
      ].join("\n"),
    );
    expect(readFirstPromptFromTranscript(path)).toContain("AthleteMemory");
  });

  it("treats long raw prompts and Subagent · titles as fallbacks", () => {
    expect(isFallbackSessionTitle("Subagent · endurio")).toBe(true);
    expect(isFallbackSessionTitle("general-purpose · endurio")).toBe(true);
    expect(isFallbackSessionTitle("opencode · endurio")).toBe(true);
    expect(isFallbackSessionTitle(`You are implementing ${"x".repeat(100)}`)).toBe(true);
  });

  it("reads model from Claude transcript tail", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-model-"));
    const path = join(dir, "sess.jsonl");
    writeFileSync(
      path,
      [
        JSON.stringify({ type: "user", message: { role: "user", content: "hi" } }),
        JSON.stringify({
          type: "assistant",
          message: { role: "assistant", model: "claude-opus-4-5", content: [{ type: "text", text: "hello" }] },
        }),
        JSON.stringify({
          type: "assistant",
          message: { role: "assistant", model: "claude-sonnet-4-6", content: [{ type: "text", text: "later" }] },
        }),
      ].join("\n"),
    );
    // Most recent model wins (reverse scan).
    expect(readModelFromClaudeTranscript(path)).toBe("claude-sonnet-4-6");
  });

  it("returns undefined for missing or model-less transcripts", () => {
    expect(readModelFromClaudeTranscript("/nonexistent/x.jsonl")).toBeUndefined();
    const dir = mkdtempSync(join(tmpdir(), "swarm-model-none-"));
    const path = join(dir, "sess.jsonl");
    writeFileSync(path, JSON.stringify({ type: "user", message: { role: "user", content: "hi" } }) + "\n");
    expect(readModelFromClaudeTranscript(path)).toBeUndefined();
  });
});
