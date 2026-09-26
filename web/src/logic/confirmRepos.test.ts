import { describe, expect, it } from "vitest";
import { seed } from "../mock/fixtures";
import { knownRepos } from "./repos";
import { confirmError, confirmModel, confirmPayload } from "./confirmRepos";

const db = seed();
const req = db.requests.find((r) => r.id === "req_repos")!;

describe("confirm repos rules (§16.11, L25, I13)", () => {
  it("builds proposed and suggested rows", () => {
    const m = confirmModel(req, knownRepos(db.repos));
    expect(m.proposed.map((r) => [r.name, r.youSelected, r.reason])).toEqual([
      ["endurio-chat", true, "the chat API stores messages."],
      ["endurio-app", false, "the sync queue lives in the app's data layer."],
    ]);
    expect(m.proposed[0]?.subtitle).toBe("~/GitHub · EndurioApp");
    expect(m.additions.map((r) => [r.name, r.reason])).toEqual([["endurio-landing", "pricing page lists offline mode as a feature."]]);
    expect(m.initial).toEqual(["repo_chat", "repo_app"]);
    expect(m.version).toBe(0);
    expect(confirmModel(req, []).proposed[0]).toMatchObject({ name: "repo_chat", repo: null, subtitle: "" });
  });

  it("tolerates a stored null expansion", () => {
    const nullExpansion = { ...req, options: { ...(req.options as object), expansion: null } } as typeof req;
    expect(() => confirmModel(nullExpansion, knownRepos(db.repos))).not.toThrow();
    expect(confirmModel(nullExpansion, knownRepos(db.repos)).additions).toEqual([]);
  });

  it("validates and builds the payload", () => {
    expect(confirmError([])).toBe("Choose at least one repository.");
    expect(confirmError(["a"])).toBeUndefined();
    expect(confirmPayload(["a"], "  ", 3)).toEqual({ repos: ["a"], repos_version: 3 });
    expect(confirmPayload(["a"], "ok", 3)).toEqual({ repos: ["a"], comment: "ok", repos_version: 3 });
  });
});
