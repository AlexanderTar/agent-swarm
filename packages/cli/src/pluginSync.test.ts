import { describe, expect, it } from "vitest";
import { removeSwarmClaudeHooks, removeSwarmCursorHooks } from "./pluginSync.js";

describe("removeSwarmCursorHooks", () => {
  it("removes Swarm hooks and preserves unrelated hooks in the same event", () => {
    const existing = {
      version: 1,
      hooks: {
        stop: [
          { command: "node /user/notify.mjs" },
          { command: 'node "/plugin/hooks/post-hook.mjs" cursor stop' },
        ],
        sessionStart: [{ command: 'node "/plugin/hooks/post-hook.mjs" cursor sessionStart' }],
      },
    };

    expect(removeSwarmCursorHooks(existing)).toEqual({
      version: 1,
      hooks: {
        stop: [{ command: "node /user/notify.mjs" }],
      },
    });
  });
});

describe("removeSwarmClaudeHooks", () => {
  it("removes legacy Swarm HTTP and command hooks without disturbing another hook", () => {
    const existing = {
      hooks: {
        Stop: [
          {
            hooks: [
              { type: "http", url: "http://127.0.0.1:7777/hooks/claude/Stop" },
              { type: "command", command: "node /user/notify.mjs" },
            ],
          },
        ],
        SessionStart: [
          { hooks: [{ type: "command", command: 'node "/plugin/hooks/post-hook.mjs" claude SessionStart' }] },
        ],
      },
      permissions: { allow: ["Bash(git status)"] },
    };

    expect(removeSwarmClaudeHooks(existing)).toEqual({
      hooks: { Stop: [{ hooks: [{ type: "command", command: "node /user/notify.mjs" }] }] },
      permissions: { allow: ["Bash(git status)"] },
    });
  });
});
