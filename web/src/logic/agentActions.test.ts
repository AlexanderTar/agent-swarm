import { describe, expect, it } from "vitest";
import type { SessionState } from "../types";
import {
  agentActions,
  countLive,
  displayState,
  flattenAgents,
  isFinished,
  makeAgent,
  stateLabel,
  stateText,
  stateTone,
} from "./agentActions";

const session = (
  state: SessionState,
  extra: Partial<{
    waiting: boolean;
    stale: boolean;
    tmux_alive: boolean;
    busy_since: number | null;
    messages_waiting: number;
  }> = {},
) => ({
  id: "ses_1",
  state,
  attempt: 1,
  generation: 1,
  waiting: false,
  stale: false,
  tmux_alive: true,
  started_at: 0,
  ended_at: null,
  ...extra,
});
const labels = (a: ReturnType<typeof makeAgent>) =>
  agentActions(a).map((x) => `${x.endpoint}:${x.label}${x.disabled ? ":disabled" : ""}`);

describe("busy workers", () => {
  const MIN = 60_000;
  const busy = (extra = {}) =>
    makeAgent({ session: session("running", { busy_since: 100 * MIN, ...extra }) });

  it("a busy worker is busy, not stale, and keeps the running actions", () => {
    const a = busy({ stale: true });
    expect(displayState(a)).toBe("busy");
    expect(stateTone("busy")).toBe("green");
    expect(labels(a)).toEqual(["terminal:Terminal", "pause:Pause", "cancel:Cancel"]);
  });
  it("shows how long it has been busy and how many messages wait", () => {
    expect(stateText(busy({ messages_waiting: 2 }), 112 * MIN)).toBe(
      "Busy · running a long task · 2 messages waiting · for 12 min",
    );
    expect(stateText(busy({ messages_waiting: 1 }), 105 * MIN)).toBe(
      "Busy · running a long task · 1 message waiting · for 5 min",
    );
    expect(stateText(busy(), 105 * MIN)).toBe("Busy · running a long task · for 5 min");
  });
  it("has a compact form for a narrow Kanban card", () => {
    expect(stateText(busy({ messages_waiting: 2 }), 112 * MIN, true)).toBe(
      "Busy · 2 waiting · 12 min",
    );
    expect(stateText(busy(), 112 * MIN, true)).toBe("Busy · 12 min");
    expect(stateText(makeAgent({ session: session("running") }), 0, true)).toBe("Running");
  });
  it("a worker with no busy_since reads Running even mid-tool (the daemon holds the label back)", () => {
    expect(stateText(makeAgent({ session: session("running", { busy_since: null }) }))).toBe(
      "Running",
    );
  });
  it("an idle worker keeps its plain label", () => {
    const a = makeAgent({ session: session("running", { stale: true }) });
    expect(stateText(a, 0)).toBe("No activity for 30 min");
  });
  it("a waiting prompt wins over a stale busy flag", () => {
    expect(
      displayState(makeAgent({ session: session("running", { waiting: true, busy_since: 1 }) })),
    ).toBe("waiting");
  });
});

describe("displayState and labels (§16.2)", () => {
  it.each([
    [makeAgent({ state: "queued", session: null }), "queued", "Queued", "grey"],
    [
      makeAgent({ session: null, preflight_error: "Codex isn't installed on this Mac." }),
      "preflight_failed",
      "Failed",
      "red",
    ],
    [
      makeAgent({ state: "queued", session: null, preflight_error: "x" }),
      "preflight_failed",
      "Failed",
      "red",
    ],
    [makeAgent({ session: session("running") }), "running", "Running", "green"],
    [
      makeAgent({ session: session("running", { waiting: true }) }),
      "waiting",
      "Waiting",
      "green-hollow",
    ],
    [
      makeAgent({ session: session("running", { stale: true }) }),
      "stale",
      "No activity for 30 min",
      "amber",
    ],
    [makeAgent({ session: session("spawning") }), "spawning", "Starting", "grey-pulse"],
    [makeAgent({ session: session("quiescing") }), "quiescing", "Finishing current step", "amber"],
    [makeAgent({ session: session("paused") }), "paused", "Paused", "hollow"],
    [makeAgent({ session: session("crashed") }), "crashed", "Crashed", "red"],
  ] as const)("%#", (agent, state, label, tone) => {
    expect(displayState(agent)).toBe(state);
    expect(stateLabel(displayState(agent))).toBe(label);
    expect(stateTone(displayState(agent))).toBe(tone);
  });
});

