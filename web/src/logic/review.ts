import { C, T } from "../copy";
import type { AcceptBinding, ApproveBody, GitRef, Request, RequestKind } from "../types";
import { ageAgo, sha7 } from "./format";

export const SCOPE_LABEL: Record<RequestKind, string> = {
  question: C.answer,
  confirm_repos: C.confirmRepositories,
  approve_section: C.approveSection,
  approve_plan: C.approvePlan,
  approve_report: C.approveReport,
  accept_epic: C.acceptEpic,
  accept_fix: C.acceptFix,
  close_spike: C.closeSpike,
};

export const isApprovalKind = (k: RequestKind) => k.startsWith("approve_") || k.startsWith("accept_");

export const reviewPath = (r: Request) =>
  r.item_key === r.root_key ? `${r.root_key} › ${r.item_title}` : `${r.root_key} › ${r.item_key} › ${r.item_title}`;

function revisionLine(r: Request): string | null {
  switch (r.kind) {
    case "approve_section":
      return `Spec revision ${r.artifact_revision} · Section "${r.section_title ?? ""}"`;
    case "approve_plan":
      return `Plan revision ${r.artifact_revision}`;
    case "approve_report":
      return `Report revision ${r.artifact_revision}`;
    case "accept_epic":
    case "accept_fix":
      return `Item revision ${(r.binding as AcceptBinding).item_revision}`;
    default:
      return null;
  }
}

export function reviewHeader(r: Request, now = Date.now()) {
  const title = r.kind === "question" ? reviewPath(r) : `${SCOPE_LABEL[r.kind]} · ${reviewPath(r)}`;
  const age = ageAgo(r.created_at, now);
  const by = r.agent_name ? (r.kind === "confirm_repos" ? T.proposedBy : T.requestedBy)(r.agent_name, age) : null;
  return { title, by, revision: revisionLine(r) };
}

export function approveBody(r: Request): ApproveBody {
  if (r.kind === "accept_epic" || r.kind === "accept_fix") return { binding: r.binding as AcceptBinding };
  const body: ApproveBody = { artifact_revision: r.artifact_revision ?? undefined };
  return r.section_sha256 ? { section_sha256: r.section_sha256, ...body } : body;
}

export function closeResolution(r: Request): string {
  const res = (r.binding as { resolution?: string } | null)?.resolution ?? "";
  return res.startsWith("duplicate_of:") ? T.duplicateOf(res.slice("duplicate_of:".length)) : C.noChangeNeeded;
}

export const gitBindingLine = (g: GitRef) => `${g.repo} · ${g.branch} · ${sha7(g.sha)}`;
