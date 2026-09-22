import { describe, expect, it } from "vitest";
import type { SessionState } from "../types";
import { agentActions, countLive, displayState, flattenAgents, isFinished, makeAgent, stateLabel, stateTone } from "./agentActions";

const session = (state: SessionState, extra: Partial<{ waiting: boolean; stale: boolean; tmux_alive: boolean }> = {}) => ({
  id: "ses_1", state, attempt: 1, generation: 1, waiting: false, stale: false, tmux_alive: true, started_at: 0, ended_at: null, ...extra,
});
const labels = (a: ReturnType<typeof makeAgent>) => agentActions(a).map((x) => `${x.endpoint}:${x.label}${x.disabled ? ":disabled" : ""}`);

describe("displayState and labels (§16.2)", () => {
  it.each([
    [makeAgent({ state: "queued", session: null }), "queued", "Queued", "grey"],
    [makeAgent({ session: null, preflight_error: "Codex isn't installed on this Mac." }), "preflight_failed", "Failed", "red"],
    [makeAgent({ state: "queued", session: null, preflight_error: "x" }), "preflight_failed", "Failed", "red"],
    [makeAgent({ session: session("running") }), "running", "Running", "green"],
    [makeAgent({ session: session("running", { waiting: true }) }), "waiting", "Waiting", "green-hollow"],
    [makeAgent({ session: session("running", { stale: true }) }), "stale", "No activity for 30 min", "amber"],
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
    expect(labels(makeAgent({ session: session("spawning") }))).toEqual(["terminal:Terminal", "cancel:Cancel"]);
  });
  it("running (and waiting, stale): Terminal, Pause, Cancel", () => {
    for (const s of [session("running"), session("running", { waiting: true }), session("running", { stale: true })]) {
      expect(labels(makeAgent({ session: s }))).toEqual(["terminal:Terminal", "pause:Pause", "cancel:Cancel"]);
    }
    expect(agentActions(makeAgent({ session: session("running") }))[1]?.body).toEqual({ scope: "session" });
  });
  it("running orchestrator: Pause group on the subtree", () => {
    const a = makeAgent({ role: "orchestrator", session: session("running") });
    expect(labels(a)).toEqual(["terminal:Terminal", "pause:Pause group", "cancel:Cancel"]);
    expect(agentActions(a)[1]?.body).toEqual({ scope: "subtree" });
  });
  it.each(["pause_requested", "quiescing"] as const)("%s: Terminal, a disabled Pausing…, and an enabled Cancel", (s) => {
    expect(labels(makeAgent({ session: session(s) }))).toEqual(["terminal:Terminal", "pause:Pausing…:disabled", "cancel:Cancel"]);
  });
  it("stopping: Terminal, a disabled Pausing…, and a disabled Cancel", () => {
    expect(labels(makeAgent({ session: session("stopping") }))).toEqual(["terminal:Terminal", "pause:Pausing…:disabled", "cancel:Cancel:disabled"]);
  });
  it("paused: Resume, Cancel", () => {
    expect(labels(makeAgent({ session: session("paused") }))).toEqual(["resume:Resume", "cancel:Cancel"]);
  });
  it("interrupted: Resume, Acknowledge, Cancel", () => {
    expect(labels(makeAgent({ session: session("interrupted") }))).toEqual(["resume:Resume", "ack:Acknowledge", "cancel:Cancel"]);
  });
  it.each(["crashed", "failed"] as const)("%s: Retry, Acknowledge, Terminal while the pane exists", (s) => {
    expect(labels(makeAgent({ session: session(s) }))).toEqual(["retry:Retry", "ack:Acknowledge", "terminal:Terminal"]);
    expect(labels(makeAgent({ session: session(s, { tmux_alive: false }) }))).toEqual(["retry:Retry", "ack:Acknowledge"]);
  });
  it("failed at preflight: Retry, Cancel, even while the agent is still queued", () => {
    expect(labels(makeAgent({ session: null, preflight_error: "x" }))).toEqual(["retry:Retry", "cancel:Cancel"]);
    expect(labels(makeAgent({ state: "queued", session: null, preflight_error: "x" }))).toEqual(["retry:Retry", "cancel:Cancel"]);
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
    const orch = makeAgent({ name: "auth-epic-orchestrator", role: "orchestrator", session: session("running"), children: [child, makeAgent({ name: "c3" })] });
    expect(countLive(orch)).toBe(3);
    expect(agentActions(orch).find((a) => a.endpoint === "cancel")?.confirm).toBe("Cancel auth-epic-orchestrator and its 3 agents?");
    expect(agentActions(makeAgent({ role: "orchestrator", session: session("running") })).find((a) => a.endpoint === "cancel")?.confirm).toBeUndefined();
    expect(flattenAgents([orch]).map((a) => a.name)).toEqual(["auth-epic-orchestrator", "c1", "c2", "c3"]);
  });
});
