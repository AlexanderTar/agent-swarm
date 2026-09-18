import { describe, expect, it } from "vitest";
import { NOW, seed } from "../mock/fixtures";
import type { Request } from "../types";
import { SCOPE_LABEL, approveBody, closeResolution, gitBindingLine, isApprovalKind, reviewHeader, reviewPath } from "./review";

const req = (id: string) => seed().requests.find((r) => r.id === id) as Request;

describe("review rules (§16.11)", () => {
  it("labels scopes", () => {
    expect(SCOPE_LABEL).toEqual({
      question: "Answer", confirm_repos: "Confirm repositories", approve_section: "Approve section", approve_plan: "Approve plan",
      approve_report: "Approve report", accept_epic: "Accept epic", accept_fix: "Accept fix", close_spike: "Close spike",
    });
    expect(isApprovalKind("accept_fix")).toBe(true);
    expect(isApprovalKind("close_spike")).toBe(false);
  });

  it("builds header lines", () => {
    expect(reviewHeader(req("req_section"), NOW)).toEqual({
      title: "Approve section · SPIKE-3 › Offline mode",
      by: "Requested by offline-spike-orchestrator · 12 min ago",
      revision: 'Spec revision 3 · Section "Data model"',
    });
    expect(reviewHeader(req("req_repos"), NOW)).toMatchObject({ title: "Confirm repositories · SPIKE-3 › Offline mode", by: "Proposed by offline-spike-orchestrator · 3 min ago", revision: null });
    expect(reviewHeader(req("req_plan"), NOW).revision).toBe("Plan revision 1");
    expect(reviewHeader(req("req_report"), NOW)).toMatchObject({ by: null, revision: "Report revision 2" });
    expect(reviewHeader(req("req_accept"), NOW).revision).toBe("Item revision 7");
    expect(reviewPath(req("req_q2"))).toBe("EPIC-12 › TASK-104 › Validate inputs");
    expect(reviewHeader(req("req_q2"), NOW).title).toBe("EPIC-12 › TASK-104 › Validate inputs");
  });

  it("builds approve bodies and resolutions", () => {
    expect(approveBody(req("req_section"))).toEqual({ section_sha256: "sha-dm-3", artifact_revision: 3 });
    expect(approveBody(req("req_plan"))).toEqual({ section_sha256: "sha-plan-1", artifact_revision: 1 });
    expect(approveBody(req("req_accept"))).toEqual({ binding: req("req_accept").binding });
    expect(closeResolution(req("req_close"))).toBe("Duplicate of EPIC-12");
    expect(closeResolution({ ...req("req_close"), binding: { resolution: "no_change" } })).toBe("No change needed");
    expect(gitBindingLine({ repo: "endurio-chat", branch: "epic/epic-12-authentication", sha: "a1b2c3d4e5f6" })).toBe("endurio-chat · epic/epic-12-authentication · a1b2c3d");
  });
});
