import type Database from "better-sqlite3";
import { randomUUID } from "node:crypto";
import { shouldReplaceSessionTitle, isFallbackSessionTitle } from "./sessionTitles.js";
import type { AgentKind, BoardFilters, HandoffNote, TaskRecord, TaskStatus } from "./types.js";

function rowToTask(row: Record<string, unknown>): TaskRecord {  return {
    id: row.id as number,
    key: row.key as string,
    title: row.title as string,
    status: row.status as TaskStatus,
    priority: row.priority as string,
    repoPath: (row.repo_path as string) ?? null,
    repoRemote: (row.repo_remote as string) ?? null,
    branch: (row.branch as string) ?? null,
    worktree: (row.worktree as string) ?? null,
    originAgent: row.origin_agent as AgentKind,
    originSessionId: (row.origin_session_id as string) ?? null,
    originModel: (row.origin_model as string) ?? null,
    originCwd: (row.origin_cwd as string) ?? null,
    originPid: (row.origin_pid as number) ?? null,
    parentTaskId: (row.parent_task_id as number) ?? null,
    required: Number(row.required ?? 1) !== 0,
    coordinatorSessionId: (row.coordinator_session_id as string) ?? null,
    claimedBy: (row.claimed_by as string) ?? null,
    claimedAgent: (row.claimed_agent as AgentKind) ?? null,
    claimedSessionId: (row.claimed_session_id as string) ?? null,
    claimToken: (row.claim_token as string) ?? null,
    claimedAt: row.claimed_at == null ? null : Number(row.claimed_at),
    claimExpiresAt: row.claim_expires_at == null ? null : Number(row.claim_expires_at),
    heartbeatAt: row.heartbeat_at == null ? null : Number(row.heartbeat_at),
    initialContext: (row.initial_context as string) ?? null,
    handoffNote: (row.handoff_note as string) ?? null,
    artifactsJson: row.artifacts_json as string,
    kbLinksJson: row.kb_links_json as string,
    tagsJson: row.tags_json as string,
    turnCount: row.turn_count as number,
    lastActivityAt: (row.last_activity_at as string) ?? null,
    createdAt: row.created_at as string,
    updatedAt: row.updated_at as string,
  };
}

/** Normalize tags: lowercase, trim, drop empties, dedup preserving order. */
export function normalizeTags(tags: unknown): string[] {
  if (!Array.isArray(tags)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const t of tags) {
    if (typeof t !== "string") continue;
    const norm = t.trim().toLowerCase();
    if (!norm || seen.has(norm)) continue;
    seen.add(norm);
    out.push(norm);
  }
  return out;
}

/** Slugify a model id for auto-tagging (e.g. "Claude Opus 4.5" → "claude-opus-4-5"). Empty → ''. */
export function modelTag(model: string): string {
  if (typeof model !== "string" || !model.trim()) return "";
  return model
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .slice(0, 40)
    .replace(/^-+|-+$/g, "");
}

export interface TaskSession {
  id: number;
  taskId: number;
  sessionId: string;
  agentKind: string | null;
  cwd: string | null;
  model: string | null;
  pid: number | null;
  transcriptPath: string | null;
  createdAt: string;
  updatedAt: string;
}

function rowToSession(row: Record<string, unknown>): TaskSession {
  return {
    id: row.id as number,
    taskId: row.task_id as number,
    sessionId: row.session_id as string,
    agentKind: (row.agent_kind as string) ?? null,
    cwd: (row.cwd as string) ?? null,
    model: (row.model as string) ?? null,
    pid: (row.pid as number) ?? null,
    transcriptPath: (row.transcript_path as string) ?? null,
    createdAt: row.created_at as string,
    updatedAt: row.updated_at as string,
  };
}

export class TaskService {
  private keyCounter = 0;

  constructor(private db: Database.Database) {
    const max = this.db.prepare("SELECT MAX(CAST(SUBSTR(key, 4) AS INTEGER)) as m FROM tasks").get() as
      | { m: number | null }
      | undefined;
    this.keyCounter = max?.m ?? 0;
    this.ensureSessionsTable();
  }

  /** Defensive: task_sessions must exist even on DBs that predate the v3 migration. */
  private ensureSessionsTable(): void {
    try {
      this.db.exec(`
        CREATE TABLE IF NOT EXISTS task_sessions (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
          session_id TEXT NOT NULL,
          agent_kind TEXT,
          cwd TEXT,
          model TEXT,
          pid INTEGER,
          transcript_path TEXT,
          created_at TEXT NOT NULL DEFAULT (datetime('now')),
          updated_at TEXT NOT NULL DEFAULT (datetime('now'))
        );
        DROP INDEX IF EXISTS idx_task_sessions_session;
        CREATE UNIQUE INDEX IF NOT EXISTS idx_task_sessions_task_session ON task_sessions(task_id, session_id);
        CREATE INDEX IF NOT EXISTS idx_task_sessions_task ON task_sessions(task_id);
      `);
    } catch {
      // Hooks must never fail on missing table; callers handle nulls.
    }
  }