describe("agentActions (§10.7, one case per row)", () => {
  it("queued: Cancel", () => {
    expect(labels(makeAgent({ state: "queued", session: null }))).toEqual(["cancel:Cancel"]);
  });
  it("spawning: Open terminal, Cancel", () => {
    expect(labels(makeAgent({ session: session("spawning") }))).toEqual([
      "terminal:Terminal",
      "cancel:Cancel",
    ]);
  });
  it("running (and waiting, stale): Terminal, Pause, Cancel", () => {
    for (const s of [
      session("running"),
      session("running", { waiting: true }),
      session("running", { stale: true }),
    ]) {
      expect(labels(makeAgent({ session: s }))).toEqual([
        "terminal:Terminal",
        "pause:Pause",
        "cancel:Cancel",
      ]);
    }
    expect(agentActions(makeAgent({ session: session("running") }))[1]?.body).toEqual({
      scope: "session",
    });
  });
  it("running orchestrator: Pause group on the subtree", () => {
    const a = makeAgent({ role: "orchestrator", session: session("running") });
    expect(labels(a)).toEqual(["terminal:Terminal", "pause:Pause group", "cancel:Cancel"]);
    expect(agentActions(a)[1]?.body).toEqual({ scope: "subtree" });
  });
  it.each(["pause_requested", "quiescing"] as const)(
    "%s: Terminal, a disabled Pausing…, and an enabled Cancel",
    (s) => {
      expect(labels(makeAgent({ session: session(s) }))).toEqual([
        "terminal:Terminal",
        "pause:Pausing…:disabled",
        "cancel:Cancel",
      ]);
    },
  );
  it("stopping: Terminal, a disabled Pausing…, and a disabled Cancel", () => {
    expect(labels(makeAgent({ session: session("stopping") }))).toEqual([
      "terminal:Terminal",
      "pause:Pausing…:disabled",
      "cancel:Cancel:disabled",
    ]);
  });
  it("paused: Resume, Cancel", () => {
    expect(labels(makeAgent({ session: session("paused") }))).toEqual([
      "resume:Resume",
      "cancel:Cancel",
    ]);
  });
  it("interrupted: Resume, Acknowledge, Cancel", () => {
    expect(labels(makeAgent({ session: session("interrupted") }))).toEqual([
      "resume:Resume",
      "ack:Acknowledge",
      "cancel:Cancel",
    ]);
  });
  it.each(["crashed", "failed"] as const)(
    "%s: Retry, Acknowledge, Terminal while the pane exists",
    (s) => {
      expect(labels(makeAgent({ session: session(s) }))).toEqual([
        "retry:Retry",
        "ack:Acknowledge",
        "terminal:Terminal",
      ]);
      expect(labels(makeAgent({ session: session(s, { tmux_alive: false }) }))).toEqual([
        "retry:Retry",
        "ack:Acknowledge",
      ]);
    },
  );
  it("failed at preflight: Retry, Cancel, even while the agent is still queued", () => {
    expect(labels(makeAgent({ session: null, preflight_error: "x" }))).toEqual([
      "retry:Retry",
      "cancel:Cancel",
    ]);
    expect(labels(makeAgent({ state: "queued", session: null, preflight_error: "x" }))).toEqual([
      "retry:Retry",
      "cancel:Cancel",
    ]);
  });
  it("completed, cancelled and acknowledged: none", () => {
    expect(labels(makeAgent({ state: "finished", session: session("completed") }))).toEqual([]);
    expect(labels(makeAgent({ state: "finished", session: session("cancelled") }))).toEqual([]);
    expect(labels(makeAgent({ state: "acknowledged", session: session("crashed") }))).toEqual([]);
    expect(isFinished(makeAgent({ state: "acknowledged" }))).toBe(true);
    expect(isFinished(makeAgent({ state: "active" }))).toBe(false);
  });
  it("asks before cancelling an orchestrator with agents", () => {
    const child = makeAgent({ name: "c1", children: [makeAgent({ name: "c2" })] });
    const orch = makeAgent({
      name: "auth-epic-orchestrator",
      role: "orchestrator",
      session: session("running"),
      children: [child, makeAgent({ name: "c3" })],
    });
    expect(countLive(orch)).toBe(3);
    expect(agentActions(orch).find((a) => a.endpoint === "cancel")?.confirm).toBe(
      "Cancel auth-epic-orchestrator and its 3 agents?",
    );
    expect(
      agentActions(makeAgent({ role: "orchestrator", session: session("running") })).find(
        (a) => a.endpoint === "cancel",
      )?.confirm,
    ).toBeUndefined();
    expect(flattenAgents([orch]).map((a) => a.name)).toEqual([
      "auth-epic-orchestrator",
      "c1",
      "c2",
      "c3",
    ]);
  });
});

describe("agent limit states (spec 2026-09-27-single-agent-limit-live)", () => {
  const op = (reason: "capacity" | "resume", phase = "queued") => ({
    operation_id: "op_1",
    agent: "a",
    mode: "handoff",
    phase,
    reason,
  });

  it("capacity pause: label, tone, Cancel only", () => {
    const a = makeAgent({ session: session("interrupted"), replacement: op("capacity") });
    expect(displayState(a)).toBe("capacity_paused");
    expect(stateLabel(displayState(a))).toBe("Paused: over the agent limit");
    expect(stateTone(displayState(a))).toBe("hollow");
    expect(labels(a)).toEqual(["cancel:Cancel"]);
  });

  it("queued resume: label, tone, Cancel only", () => {
    const a = makeAgent({ session: session("paused"), replacement: op("resume") });
    expect(displayState(a)).toBe("resume_queued");
    expect(stateLabel(displayState(a))).toBe("Queued: waiting for a free slot");
    expect(stateTone(displayState(a))).toBe("grey");
    expect(labels(a)).toEqual(["cancel:Cancel"]);
  });

  it("starting and reasonless replacements fall back to the session state", () => {
    expect(
      displayState(
        makeAgent({ session: session("spawning"), replacement: op("capacity", "starting") }),
      ),
    ).toBe("spawning");
    expect(
      displayState(
        makeAgent({
          session: session("running"),
          replacement: { operation_id: "op_2", agent: "a", mode: "handoff", phase: "stopping" },
        }),
      ),
    ).toBe("running");
  });
});
