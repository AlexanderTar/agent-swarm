import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { buildAntigravityHooks, inspectAntigravityHooks } from "./antigravityHooks.js";

describe("buildAntigravityHooks", () => {
  it("uses flat command handlers for PreInvocation, PostInvocation, and Stop", () => {
    const hooks = buildAntigravityHooks("/plugin")["swarm-sync"];
    expect(hooks?.PreInvocation?.[0]).toMatchObject({
      type: "command",
      command: 'node "/plugin/hooks/post-hook.mjs" antigravity PreInvocation',
    });
    expect(hooks?.PreInvocation?.[0]).not.toHaveProperty("hooks");
    expect(hooks?.PostInvocation?.[0]).toMatchObject({
      type: "command",
      command: 'node "/plugin/hooks/post-hook.mjs" antigravity PostInvocation',
    });
    expect(hooks?.Stop?.[0]).toMatchObject({
      type: "command",
      command: 'node "/plugin/hooks/post-hook.mjs" antigravity Stop',
    });
  });

  it("keeps matcher + hooks wrapper for PreToolUse and PostToolUse", () => {
    const hooks = buildAntigravityHooks("/plugin")["swarm-sync"];
    expect(hooks?.PreToolUse?.[0]?.matcher).toContain("run_command");
    expect(hooks?.PreToolUse?.[0]?.hooks?.[0]?.command).toBe(
      'node "/plugin/hooks/post-hook.mjs" antigravity PreToolUse',
    );
    expect(hooks?.PostToolUse?.[0]?.matcher).toBe("*");
    expect(hooks?.PostToolUse?.[0]?.hooks?.[0]?.command).toBe(
      'node "/plugin/hooks/post-hook.mjs" antigravity PostToolUse',
    );
  });

  it("does not leave PLUGIN_ROOT in generated commands", () => {
    const json = JSON.stringify(buildAntigravityHooks("/Users/dev/.swarm/app/current/plugin"));
    expect(json).not.toContain("${PLUGIN_ROOT}");
  });
});

describe("inspectAntigravityHooks", () => {
  it("fails when PreInvocation is nested under hooks without a top-level command", () => {
    const invalid = {
      "swarm-sync": {
        PreInvocation: [
          {
            hooks: [
              {
                type: "command",
                command: "node /plugin/hooks/post-hook.mjs antigravity PreInvocation",
              },
            ],
          },
        ],
      },
    };
    const result = inspectAntigravityHooks(JSON.stringify(invalid));
    expect(result.ok).toBe(false);
    expect(result.detail).toMatch(/command/);
  });

  it("fails when commands still contain PLUGIN_ROOT", () => {
    const hooks = {
      "swarm-sync": {
        PreInvocation: [
          {
            type: "command",
            command: 'node "${PLUGIN_ROOT}/hooks/post-hook.mjs" antigravity PreInvocation',
          },
        ],
      },
    };
    const result = inspectAntigravityHooks(JSON.stringify(hooks));
    expect(result.ok).toBe(false);
    expect(result.detail).toMatch(/PLUGIN_ROOT/);
  });

  it("passes when PreInvocation points at an existing post-hook.mjs", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-ag-hooks-"));
    const pluginPath = join(dir, "plugin");
    mkdirSync(join(pluginPath, "hooks"), { recursive: true });
    writeFileSync(join(pluginPath, "hooks/post-hook.mjs"), "export {};\n");
    const result = inspectAntigravityHooks(JSON.stringify(buildAntigravityHooks(pluginPath)));
    expect(result.ok).toBe(true);
  });
});
