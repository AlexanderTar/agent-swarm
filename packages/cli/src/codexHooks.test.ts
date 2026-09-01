import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  inspectCodexUserHooks,
  materializeCodexUserHooks,
  spliceSwarmTomlBlock,
} from "./codexHooks.js";

const PLUGIN_HOOKS = {
  hooks: {
    SessionStart: [
      {
        hooks: [
          {
            type: "command",
            command: 'node "${PLUGIN_ROOT}/hooks/post-hook.mjs" codex SessionStart',
            timeout: 5,
          },
        ],
      },
    ],
    SessionEnd: [
      {
        hooks: [
          {
            type: "command",
            command: 'node "${PLUGIN_ROOT}/hooks/post-hook.mjs" codex SessionEnd',
            timeout: 5,
            async: true,
          },
        ],
      },
    ],
  },
};

describe("materializeCodexUserHooks", () => {
  it("expands PLUGIN_ROOT to an absolute plugin path for user-level hooks.json", () => {
    const pluginPath = "/Users/dev/.swarm/app/current/plugin";
    const materialized = materializeCodexUserHooks(PLUGIN_HOOKS, pluginPath);
    const command = materialized.hooks?.SessionStart?.[0]?.hooks?.[0]?.command;
    expect(command).not.toContain("${PLUGIN_ROOT}");
    expect(command).toBe(`node "${pluginPath}/hooks/post-hook.mjs" codex SessionStart`);
  });

  it("caps SessionEnd timeout at 3 seconds", () => {
    const materialized = materializeCodexUserHooks(PLUGIN_HOOKS, "/plugin");
    const sessionEnd = materialized.hooks?.SessionEnd as Array<{
      hooks: Array<{ timeout?: number }>;
    }>;
    expect(sessionEnd[0]?.hooks[0]?.timeout).toBe(3);
  });
});

describe("inspectCodexUserHooks", () => {
  it("fails when user hooks still contain unexpanded PLUGIN_ROOT", () => {
    const result = inspectCodexUserHooks(JSON.stringify(PLUGIN_HOOKS));
    expect(result.ok).toBe(false);
    expect(result.detail).toMatch(/PLUGIN_ROOT/);
  });

  it("fails when the hook script path does not exist", () => {
    const hooks = materializeCodexUserHooks(PLUGIN_HOOKS, "/does-not-exist/plugin");
    const result = inspectCodexUserHooks(JSON.stringify(hooks));
    expect(result.ok).toBe(false);
    expect(result.detail).toMatch(/post-hook\.mjs/);
  });

  it("passes when SessionStart points at an existing post-hook.mjs", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-codex-hooks-"));
    const pluginPath = join(dir, "plugin");
    mkdirSync(join(pluginPath, "hooks"), { recursive: true });
    writeFileSync(join(pluginPath, "hooks/post-hook.mjs"), "export {};\n");
    const hooks = materializeCodexUserHooks(PLUGIN_HOOKS, pluginPath);
    const result = inspectCodexUserHooks(JSON.stringify(hooks));
    expect(result.ok).toBe(true);
  });
});

describe("spliceSwarmTomlBlock", () => {
  it("does not swallow [hooks.state] that leaked between swarm sentinels", () => {
    const mcpBlock = `# swarm:start
[mcp_servers.swarm]
command = "node"
args = ["/plugin/bin/swarm-mcp.mjs"]

[mcp_servers.swarm.env]
SWARM_URL = "http://127.0.0.1:7777"
SWARM_AGENT = "codex"
SWARM_REGISTER_SESSION = "0"
# swarm:end`;

    const content = `# swarm:start
[mcp_servers.swarm]
command = "node"
args = ["/old/swarm-mcp.mjs"]

[mcp_servers.swarm.env]
SWARM_URL = "http://127.0.0.1:7777"
SWARM_AGENT = "codex"
SWARM_REGISTER_SESSION = "0"

[projects."/Users/dev/app"]
trust_level = "trusted"

[hooks.state]

[hooks.state."/Users/dev/.codex/hooks.json:session_start:0:0"]
trusted_hash = "sha256:abc"
# swarm:end
`;

    const next = spliceSwarmTomlBlock(content, mcpBlock);
    expect(next).toContain('[projects."/Users/dev/app"]');
    expect(next).toContain('trust_level = "trusted"');
    expect(next).toContain("[hooks.state]");
    expect(next).toContain('trusted_hash = "sha256:abc"');
    expect(next).toContain('args = ["/plugin/bin/swarm-mcp.mjs"]');
    expect(next.indexOf("# swarm:end")).toBeLessThan(next.indexOf("[hooks.state]"));
  });
});
