import { describe, expect, it } from "vitest";
import {
  buildSessionContext,
  formatHookOutput,
  isSubagentHook,
  mapAntigravityTool,
  normalizeHookInput,
  resolveBoardSessionId,
  resolveHookBoardSessionId,
  resolveHookRootSessionId,
  resolveSubagentBoardId,
  sessionTaskTitle,
  shortReference,
  subagentIdFromTranscriptPath,
} from "./hooks.js";
import { resolveSessionTaskTitle } from "./sessionTitles.js";

describe("normalizeHookInput", () => {
  it("normalizes Claude SessionStart", () => {
    const input = normalizeHookInput({
      session_id: "abc-123",
      cwd: "/repo",
      hook_event_name: "SessionStart",
      model: "claude-sonnet",
    }, "claude");
    expect(input.platform).toBe("claude");
    expect(input.sessionId).toBe("abc-123");
    expect(input.cwd).toBe("/repo");
    expect(input.hookEvent).toBe("SessionStart");
    expect(input.model).toBe("claude-sonnet");
  });

  it("normalizes Cursor beforeSubmitPrompt", () => {
    const input = normalizeHookInput({
      conversation_id: "cursor-1",
      workspace_roots: ["/Users/dev/project"],
      prompt: "Fix the login bug",
      hook_event_name: "beforeSubmitPrompt",
    }, "cursor");
    expect(input.platform).toBe("cursor");
    expect(input.sessionId).toBe("cursor-1");
    expect(input.cwd).toBe("/Users/dev/project");
    expect(input.prompt).toBe("Fix the login bug");
  });

  it("normalizes Codex hook with turn_id", () => {
    const input = normalizeHookInput({
      session_id: "codex-sess",
      turn_id: "turn-1",
      hook_event_name: "PreToolUse",
      tool_name: "shell",
    });
    expect(input.platform).toBe("codex");
    expect(input.sessionId).toBe("codex-sess");
    expect(input.toolName).toBe("shell");
  });

  it("normalizes Antigravity PreToolUse with toolCall", () => {
    const input = normalizeHookInput({
      conversationId: "ag-1",
      workspacePaths: ["/workspace"],
      toolCall: { name: "write_to_file", args: { TargetFile: "foo.ts" } },
    }, "antigravity");
    expect(input.platform).toBe("antigravity");
    expect(input.sessionId).toBe("ag-1");
    expect(input.toolName).toBe("write_to_file");
    expect(input.toolInput).toEqual({ TargetFile: "foo.ts" });
  });

  it("parses opencode sessionID (capital D)", () => {
    const input = normalizeHookInput({
      sessionID: "ses_abc123",
      directory: "/repo/app",
      hook_event_name: "sessionStart",
    }, "opencode");
    expect(input.platform).toBe("opencode");
    expect(input.sessionId).toBe("ses_abc123");
  });

  it("identifies Claude subagents and resolves root session", () => {
    const input = normalizeHookInput({
      session_id: "parent-session",
      agent_id: "agent-abc",
      agent_type: "Explore",
      cwd: "/repo/myproject",
      hook_event_name: "SubagentStart",
    }, "claude");
    expect(isSubagentHook(input)).toBe(true);
    expect(resolveBoardSessionId(input)).toBe("parent-session");
    expect(resolveHookRootSessionId(input)).toBe("parent-session");
    expect(resolveSubagentBoardId(input)).toBe("agent-abc");
    expect(sessionTaskTitle(input)).toBe("Explore · myproject");
    expect(buildSessionContext(input, "parent-session")).toContain("Parent session");
  });

  it("identifies Cursor subagents and resolves root session", () => {
    const input = normalizeHookInput({
      parent_conversation_id: "conv-parent",
      subagent_id: "sub-123",
      subagent_type: "explore",
      task: "Search the codebase",
      workspace_roots: ["/repo/app"],
      hook_event_name: "subagentStart",
    }, "cursor");
    expect(isSubagentHook(input)).toBe(true);
    expect(resolveBoardSessionId(input)).toBe("conv-parent");
    expect(resolveHookRootSessionId(input)).toBe("conv-parent");
    expect(resolveSubagentBoardId(input)).toBe("sub-123");
    expect(buildSessionContext(input, "conv-parent")).toContain("Search the codebase");
    expect(resolveSessionTaskTitle(input).title).toBe("Search the codebase");
  });

  it("identifies Antigravity subagents and avoids transcript_full corruption", () => {
    // Normal root session with transcript_full.jsonl
    const rootInput = normalizeHookInput({
      conversationId: "ag-root-123",
      workspacePaths: ["/workspace"],
      hookEvent: "PreToolUse",
      transcript_path: "/Users/dev/.gemini/antigravity-cli/brain/ag-root-123/.system_generated/logs/transcript_full.jsonl",
    }, "antigravity");
    expect(isSubagentHook(rootInput)).toBe(false);
    expect(resolveHookRootSessionId(rootInput)).toBe("ag-root-123");
    expect(subagentIdFromTranscriptPath(rootInput.transcriptPath)).toBeUndefined();

    // Nested subagent session with parent conversation ID
    const subInput = normalizeHookInput({
      conversationId: "ag-child-456",
      parentConversationId: "ag-root-123",
      workspacePaths: ["/workspace"],
      hookEvent: "PreInvocation",
    }, "antigravity");
    expect(isSubagentHook(subInput)).toBe(true);
    expect(resolveHookRootSessionId(subInput)).toBe("ag-root-123");
  });

  it("identifies OpenCode and Codex subagents", () => {
    const opencodeSub = normalizeHookInput({
      sessionID: "child-session",
      parentSessionId: "parent-session",
      hook_event_name: "toolUse",
    }, "opencode");
    expect(isSubagentHook(opencodeSub)).toBe(true);
    expect(resolveHookRootSessionId(opencodeSub)).toBe("parent-session");

    const opencodeSub2 = normalizeHookInput({
      session_id: "ses_child_456",
      subagent_type: "general",
      hook_event_name: "toolUse",
    }, "opencode");
    expect(isSubagentHook(opencodeSub2)).toBe(true);

    const codexSub = normalizeHookInput({
      session_id: "codex-child",
      parent_conversation_id: "codex-parent",
      hook_event_name: "PreToolUse",
    }, "codex");
    expect(isSubagentHook(codexSub)).toBe(true);
    expect(resolveHookRootSessionId(codexSub)).toBe("codex-parent");

    const codexSub2 = normalizeHookInput({
      session_id: "codex-child-2",
      parent_session_id: "codex-parent-2",
      hook_event_name: "SessionStart",
    }, "codex");
    expect(isSubagentHook(codexSub2)).toBe(true);
    expect(resolveHookRootSessionId(codexSub2)).toBe("codex-parent-2");
  });

  it("normalizes Cursor afterAgentResponse text", () => {
    const input = normalizeHookInput({
      conversation_id: "cursor-1",
      workspace_roots: ["/repo"],
      hook_event_name: "afterAgentResponse",
      text: "Done — login bug fixed.",
    }, "cursor");
    expect(input.agentText).toBe("Done — login bug fixed.");
  });

  it("normalizes Cursor afterAgentThought with duration", () => {
    const input = normalizeHookInput({
      conversation_id: "cursor-1",
      workspace_roots: ["/repo"],
      hook_event_name: "afterAgentThought",
      text: "I'll search for auth handlers first.",
      duration_ms: 4200,
    }, "cursor");
    expect(input.agentText).toBe("I'll search for auth handlers first.");
    expect(input.thoughtDurationMs).toBe(4200);
  });

  it("normalizes Claude MessageDisplay delta", () => {
    const input = normalizeHookInput({
      session_id: "claude-1",
      cwd: "/repo",
      hook_event_name: "MessageDisplay",
      delta: "Working on it…",
      final: true,
      message_id: "msg-1",
    }, "claude");
    expect(input.messageDelta).toBe("Working on it…");
    expect(input.messageFinal).toBe(true);
  });

  it("resolves subagent board id from transcript path on subagentStop", () => {
    const input = normalizeHookInput({
      conversation_id: "parent-conv",
      workspace_roots: ["/repo"],
      hook_event_name: "subagentStop",
      agent_transcript_path: "/Users/dev/.cursor/projects/repo/agent-transcripts/parent/subagents/sub-99.jsonl",
      summary: "Found 3 auth handlers",
      status: "completed",
    }, "cursor");
    expect(isSubagentHook(input)).toBe(true);
    expect(resolveHookBoardSessionId(input, "subagentStop")).toBe("parent-conv");
    expect(resolveSubagentBoardId(input)).toBe("sub-99");
    expect(input.subagentSummary).toBe("Found 3 auth handlers");
  });
});

