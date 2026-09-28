import { describe, expect, it } from "vitest";
import type { Repo, ReposResponse } from "../types";
import { chooserRows, knownRepos, reconcileSelection, scanLine, selectedLine, shortPath, toggleRepo } from "./repos";

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
  it("shortens home paths but keeps other paths", () => {
    expect(shortPath("/Users/alex/GitHub/endurio-chat")).toBe("~/GitHub/endurio-chat");
    expect(shortPath("/opt/src/x")).toBe("/opt/src/x");
  });

  it("selects, toggles and summarises", () => {
    expect(toggleRepo([], "r1")).toEqual(["r1"]);
    expect(toggleRepo(["r1", "r2"], "r1")).toEqual(["r2"]);
    expect(selectedLine(["r1", "r3"], knownRepos(resp))).toBe("Selected: endurio-chat, endurio-landing");
    expect(selectedLine([], knownRepos(resp))).toBe("");
    expect(knownRepos(resp).map((r) => r.id)).toEqual(["r1", "r2", "r3", "r4"]);
  });

  it("describes the scan", () => {
    expect(scanLine({ ...resp, scanning: true })).toBe("Scanning your home folder…");
    expect(scanLine({ ...resp, scanned_at: 3_600_000 }, 3 * 3_600_000)).toBe("Scanned 2h ago");
    // Ruling (fix round 2): no usable scanned_at means never scanned, not a 20000-day age.
    expect(scanLine({ ...resp, scanned_at: 0 }, 2 * 3_600_000)).toBe("Never scanned");
    expect(scanLine({ ...resp, scanned_at: -1 }, 2 * 3_600_000)).toBe("Never scanned");
  });
});

describe("repo chooser (menubar parity)", () => {
  it("uses all, drops missing, dedupes by path, sorts by name then path", () => {
    const data: ReposResponse = {
      ...resp,
      recent: [repo("x", "zzz")],
      groups: [],
      all: [
        repo("b", "beta"),
        repo("a2", "alpha", { path: "/Users/alex/work/alpha" }),
        repo("a1", "alpha"),
        repo("dup", "beta"),
        repo("missing", "aaa", { missing: true }),
      ],
    };
    expect(chooserRows(data).map((r) => r.id)).toEqual(["a1", "a2", "b"]);
  });

  it("drops selections that are no longer rows", () => {
    const rows = [repo("a", "a"), repo("b", "b")];
    expect(reconcileSelection(["a", "c", "d"], rows)).toEqual({ selection: ["a"], removed: 2 });
    expect(reconcileSelection(["b", "a"], rows)).toEqual({ selection: ["b", "a"], removed: 0 });
  });
});
