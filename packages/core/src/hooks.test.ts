import { describe, expect, it } from "vitest";
import {
  formatHookOutput,
  mapAntigravityTool,
  normalizeHookInput,
  shortReference,
} from "./hooks.js";

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