describe("formatHookOutput", () => {
  it("formats Cursor additional_context flat", () => {
    const out = formatHookOutput("cursor", { additionalContext: "Board: 3 tasks" }, "sessionStart");
    expect(out.additional_context).toBe("Board: 3 tasks");
  });

  it("formats Antigravity allow/deny", () => {
    expect(formatHookOutput("antigravity", { permission: "allow" })).toEqual({ decision: "allow" });
    expect(formatHookOutput("antigravity", { permission: "deny", userMessage: "blocked" })).toEqual({
      decision: "deny",
      reason: "blocked",
    });
  });

  it("formats Claude hookSpecificOutput", () => {
    const out = formatHookOutput("claude", { additionalContext: "ctx" }, "SessionStart");
    expect(out.hookSpecificOutput).toMatchObject({
      hookEventName: "SessionStart",
      additionalContext: "ctx",
    });
  });
});

describe("mapAntigravityTool", () => {
  it("maps known tools", () => {
    expect(mapAntigravityTool("write_to_file")).toBe("Write");
    expect(mapAntigravityTool("run_command")).toBe("Bash");
  });
});

describe("shortReference", () => {
  it("truncates long messages", () => {
    const msg = "x".repeat(600);
    expect(shortReference(msg).length).toBeLessThanOrEqual(500);
  });
});
