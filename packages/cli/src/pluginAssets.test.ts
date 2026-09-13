import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const pluginRoot = join(process.cwd(), "../../plugin");

describe("plugin coordination contract", () => {
  it("ships no automatic hook registration for Claude, Codex, Cursor, or Antigravity", () => {
    for (const file of [
      ".claude-plugin/plugin.json",
      ".codex-plugin/plugin.json",
      ".cursor-plugin/plugin.json",
      ".antigravity-plugin/plugin.json",
    ]) {
      const manifest = JSON.parse(readFileSync(join(pluginRoot, file), "utf8")) as Record<string, unknown>;
      expect(manifest).not.toHaveProperty("hooks");
    }
  });

  it("keeps MCP session registration disabled for the Claude and OpenCode toolkit", () => {
    for (const file of ["mcp.json", "opencode.json"]) {
      const config = readFileSync(join(pluginRoot, file), "utf8");
      expect(config).toContain('"SWARM_REGISTER_SESSION": "0"');
    }
  });

  it("teaches the explicit main-and-child task, lease, lifecycle, and KB rules", () => {
    const instructions = readFileSync(join(pluginRoot, "AGENTS.md"), "utf8");
    expect(instructions).toContain("one main task");
    expect(instructions).toContain("one child task");
    expect(instructions).toContain("Joining is observe-only");
    expect(instructions).toContain("one active worker");
    expect(instructions).toContain("`ready` work");
    expect(instructions).toContain("`review`");
    expect(instructions).toContain("specifications, approved plans, and decisions");
  });
});
