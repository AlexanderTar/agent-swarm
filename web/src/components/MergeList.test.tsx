import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ItemMerge } from "../types";
import { MergeList } from "./MergeList";

const failing: ItemMerge = { repo: "endurio", kind: "pr", url: "https://github.com/o/endurio/pull/88", number: 88, base: "main", head: "swarm/epic-14", auto_merge: false, state: "open", checks: "failing" };
const merges: ItemMerge[] = [
  { repo: "agent-swarm", kind: "pr", url: "https://github.com/o/agent-swarm/pull/412", number: 412, base: "main", head: "swarm/epic-14", auto_merge: true, state: "open", checks: "passing" },
  failing,
  { repo: "docs", kind: "local", base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "", merged_sha: "81d0e44aa" },
  { repo: "web", kind: "pr", url: "https://github.com/o/web/pull/5", number: 5, base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "passing" },
];

describe("MergeList", () => {
  it("renders one row per repo with its PR state", () => {
    render(<MergeList merges={merges} />);
    expect(screen.getByRole("heading", { name: "Awaiting merge" })).toBeInTheDocument();
    const rows = screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(4);
    for (const s of ["agent-swarm", "#412", "✓ passing", "auto-merge on"]) expect(rows[0]).toContain(s);
    for (const s of ["endurio", "#88", "✗ failing", "(orchestrator fixing)"]) expect(rows[1]).toContain(s);
    for (const s of ["docs", "merged locally 81d0e44"]) expect(rows[2]).toContain(s);
    for (const s of ["web", "#5", "merged"]) expect(rows[3]).toContain(s);
    expect(rows[3]).not.toContain("passing");
    const links = screen.getAllByRole("link");
    expect(links).toHaveLength(3);
    expect(links[0]).toHaveAttribute("href", "https://github.com/o/agent-swarm/pull/412");
    expect(links[0]).toHaveAttribute("target", "_blank");
    expect(links[0]).toHaveAttribute("rel", "noreferrer");
    expect(links.map((link) => link.getAttribute("aria-label"))).toEqual([
      "Open agent-swarm pull request #412",
      "Open endurio pull request #88",
      "Open web pull request #5",
    ]);
    for (const link of links) expect(link).toHaveClass("size-8");
  });

  it("shows a kept repo as Kept with the orchestrator's note", () => {
    render(<MergeList merges={[{ repo: "docs", kind: "kept", base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "", note: "branch stays for review" }]} />);
    const row = screen.getByRole("listitem").textContent ?? "";
    for (const s of ["docs", "Kept", "branch stays for review"]) expect(row).toContain(s);
    expect(row).not.toContain("merged");
  });

  it("shows pending and missing checks on open PRs", () => {
    render(<MergeList merges={[{ ...failing, checks: "pending" }, { ...failing, repo: "other", checks: "" }]} />);
    const rows = screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
    expect(rows[0]).toContain("… pending");
    expect(rows[0]).not.toContain("(orchestrator fixing)");
    expect(rows[1]).toContain("no checks");
  });

  it("uses a neutral heading once nothing is awaiting a merge, and wraps long notes", () => {
    const note = "https://example.com/" + "a".repeat(200);
    render(<MergeList merges={[{ repo: "docs", kind: "kept", base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "", note }]} />);
    expect(screen.getByRole("heading", { name: "Repos" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Repos" })).toBeInTheDocument();
    expect(screen.getByText(/^Kept · /)).toHaveClass("min-w-0", "break-words");
  });
});
