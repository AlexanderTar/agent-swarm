import { describe, expect, it } from "vitest";
import type { Repo, ReposResponse } from "../types";
import { knownRepos, parentFolder, repoSections, repoSubtitle, scanLine, selectAll, selectedLine, toggleRepo } from "./repos";

const repo = (id: string, name: string, p: Partial<Repo> = {}): Repo => ({
  id, name, path: `/Users/alex/GitHub/${name}`, remote_url: null, remote_owner: null, default_branch: "main",
  source: "scan", groups: [], missing: false, dirty: false, last_used_at: null, ...p,
});
const chat = repo("r1", "endurio-chat", { remote_owner: "EndurioApp" });
const app = repo("r2", "endurio-app", { remote_owner: "EndurioApp" });
const land = repo("r3", "endurio-landing", { remote_owner: "AlexanderTar" });
const gone = repo("r4", "old-api", { missing: true });

const resp: ReposResponse = {
  recent: [chat],
  groups: [
    { name: "EndurioApp", source: "remote_owner", repos: [chat, app] },
    { name: "endurio", source: "workspace_dir", repos: [chat, app] },
    { name: "endurio", source: "code_workspace", repos: [land] },
    { name: "AlexanderTar", source: "remote_owner", repos: [land] },
  ],
  all: [chat, app, land, gone],
  scanned_at: 0,
  scanning: false,
};

describe("repo picker rules (§16.3)", () => {
  it("abbreviates the parent folder and builds the subtitle", () => {
    expect(parentFolder("/Users/alex/GitHub/endurio-chat")).toBe("~/GitHub");
    expect(parentFolder("/opt/src/x")).toBe("/opt/src");
    expect(repoSubtitle(chat)).toBe("~/GitHub · EndurioApp");
    expect(repoSubtitle(gone)).toBe("~/GitHub");
  });

  it("orders sections Recent, local groups, owner groups, All and merges same-name groups", () => {
    const s = repoSections(resp);
    expect(s.map((x) => x.title)).toEqual(["Recent", "endurio", "AlexanderTar", "EndurioApp", "All"]);
    expect(s[1]?.repos.map((r) => r.name)).toEqual(["endurio-chat", "endurio-app", "endurio-landing"]);
    expect(s[1]?.groupName).toBe("endurio");
    expect(repoSections({ ...resp, recent: [], groups: [], all: [] })).toEqual([]);
  });

  it("selects, toggles and summarises", () => {
    expect(toggleRepo([], "r1")).toEqual(["r1"]);
    expect(toggleRepo(["r1", "r2"], "r1")).toEqual(["r2"]);
    expect(selectAll(["r2"], [chat, app, gone])).toEqual(["r2", "r1"]);
    expect(selectedLine(["r1", "r3"], knownRepos(resp))).toBe("Selected: endurio-chat, endurio-landing");
    expect(selectedLine([], knownRepos(resp))).toBe("");
    expect(knownRepos(resp).map((r) => r.id)).toEqual(["r1", "r2", "r3", "r4"]);
  });

  it("describes the scan", () => {
    expect(scanLine({ ...resp, scanning: true })).toBe("Scanning your home folder…");
    expect(scanLine({ ...resp, scanned_at: 0 }, 2 * 3_600_000)).toBe("Scanned 2h ago");
  });
});
