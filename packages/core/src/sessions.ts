import type Database from "better-sqlite3";
import type { AgentKind, SessionRecord, TaskSessionLabel } from "./types.js";

/** A session counts as live while it is unfinished and was seen in the last two minutes. */
const ACTIVE_SQL = "(ended_at IS NULL AND last_seen_at > datetime('now', '-2 minutes'))";

function rowToSession(row: Record<string, unknown>): SessionRecord {
  return {
    id: row.id as string,
    agentKind: row.agent_kind as AgentKind,
    cwd: (row.cwd as string) ?? null,
    model: (row.model as string) ?? null,
    pid: (row.pid as number) ?? null,
    taskId: (row.task_id as number) ?? null,
    parentSessionId: (row.parent_session_id as string) ?? null,
    transcriptPath: (row.transcript_path as string) ?? null,
    startedAt: row.started_at as string,
    lastSeenAt: (row.last_seen_at as string) ?? null,
    endedAt: (row.ended_at as string) ?? null,
  };
}

export class SessionService {
  constructor(private db: Database.Database) {}

  upsert(input: {
    id: string;
    agent: AgentKind;
    cwd?: string;
    model?: string;
    pid?: number;
    parentSessionId?: string;
    transcriptPath?: string;
  }): SessionRecord {
    this.db
      .prepare(
        `INSERT INTO sessions (id, agent_kind, cwd, model, pid, parent_session_id, transcript_path, started_at, last_seen_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
         ON CONFLICT(id) DO UPDATE SET
           agent_kind = CASE WHEN excluded.agent_kind = 'unknown' THEN sessions.agent_kind ELSE excluded.agent_kind END,
           cwd = COALESCE(excluded.cwd, sessions.cwd),
           model = COALESCE(excluded.model, sessions.model),
           pid = COALESCE(excluded.pid, sessions.pid),
           parent_session_id = COALESCE(excluded.parent_session_id, sessions.parent_session_id),
           transcript_path = COALESCE(excluded.transcript_path, sessions.transcript_path),
           last_seen_at = datetime('now')`,
      )
      .run(
        input.id,
        input.agent,
        input.cwd ?? null,
        input.model ?? null,
        input.pid ?? null,
        input.parentSessionId ?? null,
        input.transcriptPath ?? null,
      );
    return this.get(input.id)!;
  }

  get(id: string): SessionRecord | null {
    const row = this.db.prepare("SELECT * FROM sessions WHERE id = ?").get(id);
    return row ? rowToSession(row as Record<string, unknown>) : null;
  }

  touch(id: string): void {
    this.db.prepare("UPDATE sessions SET last_seen_at = datetime('now') WHERE id = ?").run(id);
  }

  end(id: string): void {
    this.db.prepare("UPDATE sessions SET ended_at = datetime('now') WHERE id = ?").run(id);
  }

  /** Attach a session to a task (additive join). Returns false when the session is unknown. */
  bind(sessionId: string, taskId: number): boolean {
    const result = this.db
      .prepare("UPDATE sessions SET task_id = ?, last_seen_at = datetime('now') WHERE id = ?")
      .run(taskId, sessionId);
    return result.changes > 0;
  }

  /** Explicit id wins; otherwise the most recently seen live session in `cwd`. */
  resolve(opts: { sessionId?: string; cwd?: string }): SessionRecord | null {
    if (opts.sessionId?.trim()) return this.get(opts.sessionId.trim());
    if (!opts.cwd?.trim()) return null;
    // ponytail: two agents in one cwd can bind to the wrong row; pass sessionId explicitly to be exact.
    const row = this.db
      .prepare(
        `SELECT * FROM sessions WHERE cwd = ? AND ended_at IS NULL
         ORDER BY last_seen_at DESC, started_at DESC LIMIT 1`,
      )
      .get(opts.cwd.trim());
    return row ? rowToSession(row as Record<string, unknown>) : null;
  }

  listByTask(taskId: number): SessionRecord[] {
    return (
      this.db
        .prepare("SELECT * FROM sessions WHERE task_id = ? ORDER BY started_at")
        .all(taskId) as Record<string, unknown>[]
    ).map(rowToSession);
  }

  labelsForTasks(taskIds: number[]): Map<number, TaskSessionLabel[]> {
    const labels = new Map<number, TaskSessionLabel[]>();
    if (taskIds.length === 0) return labels;
    const rows = this.db
      .prepare(
        `SELECT task_id, id, agent_kind, model, ${ACTIVE_SQL} AS active FROM sessions
         WHERE task_id IN (${taskIds.map(() => "?").join(",")})
         ORDER BY started_at`,
      )
      .all(...taskIds) as Array<{
      task_id: number;
      id: string;
      agent_kind: AgentKind;
      model: string | null;
      active: number;
    }>;
    for (const row of rows) {
      const list = labels.get(row.task_id) ?? [];
      list.push({
        sessionId: row.id,
        agent: row.agent_kind,
        model: row.model,
        active: row.active === 1,
      });
      labels.set(row.task_id, list);
    }
    return labels;
  }
}
