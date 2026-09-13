import { describe, expect, it } from "vitest";
import {
  removeSwarmCodexHooks,
  spliceSwarmTomlBlock,
} from "./codexHooks.js";

const EXISTING_HOOKS = {
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

describe("removeSwarmCodexHooks", () => {
  it("removes only Swarm Codex hook commands and preserves unrelated hooks", () => {
    const existing = {
      hooks: {
        ...EXISTING_HOOKS.hooks,
        Stop: [
          {
            hooks: [
              { type: "command", command: "node /usr/local/bin/user-stop-hook.mjs" },
              { type: "command", command: 'node "/plugin/hooks/post-hook.mjs" codex Stop' },
            ],
          },
        ],
      },
    };

    expect(removeSwarmCodexHooks(existing)).toEqual({
      hooks: {
        Stop: [{ hooks: [{ type: "command", command: "node /usr/local/bin/user-stop-hook.mjs" }] }],
      },
    });
  });

  it("is idempotent when no Swarm hooks are installed", () => {
    const userHooks = { hooks: { Stop: [{ hooks: [{ command: "echo user" }] }] } };
    expect(removeSwarmCodexHooks(userHooks)).toEqual(userHooks);
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
