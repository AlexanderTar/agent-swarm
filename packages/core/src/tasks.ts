import type Database from "better-sqlite3";
import type { SessionService } from "./sessions.js";
import type {
  AgentKind,
  BoardFilters,
  HandoffNote,
  TaskRecord,
  TaskStatus,
  TaskWithSessions,
} from "./types.js";

/** Tool payloads are unbounded; 38k preToolUse rows were 85 MB of the live db. */
const EVENT_PAYLOAD_MAX = 2000;

export function normalizeTags(tags: readonly string[]): string[] {
  const seen = new Set<string>();
  for (const raw of tags) {
    const tag = raw.trim().toLowerCase().replace(/\s+/g, "-").replace(/[^a-z0-9._/-]/g, "");
    if (tag) seen.add(tag.slice(0, 40));
  }
  return [...seen].sort().slice(0, 20);
}

function parseTags(tagsJson: string): string[] {
  try {
    const parsed = JSON.parse(tagsJson) as unknown;
    return Array.isArray(parsed) ? parsed.filter((t): t is string => typeof t === "string") : [];
  } catch {
    return [];
  }
}

function rowToTask(row: Record<string, unknown>): TaskRecord {
  return {
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
    claimedBy: (row.claimed_by as string) ?? null,
    claimedAgent: (row.claimed_agent as AgentKind) ?? null,
    claimedSessionId: (row.claimed_session_id as string) ?? null,
    claimedAt: (row.claimed_at as string) ?? null,
    claimExpiresAt: (row.claim_expires_at as string) ?? null,
    heartbeatAt: (row.heartbeat_at as string) ?? null,
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

export class TaskService {
  private keyCounter = 0;

  constructor(
    private db: Database.Database,
    private sessions: SessionService,
  ) {
    const max = this.db.prepare("SELECT MAX(CAST(SUBSTR(key, 4) AS INTEGER)) as m FROM tasks").get() as
      | { m: number | null }
      | undefined;
    this.keyCounter = max?.m ?? 0;
  }

  nextKey(): string {
    this.keyCounter += 1;
    return `SW-${this.keyCounter}`;
  }

  create(input: {
    title: string;
    summary?: string;
    status?: TaskStatus;
    priority?: string;
    tags?: string[];
    originAgent: AgentKind;
    originSessionId?: string;
    originModel?: string;
    originCwd?: string;
    originPid?: number;
    repoPath?: string;
    branch?: string;
    initialContext?: string;
  }): TaskRecord {
    const key = this.nextKey();
    const now = new Date().toISOString();
    const result = this.db
      .prepare(
        `INSERT INTO tasks (key, title, status, priority, tags_json, handoff_note, origin_agent, origin_session_id,
           origin_model, origin_cwd, origin_pid, repo_path, branch, initial_context, last_activity_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      )
      .run(
        key,
        input.title,
        input.status ?? "in_progress",
        input.priority ?? "medium",
        JSON.stringify(normalizeTags(input.tags ?? [])),
        input.summary ?? null,
        input.originAgent,
        input.originSessionId ?? null,
        input.originModel ?? null,
        input.originCwd ?? null,
        input.originPid ?? null,
        input.repoPath ?? null,
        input.branch ?? null,
        input.initialContext ?? null,
        now,
        now,
      );
    return this.getById(Number(result.lastInsertRowid))!;
  }

  getById(id: number): TaskRecord | null {
    const row = this.db.prepare("SELECT * FROM tasks WHERE id = ?").get(id);
    return row ? rowToTask(row as Record<string, unknown>) : null;
  }

  getByKey(key: string): TaskRecord | null {
    const row = this.db.prepare("SELECT * FROM tasks WHERE key = ?").get(key);
    return row ? rowToTask(row as Record<string, unknown>) : null;
  }

  list(filters: BoardFilters = {}): TaskWithSessions[] {
    const clauses: string[] = ["(status != 'archived' OR @includeArchived = 1)"];
    const params: Record<string, unknown> = { includeArchived: filters.status === "archived" ? 1 : 0 };
    if (filters.status) {
      clauses.push("status = @status");
      params.status = filters.status;
    }
    if (filters.repo) {
      clauses.push("repo_path LIKE @repo");
      params.repo = `%${filters.repo}%`;
    }
    if (filters.agent) {
      clauses.push(
        `(origin_agent = @agent OR claimed_agent = @agent
          OR EXISTS (SELECT 1 FROM sessions s WHERE s.task_id = tasks.id AND s.agent_kind = @agent))`,
      );
      params.agent = filters.agent;
    }
    if (filters.tag) {
      clauses.push("EXISTS (SELECT 1 FROM json_each(tasks.tags_json) WHERE json_each.value = @tag)");
      params.tag = filters.tag;
    }
    if (filters.stale) {
      clauses.push("last_activity_at < datetime('now', '-30 minutes')");
    }
    const sql = `SELECT * FROM tasks WHERE ${clauses.join(" AND ")} ORDER BY updated_at DESC`;
    const tasks = (this.db.prepare(sql).all(params) as Record<string, unknown>[]).map(rowToTask);
    const labels = this.sessions.labelsForTasks(tasks.map((t) => t.id));
    return tasks.map((task) => ({
      ...task,
      tags: parseTags(task.tagsJson),
      sessions: labels.get(task.id) ?? [],
    }));
  }

  listAllTags(): string[] {
    return (
      this.db
        .prepare(
          `SELECT DISTINCT json_each.value AS tag FROM tasks, json_each(tasks.tags_json)
           WHERE tasks.status != 'archived' ORDER BY tag`,
        )
        .all() as Array<{ tag: string }>
    ).map((r) => r.tag);
  }

  getTags(id: number): string[] {
    const row = this.db.prepare("SELECT tags_json FROM tasks WHERE id = ?").get(id) as
      | { tags_json: string }
      | undefined;
    return row ? parseTags(row.tags_json) : [];
  }

  setTags(id: number, tags: string[]): TaskRecord {
    return this.update(id, { tags });
  }

  addTags(id: number, tags: string[]): TaskRecord {
    return this.setTags(id, [...this.getTags(id), ...tags]);
  }

  removeTags(id: number, tags: string[]): TaskRecord {
    const drop = new Set(normalizeTags(tags));
    return this.setTags(
      id,
      this.getTags(id).filter((t) => !drop.has(t)),
    );
  }

  update(
    id: number,
    patch: Partial<{
      title: string;
      status: TaskStatus;
      priority: string;
      summary: string;
      initialContext: string;
      handoffNote: string;
      tags: string[];
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
    if (patch.priority !== undefined) {
      sets.push("priority = ?");
      params.push(patch.priority);
    }
    if (patch.initialContext !== undefined) {
      sets.push("initial_context = ?");
      params.push(patch.initialContext);
    }
    const handoffNote = patch.handoffNote ?? patch.summary;
    if (handoffNote !== undefined) {
      sets.push("handoff_note = ?");
      params.push(handoffNote);
    }
    if (patch.tags !== undefined) {
      sets.push("tags_json = ?");
      params.push(JSON.stringify(normalizeTags(patch.tags)));
    }
    params.push(id);
    this.db.prepare(`UPDATE tasks SET ${sets.join(", ")} WHERE id = ?`).run(...params);
    return this.getById(id)!;
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
    // ponytail: a Write tool_input carries the whole file, so cap the row rather than the callers.
    const json = JSON.stringify(payload);
    const stored =
      json.length > EVENT_PAYLOAD_MAX
        ? JSON.stringify({ truncated: true, preview: json.slice(0, EVENT_PAYLOAD_MAX) })
        : json;
    this.db
      .prepare("INSERT INTO task_events (task_id, event_type, payload_json) VALUES (?, ?, ?)")
      .run(taskId, eventType, stored);
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
    claimer: { agent: AgentKind; sessionId: string; by: string },
    leaseSeconds: number,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    const expires = new Date(Date.now() + leaseSeconds * 1000).toISOString();
    const now = new Date().toISOString();
    const result = this.db
      .prepare(
        `UPDATE tasks SET
          claimed_by = ?, claimed_agent = ?, claimed_session_id = ?,
          claimed_at = ?, claim_expires_at = ?, heartbeat_at = ?,
          status = 'in_progress', updated_at = datetime('now')
         WHERE key = ? AND (claimed_by IS NULL OR claim_expires_at < datetime('now'))`,
      )
      .run(claimer.by, claimer.agent, claimer.sessionId, now, expires, now, key);
    if (result.changes === 0) {
      return { ok: false, error: "Task already claimed or not found" };
    }
    return { ok: true, task: this.getByKey(key)! };
  }

  heartbeat(key: string, sessionId: string, leaseSeconds: number): boolean {
    const expires = new Date(Date.now() + leaseSeconds * 1000).toISOString();
    const now = new Date().toISOString();
    const result = this.db
      .prepare(
        `UPDATE tasks SET heartbeat_at = ?, claim_expires_at = ?, updated_at = datetime('now')
         WHERE key = ? AND claimed_session_id = ?`,
      )
      .run(now, expires, key, sessionId);
    return result.changes > 0;
  }

  release(key: string, sessionId: string): boolean {
    const result = this.db
      .prepare(
        `UPDATE tasks SET claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
          claimed_at = NULL, claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
         WHERE key = ? AND claimed_session_id = ?`,
      )
      .run(key, sessionId);
    return result.changes > 0;
  }

  stage(
    key: string,
    action: "move" | "claim" | "release" | "block" | "complete" | "fail" | "heartbeat" | "archive",
    payload: Record<string, unknown>,
    leaseSeconds: number,
  ): { ok: boolean; task?: TaskRecord; error?: string } {
    switch (action) {
      case "move":
        return { ok: true, task: this.update(this.getByKey(key)!.id, { status: payload.status as TaskStatus }) };
      case "claim":
        return this.claim(
          key,
          {
            agent: payload.agent as AgentKind,
            sessionId: payload.sessionId as string,
            by: payload.by as string,
          },
          leaseSeconds,
        );
      case "release":
        this.release(key, payload.sessionId as string);
        return { ok: true, task: this.getByKey(key)! };
      case "block":
        return { ok: true, task: this.update(this.getByKey(key)!.id, { status: "blocked" }) };
      case "complete":
        return { ok: true, task: this.update(this.getByKey(key)!.id, { status: "done" }) };
      case "fail":
        return { ok: true, task: this.update(this.getByKey(key)!.id, { status: "blocked" }) };
      case "heartbeat":
        this.heartbeat(key, payload.sessionId as string, leaseSeconds);
        return { ok: true, task: this.getByKey(key)! };
      case "archive":
        return { ok: true, task: this.update(this.getByKey(key)!.id, { status: "archived" }) };
      default:
        return { ok: false, error: `Unknown action: ${action}` };
    }
  }

  writeHandoff(key: string, note: HandoffNote, markdown: string): TaskRecord {
    const task = this.getByKey(key);
    if (!task) throw new Error(`Task not found: ${key}`);
    this.db
      .prepare(
        `UPDATE tasks SET handoff_note = ?, status = 'ready',
          claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
          claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
         WHERE key = ?`,
      )
      .run(markdown, key);
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
        `SELECT * FROM tasks WHERE claimed_by IS NOT NULL AND claim_expires_at < datetime('now') AND status = 'in_progress'`,
      )
      .all() as Record<string, unknown>[];
    const reclaimed: TaskRecord[] = [];
    for (const row of expired) {
      const task = rowToTask(row);
      const note = `Previous agent ${task.claimedBy} (${task.claimedAgent}) vanished — claim expired at ${task.claimExpiresAt}.`;
      this.db
        .prepare(
          `UPDATE tasks SET status = 'ready', handoff_note = COALESCE(handoff_note, '') || '\n\n' || ?,
            claimed_by = NULL, claimed_agent = NULL, claimed_session_id = NULL,
            claim_expires_at = NULL, heartbeat_at = NULL, updated_at = datetime('now')
           WHERE id = ?`,
        )
        .run(note, task.id);
      this.appendEvent(task.id, "claim_expired", { previousClaimer: task.claimedBy });
      reclaimed.push(this.getById(task.id)!);
    }
    return reclaimed;
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
