import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { encodeCursorProjectPath } from "./cursorSessions.js";
import { encodeClaudeProjectPath, extractTranscriptText } from "./transcripts.js";

describe("encodeCursorProjectPath", () => {
  it("encodes absolute paths", () => {
    expect(encodeCursorProjectPath("/Users/foo/bar")).toBe("Users-foo-bar");
  });
});

describe("encodeClaudeProjectPath", () => {
  it("encodes absolute paths", () => {
    expect(encodeClaudeProjectPath("/Users/dev/repo")).toBe("-Users-dev-repo");
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
