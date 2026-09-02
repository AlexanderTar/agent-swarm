import { describe, expect, it } from "vitest";
import {
  classifyHookEvent,
  formatHookOutput,
  hookSessionKey,
  mapAntigravityTool,
  normalizeHookInput,
  sessionBriefing,
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

  it("uses agent_id as the board session key for subagents", () => {
    const input = normalizeHookInput({
      session_id: "parent-session",
      agent_id: "agent-abc",
      agent_type: "Explore",
      cwd: "/repo/myproject",
      hook_event_name: "SubagentStart",
    }, "claude");
    expect(hookSessionKey(input)).toBe("agent-abc");
    expect(hookSessionKey({ sessionId: "solo", agentId: "  " })).toBe("solo");
  });

  it("detects opencode payloads", () => {
    const input = normalizeHookInput({ opencode: true, session_id: "oc-1", hook_event_name: "session.created" });
    expect(input.platform).toBe("opencode");
  });

  it("uses subagent_id for Cursor subagents", () => {
    const input = normalizeHookInput({
      parent_conversation_id: "conv-parent",
      subagent_id: "sub-123",
      subagent_type: "explore",
      task: "Search the codebase",
      workspace_roots: ["/repo/app"],
      hook_event_name: "subagentStart",
    }, "cursor");
    expect(hookSessionKey(input)).toBe("sub-123");
    expect(input.parentSessionId).toBe("conv-parent");
  });
});

describe("classifyHookEvent", () => {
  it("collapses each platform's spelling onto one kind", () => {
    expect(["SessionStart", "sessionStart", "session.created"].map(classifyHookEvent)).toEqual([
      "session_start",
      "session_start",
      "session_start",
    ]);
    expect(["SubagentStart", "subagentStart"].map(classifyHookEvent)).toEqual(["subagent_start", "subagent_start"]);
    expect(["SessionEnd", "sessionEnd", "session.deleted"].map(classifyHookEvent)).toEqual([
      "session_end",
      "session_end",
      "session_end",
    ]);
    for (const event of ["Stop", "stop", "StopFailure", "SubagentStop", "subagentStop", "session.idle", "PostInvocation", "afterAgentResponse", "TaskCompleted"]) {
      expect(classifyHookEvent(event)).toBe("turn_end");
    }
    for (const event of ["PreToolUse", "PostToolUse", "preToolUse", "postToolUse", "afterFileEdit", "PermissionRequest", "tool.execute.before", "tool.execute.after"]) {
      expect(classifyHookEvent(event)).toBe("tool");
    }
    expect(["UserPromptSubmit", "beforeSubmitPrompt"].map(classifyHookEvent)).toEqual(["prompt", "prompt"]);
    expect(classifyHookEvent("Notification")).toBe("notification");
    expect(["PreCompact", "preCompact", "PostCompact"].map(classifyHookEvent)).toEqual(["compact", "compact", "compact"]);
    expect(classifyHookEvent("MessageDisplay")).toBe("other");
    expect(classifyHookEvent("")).toBe("other");
  });
});

describe("sessionBriefing", () => {
  it("tells an unbound session how to create a board item", () => {
    const text = sessionBriefing({ sessionId: "s1", boardUrl: "http://127.0.0.1:7777" });
    expect(text).toContain("your swarm session: `s1`");
    expect(text).toContain("This session is not on the board.");
    expect(text).toContain("swarm_task_create");
  });

  it("names the task a bound session is working on", () => {
    const text = sessionBriefing({
      sessionId: "s1",
      boardUrl: "http://127.0.0.1:7777",
      task: { key: "SW-1", title: "Fix board titles", status: "in_progress" },
    });
    expect(text).toContain("You are working on **SW-1 — Fix board titles** (in_progress).");
    expect(text).not.toContain("not on the board");
  });
});

describe("formatHookOutput", () => {
  it("formats Cursor additional_context flat", () => {
    const out = formatHookOutput("cursor", { additionalContext: "Board: 3 tasks" }, "sessionStart");
    expect(out.additional_context).toBe("Board: 3 tasks");
  });

  it("formats opencode additionalContext", () => {
    expect(formatHookOutput("opencode", { additionalContext: "brief" })).toEqual({ additionalContext: "brief" });
    expect(formatHookOutput("opencode", {})).toEqual({});
  });

  it("formats Antigravity allow/deny", () => {
    expect(formatHookOutput("antigravity", { permission: "allow" })).toEqual({ decision: "allow" });
    expect(formatHookOutput("antigravity", { additionalContext: "brief" })).toEqual({
      decision: "allow",
      additionalContext: "brief",
    });
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
