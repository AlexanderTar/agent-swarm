import { mkdirSync, mkdtempSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { resolveOpencodeSession } from "./opencodeSessions.js";

const require = createRequire(import.meta.url);
// eslint-disable-next-line @typescript-eslint/no-require-imports
const Database = require("better-sqlite3") as typeof import("better-sqlite3");

function makeDb(home: string, rows: Array<{ id: string; title: string; model: string; directory: string }>): void {
  mkdirSync(join(home, ".local", "share", "opencode"), { recursive: true });
  const db = new Database(join(home, ".local", "share", "opencode", "opencode.db"));
  db.exec(
    `CREATE TABLE session (
      id TEXT PRIMARY KEY, project_id TEXT NOT NULL, slug TEXT NOT NULL,
      directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL,
      agent TEXT, model TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL
    )`,
  );
  const stmt = db.prepare(
    "INSERT INTO session (id, project_id, slug, directory, title, version, agent, model, time_created, time_updated) VALUES (?, 'p', 's', ?, ?, 'v', 'build', ?, 1, 1)",
  );
  for (const r of rows) stmt.run(r.id, r.directory, r.title, r.model);
  db.close();
}

describe("resolveOpencodeSession", () => {
  it("returns undefined when DB is missing", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-opencode-missing-"));
    expect(resolveOpencodeSession("ses_x", home)).toBeUndefined();
    expect(resolveOpencodeSession("", home)).toBeUndefined();
  });

  it("resolves title, model (JSON), and cwd", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-opencode-"));
    makeDb(home, [{
      id: "ses_abc",
      title: "Fix login bug",
      model: JSON.stringify({ id: "big-pickle", providerID: "opencode" }),
      directory: "/repo/app",
    }]);
    expect(resolveOpencodeSession("ses_abc", home)).toEqual({
      sessionId: "ses_abc",
      title: "Fix login bug",
      model: "big-pickle",
      cwd: "/repo/app",
    });
    expect(resolveOpencodeSession("ses_missing", home)).toBeUndefined();
  });

  it("handles plain-string models", () => {
    const home = mkdtempSync(join(tmpdir(), "swarm-opencode-plain-"));
    makeDb(home, [{ id: "ses_p", title: "T", model: "muse-spark", directory: "/r" }]);
    expect(resolveOpencodeSession("ses_p", home)?.model).toBe("muse-spark");
  });
});
