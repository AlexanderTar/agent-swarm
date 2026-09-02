/** OpenCode plugin: relays session and tool events to the Agent Swarm daemon. */

const url = () => process.env.SWARM_URL ?? "http://127.0.0.1:7777";

/** First candidate that is a non-empty string. */
function sessionIdOf(...candidates) {
  return candidates.find((c) => typeof c === "string" && c.trim() !== "");
}

export const SwarmPlugin = async ({ directory, worktree }) => {
  const cwd = directory ?? worktree ?? process.cwd();

  /** Fire-and-forget; never awaited, never throws. */
  const post = (event, body) => {
    fetch(`${url()}/hooks/opencode/${event}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...body, cwd, opencode: true }),
      signal: AbortSignal.timeout(4000),
    }).catch(() => {});
  };

  const SESSION_EVENTS = {
    "session.created": "SessionStart",
    "session.idle": "Stop",
    "session.deleted": "SessionEnd",
  };

  return {
    event: async ({ event }) => {
      const name = SESSION_EVENTS[event?.type];
      if (!name) return;
      const p = event.properties ?? {};
      const session_id = sessionIdOf(p.info?.id, p.sessionID, p.sessionId, p.id);
      if (session_id) post(name, { session_id });
    },

    "tool.execute.before": async (input, output) => {
      const session_id = sessionIdOf(input?.sessionID, input?.sessionId);
      if (session_id) post("PreToolUse", { session_id, tool_name: input?.tool, tool_input: input?.args ?? output?.args });
    },

    "tool.execute.after": async (input, output) => {
      const session_id = sessionIdOf(input?.sessionID, input?.sessionId);
      if (session_id) post("PostToolUse", { session_id, tool_name: input?.tool, tool_input: input?.args ?? output?.args });
    },
  };
};

export default SwarmPlugin;
