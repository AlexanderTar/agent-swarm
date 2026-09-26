import { describe, expect, it } from "vitest";
import { seed } from "../mock/fixtures";
import type { Request, SessionInfo } from "../types";
import { makeAgent } from "./agentActions";
import { filterRequests, needsYou, needsYouRow, pickRequest, requestTarget } from "./inbox";

const reqs = [...seed().requests].reverse();

// Minimal Request factory for the shared Needs-you rules (spec 4.4), independent of the board fixtures.
const req = (p: Partial<Request> & Pick<Request, "id">): Request => ({
  kind: "question", is_hitl: true, agent_name: null, terminal_agent: null,
  item_key: "TASK-1", item_title: "Task", root_key: "TASK-1", artifact_id: null, artifact_revision: null,
  section_id: null, section_title: null, section_sha256: null, prompt: "", options: [], state: "open",
  confirmed: null, binding: null, response_text: null, responded_via: null, responded_at: null,
  created_at: 0, native_pending: false, approval_evidence: null, ...p,
});

describe("inbox rules (§16.11)", () => {
  it("filters and orders oldest first", () => {
    // Every open request is in "all" now (2.2.5), not just the HITL ones.
    expect(filterRequests(reqs, "all").map((r) => r.id)).toEqual([
      "req_accept", "req_question", "req_q2", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
    expect(filterRequests(reqs, "questions").map((r) => r.id)).toEqual(["req_question", "req_q2"]);
    expect(filterRequests(reqs, "approvals").map((r) => r.id)).toEqual([
      "req_section", "req_plan", "req_report", "req_close", "req_repos",
    ]);
    expect(filterRequests(reqs, "reviews").map((r) => r.id)).toEqual(["req_accept", "req_fix"]);
  });

  it("needsYou = open and not native_pending, oldest first, every kind", () => {
    const rs = [
      req({ id: "q", kind: "question", is_hitl: true, created_at: 1 }),
      req({ id: "a", kind: "approve_section", is_hitl: false, created_at: 2 }),
      req({ id: "p", kind: "approve_plan", native_pending: true, created_at: 3 }),
      req({ id: "e", kind: "accept_epic", created_at: 4 }),
      req({ id: "x", kind: "question", state: "answered", created_at: 5 }),
    ];
    expect(needsYou(rs).map((r) => r.id)).toEqual(["q", "a", "e"]);
    expect(filterRequests(rs, "all").map((r) => r.id)).toEqual(["q", "a", "e"]);
    expect(filterRequests(rs, "questions").map((r) => r.id)).toEqual(["q"]);
    expect(filterRequests(rs, "approvals").map((r) => r.id)).toEqual(["a"]);
    expect(filterRequests(rs, "reviews").map((r) => r.id)).toEqual(["e"]);
  });

  it("needsYouRow is generic and never shows the prompt", () => {
    const r = req({
      id: "r", item_key: "SPIKE-16", item_title: "go-migration-agent-debug",
      agent_name: "go-migration-agent-debug", prompt: "SECRET",
    });
    expect(needsYouRow(r)).toEqual(["SPIKE-16 · go-migration-agent-debug", "go-migration-agent-debug", "Waiting for your input"]);
    expect(needsYouRow({ ...r, agent_name: null, terminal_agent: null })[1]).toBe("—");
  });

  it("picks a request", () => {
    expect(pickRequest(filterRequests(reqs, "all"), "req_q2")?.id).toBe("req_q2");
    expect(pickRequest(filterRequests(reqs, "all"), "")?.id).toBe("req_accept");
    expect(pickRequest(filterRequests(reqs, "reviews"), "req_fix")?.id).toBe("req_fix");
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
  it("is null only without a terminal agent; approvals with one get a target too", () => {
    expect(requestTarget({ ...q, terminal_agent: null }, [])).toBeNull();
    expect(requestTarget({ ...q, kind: "approve_plan", is_hitl: false }, [])).toMatchObject({ kind: "unavailable" });
  });

  it("requestTarget works for approvals with a terminal_agent", () => {
    const liveO = makeAgent({ name: "o", role: "orchestrator", session: { ...makeAgent().session!, tmux_alive: true, state: "running" } });
    expect(requestTarget(req({ id: "r", kind: "approve_plan", is_hitl: false, terminal_agent: "o" }), [liveO]))
      .toEqual({ kind: "terminal", agent: "o" });
  });
});