  nextKey(): string {
    this.keyCounter += 1;
    return `SW-${this.keyCounter}`;
  }

  create(input: {
    title?: string;
    status?: TaskStatus;
    originAgent: AgentKind;
    originSessionId?: string;
    originModel?: string;
    originCwd?: string;
    originPid?: number;
    /** Explicit parent task. A task is never inferred from the creating session. */
    parentKey?: string;
    required?: boolean;
    /** The planner/coordinator allowed to manage a parent task's lifecycle. */
    coordinatorSessionId?: string;
    repoPath?: string;
    branch?: string;
    initialContext?: string;
    tags?: string[];
  }): TaskRecord {
    const key = this.nextKey();
    const now = new Date().toISOString();
    const tags = normalizeTags(input.tags);
    const tag = modelTag(input.originModel ?? "");
    if (tag && !tags.includes(tag)) tags.push(tag);
    const tagsJson = JSON.stringify(tags);
    let parentTaskId: number | null = null;
    if (input.parentKey?.trim()) {
      const parent = this.getByKey(input.parentKey.trim());
      if (!parent) throw new Error(`Parent task not found: ${input.parentKey}`);
      parentTaskId = parent.id;
    }
    const result = this.db
      .prepare(
        `INSERT INTO tasks (key, title, status, origin_agent, origin_session_id, origin_model, origin_cwd, origin_pid, parent_task_id, required, coordinator_session_id, repo_path, branch, initial_context, tags_json, last_activity_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      )
      .run(
        key,
        input.title ?? "Untitled",
        input.status ?? "ready",
        input.originAgent,
        input.originSessionId ?? null,
        input.originModel ?? null,
        input.originCwd ?? null,
        input.originPid ?? null,
        parentTaskId,
        input.required === false ? 0 : 1,
        input.coordinatorSessionId ?? (parentTaskId == null ? input.originSessionId ?? null : null),
        input.repoPath ?? null,
        input.branch ?? null,
        input.initialContext ?? null,
        tagsJson,
        now,
        now,
      );
    const id = Number(result.lastInsertRowid);
    // Keep task_sessions in sync so one task can own many sessions.
    if (input.originSessionId?.trim()) {
      try {
        this.attachSession(id, {
          sessionId: input.originSessionId.trim(),
          agent: input.originAgent,
          cwd: input.originCwd,
          model: input.originModel,
          pid: input.originPid,
        });
      } catch {
        // Best-effort only.
      }
    }
    return this.getById(id)!;
  }

  // ── Multi-session support ──────────────────────────────────────────────
  // One task can be worked on by many sessions (e.g. agent B picks up agent A's
  // handoff). Pickup sessions join the picked-up tile instead of spawning a twin.

  listSessions(taskId: number): TaskSession[] {
    try {
      this.ensureSessionsTable();
      const rows = this.db
        .prepare(`SELECT * FROM task_sessions WHERE task_id = ? ORDER BY id ASC`)
        .all(taskId) as Record<string, unknown>[];
      return rows.map(rowToSession);
    } catch {
      return [];
    }
  }

  private getSessionRow(sessionId: string): TaskSession | null {
    try {
      this.ensureSessionsTable();
      const row = this.db.prepare(`SELECT * FROM task_sessions WHERE session_id = ? LIMIT 1`).get(sessionId) as
        | Record<string, unknown>
        | undefined;
      return row ? rowToSession(row) : null;
    } catch {
      return null;
    }
  }

  /** A session can participate in many tasks; this helper returns its latest membership only. */
  private getTaskIdForSession(sessionId: string): number | null {
    const sid = sessionId.trim();
    if (!sid) return null;
    try {
      const membership = this.db
        .prepare(`SELECT task_id AS id FROM task_sessions WHERE session_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`)
        .get(sid) as { id: number } | undefined;
      if (membership) return membership.id;
    } catch {
      // ignore
    }
    return null;
  }

  /**
   * Attach a session to a task (idempotent). Updates the task with the latest
   * session metadata and tags the task with the participating agent.
   * Never creates a task. Membership is explicit and non-exclusive.
   */
  attachSession(
    taskId: number,
    input: { sessionId: string; agent?: AgentKind | string; cwd?: string; model?: string; pid?: number; transcriptPath?: string },
  ): TaskSession | null {
    const sessionId = input.sessionId?.trim();
    if (!sessionId) return null;
    const task = this.getById(taskId);
    if (!task) return null;
    this.ensureSessionsTable();
    try {
      this.db
        .prepare(
          `INSERT INTO task_sessions (task_id, session_id, agent_kind, cwd, model, pid, transcript_path)
           VALUES (?, ?, ?, ?, ?, ?, ?)
           ON CONFLICT(task_id, session_id) DO UPDATE SET
             agent_kind = COALESCE(excluded.agent_kind, agent_kind),
             cwd = COALESCE(excluded.cwd, cwd),
             model = COALESCE(excluded.model, model),
             pid = COALESCE(excluded.pid, pid),
             transcript_path = COALESCE(excluded.transcript_path, transcript_path),
             updated_at = datetime('now')`,
        )
        .run(
          taskId,
          sessionId,
          input.agent?.trim() || null,
          input.cwd?.trim() || null,
          input.model?.trim() || null,
          input.pid ?? null,
          input.transcriptPath?.trim() || null,
        );
    } catch {
      return null;
    }
    // Tag multi-agent work: when a different agent kind joins a task, record both
    // the origin and the joining agent so the tile is filterable by either.
    // Single-agent tasks keep their legacy tags untouched.
    try {
      const joining = (input.agent ?? "").trim().toLowerCase();
      const origin = (task.originAgent ?? "").trim().toLowerCase();
      if (joining && joining !== "unknown" && origin && origin !== "unknown" && joining !== origin) {
        this.ensureAgentTag(taskId, origin);
        this.ensureAgentTag(taskId, joining);
      } else {
        // Still record the joining agent when the origin is unknown (legacy rows).
        const sessions = this.listSessions(taskId);
        const kinds = new Set(
          sessions.map((s) => (s.agentKind ?? "").trim().toLowerCase()).filter((a) => a && a !== "unknown"),
        );
        if (joining && joining !== "unknown") kinds.add(joining);
        if (origin && origin !== "unknown") kinds.add(origin);
        if (kinds.size > 1) {
          for (const k of kinds) this.ensureAgentTag(taskId, k);
        }
      }
    } catch {
      /* best-effort */
    }
    if (input.model) this.ensureModelTag(taskId, input.model);
    if (input.transcriptPath?.trim()) this.mergeTranscriptArtifact(taskId, input.transcriptPath.trim());
    // Refresh origin cwd/model/pid only when empty, then touch.
    try {
      if (input.cwd?.trim()) this.maybeRefreshOriginCwd(taskId, input.cwd);
    } catch {
      /* ignore */
    }
    try {
      this.refreshOriginMetadata(taskId, { model: input.model, pid: input.pid });
    } catch {
      /* ignore */
    }
    try {
      this.touch(taskId);
    } catch {
      /* ignore */
    }
    return this.getSessionRow(sessionId);
  }

  /** Ensure the lowercase agent kind is present in the task's tags. Never duplicates. */
  ensureAgentTag(taskId: number, agent?: string | null): TaskRecord | null {
    try {
      const norm = (agent ?? "").trim().toLowerCase();
      if (!norm || norm === "unknown") return this.getById(taskId);
      const task = this.getById(taskId);
      if (!task) return null;
      let existing: string[];
      try {
        existing = normalizeTags(JSON.parse(task.tagsJson));
      } catch {
        existing = [];
      }
      if (existing.includes(norm)) return task;
      return this.update(taskId, { addTags: [norm] });
    } catch {
      try {
        return this.getById(taskId);
      } catch {
        return null;
      }
    }
  }

  /** @deprecated Session-derived task creation is intentionally disabled. */
  upsertSessionTask(input: {
    sessionId: string;
    agent: AgentKind;
    cwd?: string;
    model?: string;
    pid?: number;
    transcriptPath?: string;
    title?: string;
    titleFromSession?: boolean;
    initialContext?: string;
  }): TaskRecord {
    void input;
    throw new Error("Session-derived task creation is disabled; create an explicit task instead");
  }

  private getClaimedTaskIdForSession(sessionId: string): number | null {
    try {
      const row = this.db
        .prepare(`SELECT id FROM tasks WHERE claimed_session_id = ? LIMIT 1`)
        .get(sessionId.trim()) as { id: number } | undefined;
      return row?.id ?? null;
    } catch {
      return null;
    }
  }

  private getAttachedTaskIdForSession(sessionId: string): number | null {
    try {
      this.ensureSessionsTable();
      const row = this.db
        .prepare(`SELECT task_id FROM task_sessions WHERE session_id = ? LIMIT 1`)
        .get(sessionId.trim()) as { task_id: number } | undefined;
      return row?.task_id ?? null;
    } catch {
      return null;
    }
  }

  /** Touch + revive + refresh metadata for an existing session tile (origin or picked-up). */
  private refreshSessionTask(
    taskId: number,
    input: { title?: string; titleFromSession?: boolean; cwd?: string; model?: string; pid?: number; initialContext?: string; transcriptPath?: string },
  ): TaskRecord {
    const current = this.getById(taskId);
    if (!current) throw new Error(`Task not found: ${taskId}`);
    this.touch(taskId);
    if (current.status === "done" || current.status === "archived") {
      this.update(taskId, { status: "in_progress" });
    }
    const fresh = this.getById(taskId)!;
    if (input.title && this.shouldReplaceTitle(fresh.title, input.title, input.titleFromSession ?? false)) {
      this.update(taskId, { title: input.title });
    }
    if (input.cwd?.trim()) {
      this.maybeRefreshOriginCwd(taskId, input.cwd);
    }
    if (input.model) {
      this.db
        .prepare("UPDATE tasks SET origin_model = COALESCE(origin_model, ?), updated_at = datetime('now') WHERE id = ?")
        .run(input.model, taskId);
      this.ensureModelTag(taskId, input.model);
    }
    if (input.pid != null) {
      this.db.prepare("UPDATE tasks SET origin_pid = ?, updated_at = datetime('now') WHERE id = ?").run(input.pid, taskId);
    }
    const after = this.getById(taskId)!;
    if (input.initialContext && !after.initialContext) {
      this.update(taskId, { initialContext: input.initialContext });
    }
    if (input.transcriptPath?.trim()) {
      this.mergeTranscriptArtifact(taskId, input.transcriptPath.trim());
    }
    return this.getById(taskId)!;
  }

  /** @deprecated Session-derived tile merging is disabled. */
  consolidateDuplicateSessions(): number {
    return 0;
  }

  maybeRefreshTitle(taskId: number, title: string, fromSession: boolean): TaskRecord | null {
    const existing = this.getById(taskId);
    if (!existing || !this.shouldReplaceTitle(existing.title, title, fromSession)) return existing;
    this.update(taskId, { title });
    return this.getById(taskId);
  }

  maybeRefreshOriginCwd(taskId: number, cwd: string): TaskRecord | null {
    const existing = this.getById(taskId);
    if (!existing || !cwd.trim() || existing.originCwd === cwd) return existing;
    this.db
      .prepare("UPDATE tasks SET origin_cwd = ?, repo_path = COALESCE(repo_path, ?), updated_at = datetime('now') WHERE id = ?")
      .run(cwd, cwd, taskId);
    return this.getById(taskId);
  }

  private shouldReplaceTitle(current: string, next: string, fromSession: boolean): boolean {
    return shouldReplaceSessionTitle(current, next, fromSession);
  }

  getById(id: number): TaskRecord | null {
    const row = this.db.prepare("SELECT * FROM tasks WHERE id = ?").get(id);
    return row ? rowToTask(row as Record<string, unknown>) : null;
  }

  getByKey(key: string): TaskRecord | null {
    const row = this.db.prepare("SELECT * FROM tasks WHERE key = ?").get(key);
    return row ? rowToTask(row as Record<string, unknown>) : null;
  }

  getBySession(sessionId: string): TaskRecord | null {
    if (!sessionId.trim()) return null;
    const taskId = this.getTaskIdForSession(sessionId.trim());
    return taskId == null ? null : this.getById(taskId);
  }

  list(filters: BoardFilters = {}): TaskRecord[] {
    const clauses: string[] = ["status != 'archived' OR ? = 1"];
    const params: unknown[] = [filters.status === "archived" ? 1 : 0];
    if (filters.status) {
      clauses.push("status = ?");
      params.push(filters.status);
    }
    if (filters.repo) {
      clauses.push("repo_path LIKE ?");
      params.push(`%${filters.repo}%`);
    }
    if (filters.agent) {
      clauses.push(
        `(origin_agent = ? OR claimed_agent = ? OR EXISTS (SELECT 1 FROM task_sessions ts WHERE ts.task_id = tasks.id AND ts.agent_kind = ?))`,
      );
      params.push(filters.agent, filters.agent, filters.agent);
    }
    if (filters.stale) {
      clauses.push("last_activity_at < datetime('now', '-30 minutes')");
    }
    const sql = `SELECT * FROM tasks WHERE ${clauses.join(" AND ")} ORDER BY updated_at DESC`;
    return (this.db.prepare(sql).all(...params) as Record<string, unknown>[]).map((row) => {
      const task = rowToTask(row);
      if ((row.status as string) === "handoff") {
        return { ...task, status: "ready" as TaskStatus };
      }
      return task;
    });
  }

  update(
    id: number,
    patch: Partial<{
      title: string;
      status: TaskStatus;
      initialContext: string;
      handoffNote: string;
      tags: string[];
      addTags: string[];
      removeTags: string[];
    }>,
  ): TaskRecord {
    const sets: string[] = ["updated_at = datetime('now')"];
    const params: unknown[] = [];
    if (patch.title !== undefined) {
      sets.push("title = ?");
      params.push(patch.title);
    }
    if (patch.status !== undefined) {
      sets.push("status = ?");
      params.push(patch.status);
    }
    if (patch.initialContext !== undefined) {
      sets.push("initial_context = ?");
      params.push(patch.initialContext);
    }
    if (patch.handoffNote !== undefined) {
      sets.push("handoff_note = ?");
      params.push(patch.handoffNote);
    }
    if (patch.tags !== undefined) {
      sets.push("tags_json = ?");
      params.push(JSON.stringify(normalizeTags(patch.tags)));
    } else if (patch.addTags !== undefined || patch.removeTags !== undefined) {
      const current = this.getById(id);
      const existing = current ? (JSON.parse(current.tagsJson) as string[]) : [];
      const merged = new Set<string>(normalizeTags(existing));
      for (const t of normalizeTags(patch.addTags)) merged.add(t);
      for (const t of normalizeTags(patch.removeTags)) merged.delete(t);
      sets.push("tags_json = ?");
      params.push(JSON.stringify([...merged]));
    }
    params.push(id);
    this.db.prepare(`UPDATE tasks SET ${sets.join(", ")} WHERE id = ?`).run(...params);
    return this.getById(id)!;
  }

  setTags(id: number, tags: string[]): TaskRecord {
    return this.update(id, { tags });
  }

  /** Fill origin model/pid only when currently NULL. Returns refreshed task. */
  refreshOriginMetadata(taskId: number, meta: { model?: string; pid?: number }): TaskRecord | null {
    if (meta.model?.trim()) {
      this.db
        .prepare("UPDATE tasks SET origin_model = ?, updated_at = datetime('now') WHERE id = ? AND origin_model IS NULL")
        .run(meta.model.trim(), taskId);
      this.ensureModelTag(taskId, meta.model);
    }
    if (meta.pid != null) {
      this.db
        .prepare("UPDATE tasks SET origin_pid = ?, updated_at = datetime('now') WHERE id = ? AND origin_pid IS NULL")
        .run(meta.pid, taskId);
    }
    return this.getById(taskId);
  }

  /**
   * Best-effort: ensure the model slug tag is present on a task.
   * Never duplicates, never removes explicit tags. Never throws (hooks must exit 0).
   */
  ensureModelTag(taskId: number, model?: string | null): TaskRecord | null {
    try {
      const tag = modelTag(model ?? "");
      if (!tag) return this.getById(taskId);
      const task = this.getById(taskId);
      if (!task) return null;
      let existing: string[];
      try {
        existing = normalizeTags(JSON.parse(task.tagsJson));
      } catch {
        existing = [];
      }
      if (existing.includes(tag)) return task;
      return this.update(taskId, { addTags: [tag] });
    } catch {
      try {
        return this.getById(taskId);
      } catch {
        return null;
      }
    }
  }

  /** Persist transcript path into artifacts_json.transcript (array, deduped). No schema migration. */
  mergeTranscriptArtifact(taskId: number, transcriptPath: string): void {
    const task = this.getById(taskId);
    if (!task || !transcriptPath.trim()) return;
    let artifacts: Record<string, unknown>;
    try {
      artifacts = JSON.parse(task.artifactsJson) as Record<string, unknown>;
    } catch {
      artifacts = {};
    }
    const current = Array.isArray(artifacts.transcript)
      ? (artifacts.transcript as unknown[]).filter((v): v is string => typeof v === "string")
      : [];
    if (!current.includes(transcriptPath)) current.push(transcriptPath);
    artifacts.transcript = current;
    this.db
      .prepare("UPDATE tasks SET artifacts_json = ?, updated_at = datetime('now') WHERE id = ?")
      .run(JSON.stringify(artifacts), taskId);
  }

  touch(id: number): void {
    this.db
      .prepare("UPDATE tasks SET last_activity_at = datetime('now'), updated_at = datetime('now') WHERE id = ?")
      .run(id);
  }

  incrementTurn(id: number): void {
    this.db
      .prepare(
        "UPDATE tasks SET turn_count = turn_count + 1, last_activity_at = datetime('now'), updated_at = datetime('now') WHERE id = ?",
      )
      .run(id);
  }

  appendEvent(taskId: number, eventType: string, payload: Record<string, unknown>): void {
    this.db
      .prepare("INSERT INTO task_events (task_id, event_type, payload_json) VALUES (?, ?, ?)")
      .run(taskId, eventType, JSON.stringify(payload));
    this.touch(taskId);
  }

  addArtifact(taskId: number, kind: string, value: string): void {
    const task = this.getById(taskId);
    if (!task) return;
    const artifacts = JSON.parse(task.artifactsJson) as Record<string, string[]>;
    if (!artifacts[kind]) artifacts[kind] = [];
    if (!artifacts[kind].includes(value)) artifacts[kind].push(value);
    this.db.prepare("UPDATE tasks SET artifacts_json = ?, updated_at = datetime('now') WHERE id = ?").run(JSON.stringify(artifacts), taskId);
  }

  claim(
    key: string,
    claimer: {
      agent: AgentKind;
      sessionId: string;
      by: string;
      model?: string;
      cwd?: string;
      pid?: number;
      transcriptPath?: string;
    },
    leaseSeconds: number,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    const now = Date.now();
    const expires = now + Math.max(1, leaseSeconds) * 1000;
    const token = randomUUID();
    const result = this.db
      .prepare(
        `UPDATE tasks SET
          claimed_by = ?, claimed_agent = ?, claimed_session_id = ?,
          claim_token = ?, claimed_at = ?, claim_expires_at = ?, heartbeat_at = ?,
          status = CASE WHEN status = 'ready' THEN 'in_progress' ELSE 'review' END,
          updated_at = datetime('now')
         WHERE key = ?
           AND status IN ('ready', 'review')
           AND (claimed_by IS NULL OR claim_expires_at < ?)`,
      )
      .run(claimer.by, claimer.agent, claimer.sessionId, token, now, expires, now, key, now);
    if (result.changes === 0) {
      return { ok: false, error: "Task is not available for claim" };
    }
    const claimed = this.getByKey(key)!;
    try {
      if (claimer.sessionId?.trim()) {
        this.attachSession(claimed.id, {
          sessionId: claimer.sessionId.trim(),
          agent: claimer.agent,
          cwd: claimer.cwd,
          model: claimer.model,
          pid: claimer.pid,
          transcriptPath: claimer.transcriptPath,
        });
      }
    } catch {
      // Best-effort only; claim itself already succeeded.
    }
    this.appendEvent(claimed.id, "claim", {
      agent: claimer.agent,
      sessionId: claimer.sessionId,
      by: claimer.by,
    });
    return { ok: true, task: this.getByKey(key)! };
  }

  /**
   * Register task membership. Joining is deliberately non-exclusive and never
   * changes a task's lease or lifecycle state.
   */
  join(
    key: string,
    joiner: {
      agent: AgentKind;
      sessionId: string;
      by: string;
      cwd?: string;
      model?: string;
      pid?: number;
      transcriptPath?: string;
    },
    _leaseSeconds = 300,
  ): { ok: boolean; task?: TaskRecord; joined?: boolean; error?: string } {
    const existing = this.getByKey(key);
    if (!existing) return { ok: false, error: `Task not found: ${key}` };

    this.appendEvent(existing.id, "join", {
      agent: joiner.agent,
      sessionId: joiner.sessionId,
      by: joiner.by,
      cwd: joiner.cwd,
      model: joiner.model,
      pid: joiner.pid,
    });
    try {
      if (joiner.sessionId?.trim()) {
        this.attachSession(existing.id, {
          sessionId: joiner.sessionId.trim(),
          agent: joiner.agent,
          cwd: joiner.cwd,
          model: joiner.model,
          pid: joiner.pid,
          transcriptPath: joiner.transcriptPath,
        });
      }
    } catch {
      // Best-effort only.
    }
    return { ok: true, task: this.getByKey(key)!, joined: true };
  }

  heartbeat(key: string, sessionId: string, claimToken: string, leaseSeconds: number): boolean {
    const now = Date.now();
    const expires = now + Math.max(1, leaseSeconds) * 1000;
    const result = this.db
      .prepare(
        `UPDATE tasks SET heartbeat_at = ?, claim_expires_at = ?, updated_at = datetime('now')
         WHERE key = ? AND claimed_session_id = ? AND claim_token = ? AND claim_expires_at >= ?`,
      )
      .run(now, expires, key, sessionId, claimToken, now);
    return result.changes > 0;
  }

  release(key: string, sessionId: string, claimToken: string): boolean {
    const result = this.db
      .prepare(
        `UPDATE tasks SET claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
          claim_token = NULL, claimed_at = NULL, claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
         WHERE key = ? AND claimed_session_id = ? AND claim_token = ? AND claim_expires_at >= ?`,
      )
      .run(key, sessionId, claimToken, Date.now());
    return result.changes > 0;
  }

  /** Implementation worker submits the work and relinquishes its lease. */
  submit(key: string, sessionId: string, claimToken: string): { ok: boolean; task?: TaskRecord; error?: string } {
    return this.protectedTransition(key, sessionId, claimToken, "in_progress", "review", "submit");
  }

  /** Reviewer approves an actively reviewed task. */
  approve(key: string, sessionId: string, claimToken: string): { ok: boolean; task?: TaskRecord; error?: string } {
    return this.protectedTransition(key, sessionId, claimToken, "review", "done", "approve");
  }

  /** Reviewer returns an actively reviewed task to the implementation queue. */
  requestChanges(key: string, sessionId: string, claimToken: string): { ok: boolean; task?: TaskRecord; error?: string } {
    return this.protectedTransition(key, sessionId, claimToken, "review", "ready", "request_changes");
  }

  private protectedTransition(
    key: string,
    sessionId: string,
    claimToken: string,
    from: TaskStatus,
    to: TaskStatus,
    eventType: string,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    const now = Date.now();
    const result = this.db.prepare(
      `UPDATE tasks SET status = ?, claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
       claim_token = NULL, claimed_at = NULL, claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
       WHERE key = ? AND status = ? AND claimed_session_id = ? AND claim_token = ? AND claim_expires_at >= ?`,
    ).run(to, key, from, sessionId, claimToken, now);
    if (result.changes === 0) return { ok: false, error: "Active lease required for this transition" };
    const task = this.getByKey(key)!;
    this.appendEvent(task.id, eventType, { sessionId });
    return { ok: true, task: this.getByKey(key)! };
  }

  stage(
    key: string,
    action: "move" | "claim" | "release" | "block" | "complete" | "fail" | "heartbeat" | "archive" | "submit" | "approve" | "request_changes",
    payload: Record<string, unknown>,
    leaseSeconds: number,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    switch (action) {
      case "move":
        return { ok: false, error: "Use an explicit lifecycle action; arbitrary status moves are not permitted" };
      case "claim":
        return this.claim(
          key,
          {
            agent: payload.agent as AgentKind,
            sessionId: payload.sessionId as string,
            by: payload.by as string,
            model: payload.model as string | undefined,
            cwd: payload.cwd as string | undefined,
            pid: payload.pid as number | undefined,
            transcriptPath: payload.transcriptPath as string | undefined,
          },
          leaseSeconds,
        );
      case "release":
        return this.release(key, payload.sessionId as string, payload.claimToken as string)
          ? { ok: true, task: this.getByKey(key)! }
          : { ok: false, error: "Active lease required for release" };
      case "block":
      case "complete":
      case "fail":
      case "archive":
        return { ok: false, error: "Use an authorized lifecycle action" };
      case "heartbeat":
        return this.heartbeat(key, payload.sessionId as string, payload.claimToken as string, leaseSeconds)
          ? { ok: true, task: this.getByKey(key)! }
          : { ok: false, error: "Active lease required for heartbeat" };
      case "submit":
        return this.submit(key, payload.sessionId as string, payload.claimToken as string);
      case "approve":
        return this.approve(key, payload.sessionId as string, payload.claimToken as string);
      case "request_changes":
        return this.requestChanges(key, payload.sessionId as string, payload.claimToken as string);
      default:
        return { ok: false, error: `Unknown action: ${action}` };
    }
  }

  writeHandoff(key: string, note: HandoffNote, markdown: string, sessionId?: string, claimToken?: string): TaskRecord {
    const task = this.getByKey(key);
    if (!task) throw new Error(`Task not found: ${key}`);
    if (!sessionId || !claimToken) throw new Error("Active lease required for handoff");
    const result = this.db.prepare(
      `UPDATE tasks SET handoff_note = ?, status = 'ready',
       claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL, claim_token = NULL,
       claimed_at = NULL, claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
       WHERE key = ? AND status = 'in_progress' AND claimed_session_id = ? AND claim_token = ? AND claim_expires_at >= ?`,
    ).run(markdown, key, sessionId, claimToken, Date.now());
    if (result.changes === 0) throw new Error("Active lease required for handoff");
    this.appendEvent(task.id, "handoff", note as unknown as Record<string, unknown>);
    return this.getByKey(key)!;
  }

  listHandoffs(): TaskRecord[] {
    return (this.db
      .prepare(
        `SELECT * FROM tasks WHERE handoff_note IS NOT NULL AND trim(handoff_note) != ''
         AND status NOT IN ('done', 'archived') AND claimed_by IS NULL
         ORDER BY updated_at DESC`,
      )
      .all() as Record<string, unknown>[]).map(rowToTask);
  }

  reaperExpiredClaims(): TaskRecord[] {
    const expired = this.db
      .prepare(
        `SELECT * FROM tasks WHERE claimed_by IS NOT NULL AND claim_expires_at < ? AND status = 'in_progress'`,
      )
      .all(Date.now()) as Record<string, unknown>[];
    const reclaimed: TaskRecord[] = [];
    for (const row of expired) {
      const task = rowToTask(row);
      const note = `Previous agent ${task.claimedBy} (${task.claimedAgent}) vanished — claim expired at ${task.claimExpiresAt}.`;
      this.db
        .prepare(
          `UPDATE tasks SET status = 'ready', handoff_note = COALESCE(handoff_note, '') || '\n\n' || ?,
            claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
            claim_token = NULL, claimed_at = NULL, claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
           WHERE id = ?`,
        )
        .run(note, task.id);
      this.appendEvent(task.id, "claim_expired", { previousClaimer: task.claimedBy });
      reclaimed.push(this.getById(task.id)!);
    }
    return reclaimed;
  }

  janitorArchive(config: { idleMinutes: number; minTurns: number }): number {
    const result = this.db
      .prepare(
        `UPDATE tasks SET status = 'archived', updated_at = datetime('now')
         WHERE status IN ('in_progress','review')
         AND turn_count < ?
         AND (artifacts_json = '{}' OR artifacts_json IS NULL)
         AND last_activity_at < datetime('now', '-' || ? || ' minutes')
         AND claimed_by IS NULL`,
      )
      .run(config.minTurns, config.idleMinutes);
    return result.changes;
  }

  hasActiveSessions(): boolean {
    const row = this.db
      .prepare(
        `SELECT COUNT(*) as c FROM tasks WHERE status = 'in_progress' AND heartbeat_at > ?`,
      )
      .get(Date.now() - 120_000) as { c: number };
    return row.c > 0;
  }

  /** A planner explicitly advances only the parent task it coordinates. */
  coordinateParent(
    key: string,
    coordinatorSessionId: string,
    status: Extract<TaskStatus, "ready" | "in_progress" | "review" | "done">,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    const result = this.db.prepare(
      `UPDATE tasks SET status = ?, updated_at = datetime('now')
       WHERE key = ? AND parent_task_id IS NULL AND coordinator_session_id = ? AND status NOT IN ('archived', 'done')`,
    ).run(status, key, coordinatorSessionId);
    if (result.changes === 0) return { ok: false, error: "Coordinator authorization required" };
    return { ok: true, task: this.getByKey(key)! };
  }

  listNeedingSummary(force = false): TaskRecord[] {
    return this.listNeedingBackfill(force);
  }

  listNeedingBackfill(force = false): TaskRecord[] {
    const sql = `SELECT * FROM tasks WHERE origin_session_id IS NOT NULL AND status != 'archived' ORDER BY updated_at DESC`;
    const tasks = (this.db.prepare(sql).all() as Record<string, unknown>[]).map(rowToTask);
    if (force) return tasks;
    return tasks.filter(
      (task) =>
        !task.handoffNote?.trim() ||
        !task.title?.trim() ||
        task.title === "Untitled" ||
        isFallbackSessionTitle(task.title),
    );
  }

  applySessionSummary(id: number, markdown: string, status?: TaskStatus): TaskRecord {
    const patch: Partial<{ handoffNote: string; status: TaskStatus }> = { handoffNote: markdown };
    if (status) patch.status = status;
    return this.update(id, patch);
  }

  getEvents(taskId: number, limit = 50): Array<{ id: number; eventType: string; payload: unknown; createdAt: string }> {
    return (
      this.db
        .prepare("SELECT * FROM task_events WHERE task_id = ? ORDER BY id DESC LIMIT ?")
        .all(taskId, limit) as Array<{ id: number; event_type: string; payload_json: string; created_at: string }>
    ).map((r) => ({
      id: r.id,
      eventType: r.event_type,
      payload: JSON.parse(r.payload_json),
      createdAt: r.created_at,
    }));
  }

  addSubtask(taskId: number, subject: string, description?: string): void {
    this.db.prepare("INSERT INTO subtasks (task_id, subject, description) VALUES (?, ?, ?)").run(taskId, subject, description ?? null);
  }

  completeSubtask(taskId: number, subject: string): void {
    this.db
      .prepare("UPDATE subtasks SET completed = 1 WHERE task_id = ? AND subject = ?")
      .run(taskId, subject);
  }

  getSubtasks(taskId: number): Array<{ id: number; subject: string; description: string | null; completed: boolean }> {
    return this.db
      .prepare("SELECT id, subject, description, completed FROM subtasks WHERE task_id = ?")
      .all(taskId) as Array<{ id: number; subject: string; description: string | null; completed: boolean }>;
  }

  /**
   * Cleanup excessive subagent tasks and consolidate them into their parent tasks.
   */
  cleanupSubagentTasks(): { archivedCount: number; details: string[] } {
    return { archivedCount: 0, details: ["Automatic task cleanup is disabled; archive reviewed task keys explicitly."] };
  }
}

export function renderHandoffMarkdown(note: HandoffNote, taskKey: string): string {
  const sections = [
    `# Handoff: ${taskKey}`,
    "",
    "## Goal",
    note.goal,
    "",
    "## Done",
    note.done,
    "",
    "<!-- SWARM:NEXT_STEPS:BEGIN -->",
    "## Next Steps",
    ...note.nextSteps.map((s, i) => `${i + 1}. ${s}`),
    "<!-- SWARM:NEXT_STEPS:END -->",
    "",
    "## Decisions",
    ...note.decisions.map((d) => `- ${d}`),
    "",
    "## Gotchas",
    ...note.gotchas.map((g) => `- ${g}`),
    "",
    "## Verification",
    "```bash",
    ...note.verification,
    "```",
    "",
    "## Files",
    ...note.files.map((f) => `- \`${f.path}\` — ${f.reason}`),
    "",
    "## KB References",
    ...note.kbRefs.map((r) => `- ${r}`),
    "",
    "## Open Questions",
    ...note.openQuestions.map((q) => `- ${q}`),
  ];
  return sections.join("\n");
}

export function renderPickupPrompt(task: TaskRecord): string {
  return [
    `# Pick up task ${task.key}: ${task.title}`,
    "",
    task.handoffNote ?? task.initialContext ?? "(no context)",
    "",
    "---",
    "Restate your plan before continuing. Call swarm_task_stage with action heartbeat periodically.",
  ].join("\n");
}

/** @deprecated Session-based task merging is intentionally disabled. */
export function consolidateTasksBySessionId(db: Database.Database): number {
  void db;
  return 0;
}

/**
 * Archive excessive subagent tasks and attach their sessions/events to parent tasks.
 */
export function cleanupSubagentTasks(
  db: Database.Database,
  taskService?: TaskService,
): { archivedCount: number; details: string[] } {
  void db;
  void taskService;
  return { archivedCount: 0, details: ["Automatic task cleanup is disabled; archive reviewed task keys explicitly."] };
}
