import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { enrichHookInput } from "./hookEnrichment.js";
import { normalizeHookInput } from "./hooks.js";

describe("enrichHookInput", () => {
  it("enriches cursor chat sessions from ~/.cursor/chats on live hooks", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-enrich-chat-"));
    const convId = "72370b05-0483-4ee2-9a67-9067450bee16";
    const chatDir = join(home, ".cursor", "chats", "ws", convId);
    mkdirSync(chatDir, { recursive: true });
    writeFileSync(
      join(chatDir, "meta.json"),
      JSON.stringify({ title: "Style Consistency Sweep", cwd: "/Users/dev/endurio" }),
    );

    const input = normalizeHookInput(
      {
        conversation_id: convId,
        workspace_roots: ["/Users/dev/endurio"],
        hook_event_name: "sessionStart",
      },
      "cursor",
    );

    const prevHome = process.env.HOME;
    process.env.HOME = home;
    try {
      enrichHookInput(input);
    } finally {
      process.env.HOME = prevHome;
    }

    expect(input.sessionTitle).toBe("Style Consistency Sweep");
    expect(input.cwd).toBe("/Users/dev/endurio");
  });

  it("enriches cursor agent-mode sessions from agent-transcripts", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-enrich-agent-"));
    const sessionId = "fe37b85b-0da1-40ee-95e9-e84782f16b59";
    const cwd = "/Users/dev/agent-swarm";
    const dir = join(home, ".cursor", "projects", "Users-dev-agent-swarm", "agent-transcripts", sessionId);
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, `${sessionId}.jsonl`),
      `${JSON.stringify({
        role: "user",
        message: { content: [{ type: "text", text: "<user_query>Build local agent board</user_query>" }] },
      })}\n`,
    );

    const input = normalizeHookInput(
      {
        conversation_id: sessionId,
        workspace_roots: [cwd],
        hook_event_name: "stop",
      },
      "cursor",
    );

    const prevHome = process.env.HOME;
    process.env.HOME = home;
    try {
      enrichHookInput(input);
    } finally {
      process.env.HOME = prevHome;
    }

    expect(input.sessionTitle).toBe("Build local agent board");
    expect(input.transcriptPath).toContain(`${sessionId}.jsonl`);
  });

  it("prefers hook transcript_path over disk lookup", () => {
    const input = normalizeHookInput(
      {
        conversation_id: "abc",
        workspace_roots: ["/tmp"],
        transcript_path: "/custom/transcript.jsonl",
        hook_event_name: "stop",
      },
      "cursor",
    );
    enrichHookInput(input);
    expect(input.transcriptPath).toBe("/custom/transcript.jsonl");
  });
});
