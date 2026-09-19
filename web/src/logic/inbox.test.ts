import { describe, expect, it } from "vitest";
import { NOW, seed } from "../mock/fixtures";
import { filterRequests, inboxRow, pickRequest } from "./inbox";

const reqs = [...seed().requests].reverse();

describe("inbox rules (§16.11)", () => {
  it("filters and orders oldest first", () => {
    expect(filterRequests(reqs, "all").map((r) => r.id)).toEqual([
      "req_accept", "req_question", "req_q2", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
    expect(filterRequests(reqs, "questions").map((r) => r.id)).toEqual(["req_question", "req_q2"]);
    expect(filterRequests(reqs, "approvals").map((r) => r.id)).toEqual([
      "req_accept", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
  });

  it("builds rows and picks a request", () => {
    const section = reqs.find((r) => r.id === "req_section")!;
    expect(inboxRow(section, NOW)).toEqual({ title: 'Approve "Data model"', sub: "SPIKE-3 · 12m" });
    expect(pickRequest(filterRequests(reqs, "all"), "req_plan")?.id).toBe("req_plan");
    expect(pickRequest(filterRequests(reqs, "all"), "")?.id).toBe("req_accept");
    expect(pickRequest([], "x")).toBeUndefined();
  });
});
