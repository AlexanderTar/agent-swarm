import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { encodeCursorProjectPath, eventsToTranscript, extractTranscriptText } from "./transcripts.js";
import { inferStatusFromStop } from "./sessionSummary.js";
import type { NormalizedHookInput } from "./hooks.js";

describe("encodeCursorProjectPath", () => {
  it("encodes absolute paths", () => {
    expect(encodeCursorProjectPath("/Users/foo/bar")).toBe("Users-foo-bar");
  });
});

describe("eventsToTranscript", () => {
  it("includes prompts and stop summaries", () => {
    const text = eventsToTranscript([
      { eventType: "prompt", payloadJson: JSON.stringify({ prompt: "Fix the bug" }) },
      { eventType: "stop_summary", payloadJson: JSON.stringify({ summary: "Applied patch" }) },
    ]);
    expect(text).toContain("Fix the bug");
    expect(text).toContain("Applied patch");
  });
});

describe("inferStatusFromStop", () => {
  const base: NormalizedHookInput = {
    platform: "cursor",
    sessionId: "s1",
    cwd: "/tmp",
    hookEvent: "stop",
    raw: {},
  };

  it("maps session end to ready", () => {
    expect(inferStatusFromStop(base, "sessionEnd")).toBe("ready");
  });

  it("maps aborted stop to blocked", () => {
    expect(inferStatusFromStop({ ...base, raw: { status: "aborted" } }, "stop")).toBe("blocked");
  });

  it("maps normal stop to review", () => {
    expect(inferStatusFromStop(base, "stop")).toBe("review");
  });
});

describe("extractTranscriptText cursor jsonl", () => {
  it("parses user and assistant lines", () => {
    const jsonl = [
      JSON.stringify({ role: "user", message: { content: [{ type: "text", text: "Hello" }] } }),
      JSON.stringify({ role: "assistant", message: { content: [{ type: "text", text: "Hi there" }] } }),
    ].join("\n");
    const dir = mkdtempSync(join(tmpdir(), "swarm-transcript-"));
    const path = join(dir, "t.jsonl");
    writeFileSync(path, jsonl);
    const text = extractTranscriptText("cursor", path);
    expect(text).toContain("Hello");
    expect(text).toContain("Hi there");
  });
});
