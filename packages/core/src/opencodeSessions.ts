import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import { homedir } from "node:os";
import { join } from "node:path";

export interface OpencodeSessionRef {
  sessionId: string;
  title?: string;
  model?: string;
  cwd?: string;
}

function parseOpencodeModel(raw: unknown): string | undefined {
  if (typeof raw !== "string" || !raw.trim()) return undefined;
  const trimmed = raw.trim();
  // Model column is usually JSON like {"id":"big-pickle","providerID":"opencode"}.
  if (trimmed.startsWith("{")) {
    try {
      const obj = JSON.parse(trimmed) as { id?: unknown; model?: unknown; name?: unknown };
      for (const key of ["id", "model", "name"] as const) {
        const v = obj[key];
        if (typeof v === "string" && v.trim()) return v.trim();
      }
      return undefined;
    } catch {
      return undefined;
    }
  }
  return trimmed;
}

/**
 * Resolve an opencode session from ~/.local/share/opencode/opencode.db.
 * Read-only, best-effort — returns undefined on any failure (WAL-locked, missing DB, etc.).
 */
export function resolveOpencodeSession(sessionId: string, home = homedir()): OpencodeSessionRef | undefined {
  try {
    if (!sessionId?.trim()) return undefined;
    const dbPath = join(home, ".local", "share", "opencode", "opencode.db");
    if (!existsSync(dbPath)) return undefined;

    // Lazy import so hooks never fail when better-sqlite3 is unavailable.
    const require = createRequire(import.meta.url);
    const Database = require("better-sqlite3") as typeof import("better-sqlite3");
    let db: import("better-sqlite3").Database | undefined;
    try {
      db = new Database(dbPath, { readonly: true, fileMustExist: true });
      const row = db
        .prepare("SELECT title, model, directory FROM session WHERE id = ? LIMIT 1")
        .get(sessionId.trim()) as { title?: unknown; model?: unknown; directory?: unknown } | undefined;
      if (!row) return undefined;
      const title = typeof row.title === "string" && row.title.trim() ? row.title.trim() : undefined;
      const model = parseOpencodeModel(row.model);
      const cwd = typeof row.directory === "string" && row.directory.trim() ? row.directory.trim() : undefined;
      if (!title && !model && !cwd) return undefined;
      return { sessionId: sessionId.trim(), title, model, cwd };
    } finally {
      try {
        db?.close();
      } catch {
        /* ignore close errors */
      }
    }
  } catch {
    return undefined;
  }
}
