import { describe, expect, it } from "vitest";
import { NOW, seed } from "../mock/fixtures";
import type { Request, SessionInfo } from "../types";
import { makeAgent } from "./agentActions";
import { filterRequests, inboxRow, pickRequest, requestTarget } from "./inbox";

const reqs = [...seed().requests].reverse();

describe("inbox rules (§16.11)", () => {
  it("filters and orders oldest first", () => {
    expect(filterRequests(reqs, "all").map((r) => r.id)).toEqual(["req_question", "req_q2"]);
    expect(filterRequests(reqs, "questions").map((r) => r.id)).toEqual(["req_question", "req_q2"]);
    expect(filterRequests(reqs, "approvals").map((r) => r.id)).toEqual([
      "req_accept", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
    expect(filterRequests(reqs, "reviews").map((r) => r.id)).toEqual([
      "req_accept", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
  });

  it("builds rows and picks a request", () => {
    const section = reqs.find((r) => r.id === "req_section")!;
    expect(inboxRow(section, NOW)).toEqual({ title: 'Approve "Data model"', sub: "SPIKE-3 · 12m" });
    expect(pickRequest(filterRequests(reqs, "all"), "req_q2")?.id).toBe("req_q2");
    expect(pickRequest(filterRequests(reqs, "all"), "")?.id).toBe("req_question");
    expect(pickRequest(filterRequests(reqs, "reviews"), "req_plan")?.id).toBe("req_plan");
    expect(pickRequest(filterRequests(reqs, "reviews"), "")?.id).toBe("req_accept");
    expect(pickRequest([], "x")).toBeUndefined();
  });
});

describe("requestTarget", () => {
  const q = { ...reqs.find((r) => r.id === "req_question")!, kind: "question", is_hitl: true, terminal_agent: "orch" } as Request;
  const orch = (session: Partial<SessionInfo> | null) =>
    makeAgent({ name: "orch", role: "orchestrator", session: session && { ...makeAgent().session!, ...session } });
  it("opens the terminal when the tmux session is alive", () => {
    expect(requestTarget(q, [orch({ tmux_alive: true, state: "running" })])).toEqual({ kind: "terminal", agent: "orch" });
  });
  it("says paused for a paused or interrupted orchestrator", () => {
    expect(requestTarget(q, [orch({ tmux_alive: false, state: "paused" })])).toEqual({ kind: "unavailable", hint: "Orchestrator is paused. Resume it to continue." });
    expect(requestTarget(q, [orch({ tmux_alive: false, state: "interrupted" })])).toEqual({ kind: "unavailable", hint: "Orchestrator is paused. Resume it to continue." });
  });
  it("says not running otherwise, including an unknown agent", () => {
    expect(requestTarget(q, [orch({ tmux_alive: false, state: "crashed" })])).toMatchObject({ kind: "unavailable", hint: "Orchestrator isn't running." });
    expect(requestTarget(q, [])).toMatchObject({ kind: "unavailable", hint: "Orchestrator isn't running." });
  });
  it("is null without a terminal agent or for approvals", () => {
    expect(requestTarget({ ...q, terminal_agent: null }, [])).toBeNull();
    expect(requestTarget({ ...q, kind: "approve_plan", is_hitl: false }, [])).toBeNull();
  });
});
