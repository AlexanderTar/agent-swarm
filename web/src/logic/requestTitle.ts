import { C, T } from "../copy";
import type { Artifact, ConfirmReposOptions, Request } from "../types";
import { truncate } from "./format";

export function requestTitle(r: Request): string {
  switch (r.kind) {
    case "question":
    case "prompt":
    case "blocker":
      return truncate(r.prompt, 80);
    case "approve_section":
      return T.approveSectionRow(r.section_title ?? "");
    case "approve_plan":
      return C.approvePlan;
    case "approve_report":
      return C.approveReport;
    case "accept_epic":
      return C.acceptEpic;
    case "accept_fix":
      return C.acceptFix;
    case "confirm_repos":
      return T.confirmNRepos((r.options as ConfirmReposOptions).proposed.length);
    case "close_spike":
      return C.closeSpikeRow;
  }
}

export const ARTIFACT_LABEL: Record<Artifact["kind"], string> = { spec: "Spec", plan: "Plan", debug_report: "Report", note: "Note" };
