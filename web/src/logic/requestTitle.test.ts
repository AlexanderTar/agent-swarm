import { describe, expect, it } from "vitest";
import { seed } from "../mock/fixtures";
import { ARTIFACT_LABEL, requestTitle } from "./requestTitle";

describe("requestTitle", () => {
  it("names each request kind", () => {
    const byId = Object.fromEntries(seed().requests.map((r) => [r.id, requestTitle(r)]));
    expect(byId).toEqual({
      req_accept: "Accept epic",
      req_question: "Which sync strategy?",
      req_q2: "Which validation library?",
      req_section: 'Approve "Data model"',
      req_plan: "Approve plan",
      req_report: "Approve report",
      req_close: "Close spike?",
      req_repos: "Confirm 2 repositories",
      req_fix: "Accept fix",
    });
    const long = { ...seed().requests[1]!, prompt: "x".repeat(100) };
    expect(requestTitle(long)).toHaveLength(80);
    expect(ARTIFACT_LABEL).toEqual({ spec: "Spec", plan: "Plan", debug_report: "Report", note: "Note" });
  });
});
