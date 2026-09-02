# Board Coordination Pivot — Agents Name Their Own Work

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` to
> implement this task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Turn the Agent Swarm board back into a coordination surface — agents create and name
board items themselves through MCP, hooks only attach activity to items that already exist, and
Ollama is used only for embedding into the knowledge base.

**Architecture:** Hooks stop upserting tasks. They maintain a `sessions` row and hand the agent a
short briefing (its swarm session id + how to create/join a task) through the hook's
`additionalContext` channel. Agents call `swarm_task_create` / `swarm_task_join` /
`swarm_task_update` with a title, summary and tags they write themselves. A task can hold many
sessions, so many agents/models show on one tile. Every new task, every `.md` artifact it produced,
and every bound agent transcript is embedded into `~/.swarm/kb` for semantic search.

**Tech Stack:** TypeScript, Fastify, better-sqlite3 + sqlite-vec, MCP SDK, React 19 + Tailwind,
Ollama (`nomic-embed-text` for embeddings only), pnpm workspaces, vitest.

**Spec:** this document.

## Global Constraints

- Node 20+, ESM only, `.js` extensions on relative TS imports.
- SQLite schema version bumps to **3**. Migration must be idempotent and must not lose data.
- Ollama chat model (`qwen3:4b`) is used **only** by `POST /api/chat` (board Console). No title or
  summary generation anywhere else. `nomic-embed-text` stays required for embeddings.
- Agent kinds: `claude | cursor | codex | antigravity | opencode | unknown`.
- Board URL stays `http://127.0.0.1:7777`.
- Hooks must never fail the host agent: `post-hook.mjs` always exits 0.
- Biome formatting (`pnpm lint`), `pnpm build` and `pnpm test` must pass.

---

## 1. Context

### The problem

The board was built as a task-coordination surface. Instead it became session telemetry:

- Every hook event (`SessionStart`, `PreToolUse`, `SubagentStart`, `MessageDisplay`, …) calls
  `TaskService.upsertSessionTask`, so **every session and every subagent of every supported agent
  gets a tile**. The live board holds 300+ tiles; `~/.swarm/swarm.db` is 500 MB.
- Titles come from scraping transcripts and from `qwen3:4b` (`sessionTitles.ts`, `titleJob.ts`,
  `ollama.summarizeTaskTitle`) with a retry ladder and a `isFallbackSessionTitle` heuristic that
  decides a title is "bad" and re-generates it. Titles are obscure and inconsistent.
- Summaries come from `sessionSummary.ts` feeding a scraped transcript to `qwen3:4b`, with a
  `fallbackSummary` when that fails.
- A whole layer of workarounds exists because one session id had to own exactly one tile:
  `idx_tasks_session_unique`, `consolidateTasksBySessionId`, `janitorArchive`, the "revive
  done/archived instead of cloning" branches in `upsertSessionTask`/`getBySession`.
- `MemoryJobs.composeInbox` / `compactNotes` are stubs returning 0.

### The change

The agent — which actually knows what it is doing — names and summarises its own work. The daemon
does bookkeeping and embedding.

### Affected trees

- `packages/core` — schema, tasks, sessions, hooks, kb, indexer (large deletions).
- `packages/daemon` — hook routes, MCP server, jobs.
- `packages/mcp-stdio` — pass session/cwd/agent as HTTP headers.
- `packages/web` — multi-agent badges, tags, tag filter.
- `packages/cli` — opencode agent, plugin sync, doctor, removed backfill commands.
- `plugin/` — rewritten instructions/skills for all agents + new opencode plugin.

### Collision warnings

- `~/.swarm/app/releases/dev` is a **separate clone** of this repo (branch
  `cursor/fix-install-bootstrap`) that `~/.swarm/app/current` points at. Redeploy happens there,
  not in the working checkout.
- The daemon runs under launchd as `dev.swarm.daemon` and holds `~/.swarm/swarm.db` open. Stop it
  before running the migration by hand.
- `~/.claude/skills/swarm`, `~/.cursor/plugins/local/swarm`, `~/.gemini/config/plugins/swarm` are
  symlinks into `<swarm home>/app/current/plugin`. Editing the repo plugin changes live behaviour
  after a redeploy.

### Caveats

- The one heuristic that survives is **session binding by working directory** when an MCP call
  carries no explicit session id (Antigravity and OpenCode have no context-injection channel). Two
  agents in the same cwd can bind to the wrong session row. Marked with a `ponytail:` comment.
  Worst case is a task attributed to the wrong sibling session — no data loss.

---

## 2. Locked decisions

1. **Hooks never create tasks.** Only `swarm_task_create` (MCP) and `POST /api/tasks` do.
2. **Agents write titles and summaries.** No LLM title/summary generation anywhere.
3. **One task, many sessions.** `sessions.task_id` is the join. `idx_tasks_session_unique` is
   dropped. `tasks.origin_session_id` stays as "who created it".
4. **Migration archives hook-spawned tiles** (`initial_context LIKE '## Session%'`), deletes their
   `task_events`, and `VACUUM`s. A `VACUUM INTO ~/.swarm/backups/pre-v3.db` snapshot is taken first.
   *(user decision, 2026-09-02)*
5. **`join` and `claim` both exist.** `swarm_task_join` is additive and unlimited;
   `swarm_pickup`/`claim` stays exclusive with a lease for handoff pickup. *(user decision)*
6. **`chatModel` stays, for `POST /api/chat` only.** Daemon preflight still requires it.
   *(user decision)*
7. **Tags live in `tasks.tags_json`** (already in the schema) and are filtered with SQLite JSON1
   `json_each`. No `task_tags` table.
8. **Transcript-path resolution is kept**, title/first-prompt scraping is deleted. Cursor and
   Antigravity do not send `transcript_path` in hook payloads and we need the path to index
   transcripts.
9. **High-frequency hooks are dropped**: `MessageDisplay`, `afterAgentThought`,
   `afterAgentResponse` deltas. They produced most of the 500 MB.

### Assumptions taken (easy to change at approval)

- **A1.** OpenCode plugin event payload shapes are read defensively (several candidate fields for
  the session id) rather than pinned to one documented shape, because the plugin event schema is
  not versioned in the docs.
- **A2.** The session briefing is delivered as `additionalContext` on `SessionStart` for
  claude/codex/cursor. Antigravity gets it through `rules/swarm.md` static text (its hook output
  schema has no context channel). OpenCode gets it through `AGENTS.md`.
- **A3.** `swarm_task_leave` is not implemented; ending a session unbinds it.

---

## 3. DB models

Schema version `2 → 3`, applied in `SwarmDatabase.migrate`.

### 3.1 Migration steps (in order, single transaction except VACUUM)

```sql
-- 0. snapshot (outside the transaction, only when version < 3)
VACUUM INTO '<paths.backups>/pre-v3-<timestamp>.db';

-- 1. one task may now hold many sessions
DROP INDEX IF EXISTS idx_tasks_session_unique;

-- 2. sessions become the session<->task join
ALTER TABLE sessions ADD COLUMN parent_session_id TEXT;
ALTER TABLE sessions ADD COLUMN transcript_path TEXT;
ALTER TABLE sessions ADD COLUMN last_seen_at TEXT;
CREATE INDEX IF NOT EXISTS idx_sessions_task ON sessions(task_id);
CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions(cwd, last_seen_at);

-- 3. tag filtering
CREATE INDEX IF NOT EXISTS idx_tasks_updated ON tasks(updated_at);

-- 4. archive hook-spawned tiles and purge their events (user decision 4)
DELETE FROM task_events
 WHERE task_id IN (SELECT id FROM tasks WHERE initial_context LIKE '## Session%');
UPDATE tasks
   SET status = 'archived', updated_at = datetime('now')
 WHERE status != 'archived' AND initial_context LIKE '## Session%';

-- 5. legacy status value written by older builds
UPDATE tasks SET status = 'ready' WHERE status = 'handoff';

UPDATE schema_meta SET version = 3;
```

```sql
-- 6. after the transaction commits
VACUUM;
```

Fresh installs (`schema_meta` absent) create the same shape directly: `sessions` gains
`parent_session_id TEXT`, `transcript_path TEXT`, `last_seen_at TEXT`, and the three indexes above;
`idx_tasks_session_unique` is **not** created.

### 3.2 Final `sessions` shape

| column | type | note |
|---|---|---|
| `id` | TEXT PK | host session id, or subagent/agent id |
| `agent_kind` | TEXT NOT NULL | `claude\|cursor\|codex\|antigravity\|opencode\|unknown` |
| `cwd` | TEXT | used for MCP binding fallback |
| `model` | TEXT | shown as a chip on the tile |
| `pid` | INTEGER | |
| `task_id` | INTEGER FK tasks(id) | NULL until the agent creates or joins |
| `parent_session_id` | TEXT | set for subagents |
| `transcript_path` | TEXT | resolved once, used for KB ingest |
| `started_at` | TEXT NOT NULL | |
| `last_seen_at` | TEXT | |
| `ended_at` | TEXT | |

### 3.3 Unchanged tables

`tasks`, `task_events`, `subtasks`, `kb_docs`, `kb_chunks`, `kb_fts`, `vec_chunks` keep their
columns. `tasks.tags_json` (already present, always `'[]'`) starts being written.

---

## 4. Model / API types

### 4.1 `packages/core/src/types.ts`

```ts
export type AgentKind = "claude" | "cursor" | "codex" | "antigravity" | "opencode" | "unknown";

export interface SessionRecord {
  id: string;
  agentKind: AgentKind;
  cwd: string | null;
  model: string | null;
  pid: number | null;
  taskId: number | null;
  parentSessionId: string | null;
  transcriptPath: string | null;
  startedAt: string;
  lastSeenAt: string | null;
  endedAt: string | null;
}

/** Agent/model labels rendered on a board tile. */
export interface TaskSessionLabel {
  sessionId: string;
  agent: AgentKind;
  model: string | null;
  active: boolean; // last_seen_at within 2 minutes and ended_at IS NULL
}

export interface TaskWithSessions extends TaskRecord {
  tags: string[];
  sessions: TaskSessionLabel[];
}

export interface BoardFilters {
  status?: TaskStatus;
  repo?: string;
  agent?: AgentKind;
  tag?: string;
  stale?: boolean;
}
```

`TaskRecord` keeps every existing field.

### 4.2 `packages/core/src/sessions.ts` (new)

```ts
export class SessionService {
  constructor(db: Database.Database);

  upsert(input: {
    id: string;
    agent: AgentKind;
    cwd?: string;
    model?: string;
    pid?: number;
    parentSessionId?: string;
    transcriptPath?: string;
  }): SessionRecord;

  get(id: string): SessionRecord | null;
  touch(id: string): void;
  end(id: string): void;
  /** Attach a session to a task (additive join). Returns false when the session is unknown. */
  bind(sessionId: string, taskId: number): boolean;
  /** Explicit id wins; otherwise the most recently seen live session in `cwd`. */
  resolve(opts: { sessionId?: string; cwd?: string }): SessionRecord | null;
  listByTask(taskId: number): SessionRecord[];
  labelsForTasks(taskIds: number[]): Map<number, TaskSessionLabel[]>;
}
```

### 4.3 `packages/core/src/tasks.ts` — changed signatures

```ts
create(input: {
  title: string;
  summary?: string;          // -> handoff_note
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
}): TaskRecord;

update(id: number, patch: Partial<{
  title: string;
  status: TaskStatus;
  priority: string;
  summary: string;           // -> handoff_note
  initialContext: string;
  handoffNote: string;
  tags: string[];
}>): TaskRecord;

setTags(id: number, tags: string[]): TaskRecord;
addTags(id: number, tags: string[]): TaskRecord;
removeTags(id: number, tags: string[]): TaskRecord;
getTags(id: number): string[];

list(filters?: BoardFilters): TaskWithSessions[];
listAllTags(): string[];
```

**Deleted from `TaskService`:** `upsertSessionTask`, `consolidateDuplicateSessions`,
`maybeRefreshTitle`, `maybeRefreshOriginCwd`, `shouldReplaceTitle`, `janitorArchive`,
`listNeedingSummary`, `listNeedingBackfill`, `applySessionSummary`, `hasActiveSessions`.
**Deleted from the module:** `consolidateTasksBySessionId`.

Tag normalisation, used by every tag entry point:

```ts
export function normalizeTags(tags: readonly string[]): string[] {
  const seen = new Set<string>();
  for (const raw of tags) {
    const tag = raw.trim().toLowerCase().replace(/\s+/g, "-").replace(/[^a-z0-9._/-]/g, "");
    if (tag) seen.add(tag.slice(0, 40));
  }
  return [...seen].sort().slice(0, 20);
}
```

Tag filter SQL:

```sql
EXISTS (SELECT 1 FROM json_each(tasks.tags_json) WHERE json_each.value = ?)
```

Agent filter SQL (origin, claimer, or any bound session):

```sql
(origin_agent = ?1 OR claimed_agent = ?1
 OR EXISTS (SELECT 1 FROM sessions s WHERE s.task_id = tasks.id AND s.agent_kind = ?1))
```

### 4.4 `packages/core/src/hooks.ts` — changed surface

```ts
export type HookPlatform = "claude" | "cursor" | "codex" | "antigravity" | "opencode";

/** Coarse event class, so route handling is one switch instead of thirty cases. */
export type HookEventKind =
  | "session_start" | "subagent_start" | "session_end"
  | "turn_end" | "tool" | "prompt" | "notification" | "compact" | "other";

export function classifyHookEvent(event: string): HookEventKind;

/** The text handed back to the agent on session start. */
export function sessionBriefing(params: {
  sessionId: string;
  boardUrl: string;
  task?: { key: string; title: string; status: string } | null;
}): string;
```

`normalizeHookInput` keeps its shape minus `messageDelta`, `messageFinal`, `agentText`,
`thoughtDurationMs`. `detectPlatform` gains an `opencode` branch (`"opencode" in raw` or the
`platformHint`). **Deleted:** `sessionTaskTitle`, `buildSessionContext`, `resolveBoardSessionId`,
`resolveHookBoardSessionId`, `resolveSubagentBoardId` (replaced by one
`hookSessionKey(input): string` = `input.agentId?.trim() || input.sessionId`).

`formatHookOutput` gains an `opencode` branch returning `{ additionalContext }` verbatim (our own
plugin consumes it) and the antigravity branch gains `result.additionalContext` alongside
`decision`, harmless to hosts that ignore it.

### 4.5 `packages/core/src/indexer.ts` (new, replaces `memory.ts`)

```ts
export class KbIndexer {
  constructor(kb: KbStore, tasks: TaskService, sessions: SessionService);

  /** kb/tasks/<KEY>.md from title + summary + tags. Called on create and update. */
  indexTask(taskId: number): Promise<string | null>;

  /** Index every *.md path recorded in artifacts_json.files that still exists. */
  indexArtifacts(taskId: number): Promise<string[]>;

  /** kb/transcripts/<KEY>-<sessionId>.md rendered from the agent transcript. */
  indexTranscript(taskId: number, sessionId: string): Promise<string | null>;

  /** indexTask + indexArtifacts + indexTranscript, never throws. */
  ingestSession(taskId: number, sessionId: string): Promise<string[]>;
}
```

`KbStore` gains:

```ts
search(query: string, limit?: number, opts?: { subdir?: string }): Promise<KbSearchResult[]>;
```

`opts.subdir` filters on `kb_docs.path LIKE '<kb>/<subdir>/%'`.

**Deleted:** `packages/core/src/memory.ts` (`MemoryJobs`), including the `composeInbox` /
`compactNotes` stubs and the hourly job that calls them.

### 4.6 MCP tool surface (`packages/daemon/src/mcp.ts`)

Per-connection metadata captured from headers at `initialize`:

```ts
export interface McpClientMeta {
  cwd?: string;        // x-swarm-cwd
  agent?: AgentKind;   // x-swarm-agent
  sessionId?: string;  // x-swarm-session-id
}
```

| tool | args | effect |
|---|---|---|
| `swarm_board` | `{status?, repo?, agent?, tag?}` | list `TaskWithSessions[]` |
| `swarm_task_get` | `{key}` | task + tags + sessions + events + subtasks |
| `swarm_task_create` | `{title, summary?, tags?, status?, repoPath?, branch?, sessionId?, agent?, model?}` | create, bind caller session, index into KB |
| `swarm_task_update` | `{key, title?, summary?, status?, tags?, addTags?, removeTags?}` | patch, re-index into KB |
| `swarm_task_join` | `{key, sessionId?, agent?, model?}` | additive bind |
| `swarm_task_stage` | unchanged | move/claim/release/block/complete/fail/heartbeat/archive |
| `swarm_handoff` | unchanged | structured handoff note |
| `swarm_pickup` | unchanged | list/claim handoffs |
| `swarm_kb_search` | `{query, limit?, subdir?}` | hybrid search |
| `swarm_kb_get` | `{slug}` | doc |
| `swarm_kb_write` | `{subdir?, filename, title?, tags?, body}` | write + index |
| `swarm_memory_write` | `{title, body, tags?}` | `kb/memory/<slug>.md`, `type: memory` |
| `swarm_memory_search` | `{query, limit?}` | `swarm_kb_search` scoped to `memory` |

Every tool that takes `sessionId?`/`agent?` falls back to `McpClientMeta`, then to
`SessionService.resolve({ cwd })`.

### 4.7 HTTP surface (`packages/daemon/src/routes.ts`)

| route | change |
|---|---|
| `POST /hooks/:platform/:event` | rewritten around `classifyHookEvent`; never creates tasks |
| `POST /hooks/session/register` | registers a `sessions` row only; returns `{ok, sessionId}` |
| `POST /hooks/session/end` | `sessions.end` + `ingestSession` when bound |
| `GET /api/board` | accepts `tag`; returns `TaskWithSessions[]` |
| `GET /api/tags` | **new** — `string[]` of every tag in use |
| `POST /api/tasks` | accepts `summary`, `tags` |
| `PATCH /api/tasks/:key` | accepts `title`, `status`, `summary`, `tags` |
| `POST /api/summaries/backfill` | **deleted** |

Hook response body is always `formatHookOutput(platform, output, event)`.

---

## 5. Screens

### 5.1 Board tile (`packages/web/src/App.tsx`, `TaskCard`)

```
┌────────────────────────────────────────────┐
│ ⠿  SW-42                          ● live  🗑│
│    Rework board title generation           │  ← agent-written title, 2 lines max
│                                             │
│    [claude] [codex]        opus-5  gpt-5.1 │  ← one badge per distinct agent,
│    #swarm #mcp #board                      │     model chips after; tag chips
│    4 files                                  │
└────────────────────────────────────────────┘
```

- Agent badges: one per **distinct** `sessions[].agent`, colour from `AGENT_COLORS`
  (+ `opencode: bg-amber-500/20 text-amber-300 border-amber-500/40`).
  When `sessions` is empty, fall back to a single `originAgent` badge.
- Model chips: distinct non-null `sessions[].model`, capped at 3 then `+N`.
- Tag chips: `#tag`, clickable → sets the header tag filter. Capped at 4 then `+N`.
- `● live` shows when any session is `active`.
- Deliberately **not** on the tile: session ids, cwd, turn counts, event counts.

### 5.2 Header (`App`)

```
┌──────────────────────────────────────────────────────────────────────┐
│ Agent Swarm Board          [ #tag ▾ ]  17 tasks   [💬 Console]       │
└──────────────────────────────────────────────────────────────────────┘
```

`#tag ▾` is a `<select>` populated from `GET /api/tags`, first option `All tags`. Selecting a tag
filters client-side (`tasks.filter(t => t.tags.includes(tag))`). Clicking a tag chip on a tile sets
it. Empty state per column is unchanged (an empty droppable area).

### 5.3 Task detail drawer

Tabs unchanged (`Initial Context`, `Summary`, `Timeline`, `Artifacts`, `KB`) plus a **Sessions**
strip under the title:

```
┌────────────────────────────────────────────┐
│ SW-42                                    ✕ │
│ Rework board title generation               │
│ claude·opus-5 ●   codex·gpt-5.1 ○           │  ← ● live, ○ ended
│ #swarm #mcp #board                          │
├─ Initial Context │ Summary │ Timeline │ …   │
```

`Summary` renders `handoffNote` — now always agent-written. Its empty state changes to
`_No summary yet — ask the agent to call swarm_task_update._`

---

## 6. User-facing copy

No i18n layer in this project; strings are inline.

### 6.1 Session briefing (`sessionBriefing`, delivered as `additionalContext`)

Unbound session:

```
## Agent Swarm
Board: http://127.0.0.1:7777 · your swarm session: `{sessionId}`

This session is not on the board. When you start work worth coordinating with other
agents — a feature, an investigation, a handoff — call `swarm_task_create` with:
- `title`: a specific, human-readable name you write yourself (max ~60 chars)
- `summary`: 2-5 sentences in your own words — goal, current state, next step
- `tags`: lowercase labels for filtering, e.g. ["repo-name", "backend", "bugfix"]
- `sessionId`: `{sessionId}`

To help on work already on the board, call `swarm_task_join` with its key instead.
Do not create a board item for trivial, throwaway, or read-only turns.
Keep `summary` current with `swarm_task_update` whenever the picture changes.
```

Bound session:

```
## Agent Swarm
Board: http://127.0.0.1:7777 · your swarm session: `{sessionId}`
You are working on **{key} — {title}** ({status}).

Call `swarm_task_update` with a fresh `summary` when the goal, state, or next step
changes, and `swarm_task_stage` to move it between columns. Add `tags` as they become
obvious. Use `swarm_handoff` before handing the work to another agent.
```

### 6.2 MCP tool descriptions (verbatim)

- `swarm_task_create` — "Create a board item for work worth coordinating. YOU write the title and
  summary — do not use a generated or templated name. Skip trivial or read-only turns."
- `swarm_task_update` — "Update a board item's title, summary, status or tags. Call this whenever
  the goal, state or next step changes so other agents see the current picture."
- `swarm_task_join` — "Attach this session to an existing board item so several agents can work on
  it at once. Additive — it does not take the item away from anyone."
- `swarm_memory_write` — "Save a durable memory to the shared knowledge base. Use for facts,
  decisions and gotchas that outlive this session."
- `swarm_memory_search` — "Search durable memories saved by any agent."

### 6.3 Errors

| condition | message |
|---|---|
| task key not found | `Task not found: {key}` |
| join with unknown session | `No swarm session for this connection — pass sessionId explicitly.` |
| claim on a claimed task | `Task already claimed or not found` (unchanged) |
| KB index failure | `Written: {path} (index deferred: {reason})` (unchanged) |
| daemon down (mcp-stdio) | `swarm daemon not running at {url}\nTry: launchctl kickstart -k gui/{uid}/dev.swarm.daemon` (unchanged) |

### 6.4 CLI

`swarm summaries backfill` and `swarm titles backfill` are removed from `--help`. New line:

```
  swarm kb reindex    Re-embed every markdown file under ~/.swarm/kb
```

---

## 7. File list

### Created

| path | responsibility |
|---|---|
| `packages/core/src/sessions.ts` | `SessionService` — the session↔task join |
| `packages/core/src/sessions.test.ts` | bind/resolve/labels |
| `packages/core/src/indexer.ts` | `KbIndexer` — task/artifact/transcript embedding |
| `packages/core/src/indexer.test.ts` | ingest writes and indexes |
| `packages/core/src/tags.test.ts` | `normalizeTags`, tag filtering |
| `packages/daemon/src/hookRoutes.test.ts` | hooks never create tasks |
| `plugin/opencode/swarm.js` | OpenCode plugin (events → daemon hooks) |
| `plugin/skills/swarm-task/SKILL.md` | create/update/join instructions |
| `plugin/skills/swarm-memory/SKILL.md` | memory read/write instructions |
| `plugin/commands/task.md` | `/task` slash command |
| `docs/specs/2026-09-02-board-coordination-pivot.md` | this spec |

### Modified

| path | change |
|---|---|
| `packages/core/src/db.ts` | schema v3 migration, backup, purge, vacuum |
| `packages/core/src/types.ts` | `opencode` kind, `SessionRecord`, `TaskWithSessions`, `BoardFilters.tag` |
| `packages/core/src/tasks.ts` | tags, `TaskWithSessions`, big deletions |
| `packages/core/src/hooks.ts` | `classifyHookEvent`, `sessionBriefing`, `hookSessionKey`, opencode |
| `packages/core/src/kb.ts` | `search(..., {subdir})`, `memory`/`tasks`/`transcripts` subdirs |
| `packages/core/src/transcripts.ts` | keep path resolution + text extraction; drop summary context builder |
| `packages/core/src/cursorSessions.ts` | strip title scraping, keep path resolution |
| `packages/core/src/antigravitySessions.ts` | strip title scraping, keep path resolution |
| `packages/core/src/index.ts` | exports follow the deletions |
| `packages/daemon/src/context.ts` | `sessions`, `indexer`; drop `memory` and consolidation |
| `packages/daemon/src/routes.ts` | rewritten hook routes, `/api/tags`, board filters |
| `packages/daemon/src/mcp.ts` | new tools, `McpClientMeta` from headers |
| `packages/daemon/src/jobs.ts` | reaper only; drop janitor + memory jobs |
| `packages/mcp-stdio/src/index.ts` | send `x-swarm-*` headers; register session row |
| `packages/web/src/App.tsx` | agent/model/tag chips, tag filter, sessions strip |
| `packages/cli/src/commands.ts` | opencode agent + sync, `kb reindex`, drop backfills |
| `packages/cli/src/index.ts` | command table follows |
| `plugin/hooks/post-hook.mjs` | relay the daemon's JSON response to stdout |
| `plugin/hooks/hooks.json` | drop `MessageDisplay`; `SessionStart` must be sync |
| `plugin/cursor-hooks.json` | drop `afterAgentThought`/`afterAgentResponse` |
| `plugin/.codex-plugin/hooks.json` | unchanged events, `SessionStart` sync |
| `plugin/hooks.json` | antigravity, regenerated by `buildAntigravityHooks` |
| `plugin/AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `rules/*.md` | new protocol text |
| `plugin/skills/swarm-*/SKILL.md` | concrete instructions |
| `plugin/.opencode-plugin/plugin.json` | opencode plugin manifest |
| `scripts/e2e-smoke.mjs` | new assertions |
| `README.md` | new model, opencode, MCP tool list |

### Deleted

`packages/core/src/sessionTitles.ts`, `sessionTitles.test.ts`, `titleJob.ts`, `summaryJob.ts`,
`sessionSummary.ts`, `hookEnrichment.ts`, `hookEnrichment.test.ts`, `hookInputFromTask.ts`,
`memory.ts`, `tasks.dedup.test.ts`; `packages/daemon/src/titleJob.ts`,
`packages/daemon/src/sessionSummary.ts`.

### Reused unchanged

`packages/core/src/ollama.ts` (minus `summarize`/`summarizeTaskTitle`), `packages/core/src/db.ts`
`SqliteVectorIndex`, `packages/cli/src/codexHooks.ts`, `packages/cli/src/antigravityHooks.ts`,
`packages/daemon/src/index.ts`.

---

## 8. Verification

### 8.1 Command order

```bash
pnpm install
pnpm lint
pnpm build
pnpm test
SWARM_HOME=/tmp/swarm-e2e node scripts/e2e-smoke.mjs
```

### 8.2 End-to-end scenarios

1. **Hook without MCP creates nothing.**
   `POST /hooks/claude/SessionStart {session_id:"s1", cwd:"/tmp/x"}` →
   `GET /api/board` has 0 tasks; the response body contains `hookSpecificOutput.additionalContext`
   with `s1`.
2. **Tool events on an unbound session are dropped.**
   `POST /hooks/claude/PostToolUse {session_id:"s1", tool_name:"Write", tool_input:{file_path:"/tmp/x/a.md"}}`
   → board still 0 tasks, no `task_events` rows.
3. **Agent creates the item.**
   `swarm_task_create {title:"Fix board titles", summary:"…", tags:["board","mcp"], sessionId:"s1"}`
   → board has 1 task, `tags == ["board","mcp"]`, `sessions == [{sessionId:"s1", agent:"claude"}]`,
   `~/.swarm/kb/tasks/SW-1.md` exists and is indexed (`swarm_kb_search "board titles"` hits it).
4. **Now hooks land on it.** Repeat scenario 2 → `artifacts_json.files` contains `/tmp/x/a.md`,
   one `PostToolUse` event.
5. **Second agent joins.**
   `POST /hooks/codex/SessionStart {session_id:"s2", cwd:"/tmp/x"}` then
   `swarm_task_join {key:"SW-1", sessionId:"s2", agent:"codex", model:"gpt-5.1"}` →
   `GET /api/board` shows two sessions, two agent labels, one tile.
6. **Agent updates its own summary.**
   `swarm_task_update {key:"SW-1", summary:"Migration landed", status:"review", addTags:["done-ish"]}`
   → `handoffNote` is exactly that text, status `review`, tags include `done-ish`,
   `kb/tasks/SW-1.md` re-indexed with the new body.
7. **Tag filter.** `GET /api/board?tag=board` returns SW-1; `?tag=nope` returns `[]`;
   `GET /api/tags` includes `board`, `mcp`, `done-ish`.
8. **Session end ingests artifacts and transcript.**
   Write `/tmp/x/a.md`, `POST /hooks/claude/SessionEnd {session_id:"s1"}` →
   `kb_docs` has a row whose path is `/tmp/x/a.md`; when a transcript path resolves,
   `kb/transcripts/SW-1-s1.md` exists.
9. **Subagent shares the parent's tile.**
   `POST /hooks/claude/SubagentStart {session_id:"s1", agent_id:"a1", agent_type:"Explore"}` →
   board count stays 1; `sessions` has `a1` with `task_id` = SW-1's id and
   `parent_session_id = "s1"`.
10. **Migration.** Against a copy of the live DB: version goes 2→3, `pre-v3-*.db` exists in
    `~/.swarm/backups`, every tile whose `initial_context` starts with `## Session` is `archived`,
    `idx_tasks_session_unique` is gone, the file shrinks.

### 8.3 Skip / decline / error / exhaust paths

- **Ollama down:** `kb.indexFile` throws → `KbIndexer` logs and returns the paths it managed;
  `swarm_task_create` still returns the task. Assert by pointing `ollamaUrl` at a dead port.
- **Daemon down:** `post-hook.mjs` catches, prints the platform default (`{}` /
  `{"decision":"allow"}`), exits 0. Assert with `SWARM_URL=http://127.0.0.1:1`.
- **MCP call with no session:** `swarm_task_create` with no `sessionId`, no headers and no matching
  cwd → task is created **unbound**, response notes `session: none`.
- **Join a missing task:** `swarm_task_join {key:"SW-999"}` → `isError: true`,
  `Task not found: SW-999`.
- **Transcript missing:** `indexTranscript` returns `null`, `ingestSession` still returns the task
  and artifact paths.
- **Malformed hook body:** `POST /hooks/claude/Stop` with `{}` → 200, no writes.

---

## 9. Explicitly out of scope

- Rewriting the board's drag-and-drop or column set.
- Replacing the `/api/chat` Console or its model.
- Any remote/multi-machine board, auth beyond the loopback token.
- Migrating historical tiles into agent-written titles — they are archived, not rewritten.
- Per-tag colours, tag management UI, or multi-tag filtering (single tag select only).
- Windows/Linux support (`launchd` remains macOS-only).
- Publishing the plugin to any marketplace.

---

## 10. Task plan

Tasks are ordered by dependency. Each ends with `pnpm build` green for the packages it touches and
a commit.

### Task 1 — Schema v3 + `SessionService`

**Files:** create `packages/core/src/sessions.ts`, `packages/core/src/sessions.test.ts`;
modify `packages/core/src/db.ts`, `packages/core/src/types.ts`, `packages/core/src/index.ts`;
delete `packages/core/src/tasks.dedup.test.ts`.

**Produces:** `SessionService`, `SessionRecord`, `TaskSessionLabel`, `AgentKind` with `opencode`.

- [ ] Write `sessions.test.ts` covering: `upsert` is idempotent; `resolve({sessionId})` beats
      `resolve({cwd})`; `resolve({cwd})` picks the newest `last_seen_at` with `ended_at IS NULL`;
      `bind` then `labelsForTasks` returns one label per session with `active` computed;
      `end` clears `active`.
- [ ] Run `pnpm --filter @swarm/core test` — fails (module missing).
- [ ] Implement §3.1 migration in `db.ts` and `SessionService` in `sessions.ts`.
- [ ] Run tests — pass. Commit.

### Task 2 — Tags and `TaskWithSessions`

**Files:** modify `packages/core/src/tasks.ts`; create `packages/core/src/tags.test.ts`.

**Consumes:** `SessionService.labelsForTasks`.
**Produces:** `normalizeTags`, `TaskService.{setTags,addTags,removeTags,getTags,listAllTags}`,
`list(): TaskWithSessions[]`.

- [ ] Write `tags.test.ts`: `normalizeTags` lowercases, dedupes, sorts, caps at 20/40 chars, drops
      empties; `list({tag})` matches via `json_each`; `list({agent})` matches a bound session.
- [ ] Run — fails.
- [ ] Implement; delete `upsertSessionTask`, `consolidateTasksBySessionId`, `janitorArchive`,
      `maybeRefresh*`, `applySessionSummary`, `listNeedingBackfill`, `listNeedingSummary`,
      `hasActiveSessions`.
- [ ] Run — pass. Commit.

### Task 3 — Delete the LLM title/summary layer

**Files:** delete `sessionTitles.ts(+test)`, `titleJob.ts`, `summaryJob.ts`, `sessionSummary.ts`,
`hookEnrichment.ts(+test)`, `hookInputFromTask.ts`, `memory.ts`,
`packages/daemon/src/{titleJob,sessionSummary}.ts`; modify `hooks.ts`, `transcripts.ts`,
`cursorSessions.ts`, `antigravitySessions.ts`, `ollama.ts`, `index.ts`, `context.ts`, `jobs.ts`.

**Produces:** `classifyHookEvent`, `hookSessionKey`, `sessionBriefing`, `resolveTranscriptPath`,
`extractTranscriptText`.

- [ ] Delete the modules above and every import of them.
- [ ] Move `encodeCursorProjectPath` into `cursorSessions.ts` (it currently re-exports from
      `sessionTitles.ts`) and `findClaudeTranscriptPath` into `transcripts.ts`.
- [ ] Strip title scraping from `cursorSessions.ts` / `antigravitySessions.ts`; keep
      `resolveCursorSession`/`resolveAntigravitySession` returning `{conversationId, cwd?,
      transcriptPath?}`.
- [ ] Remove `OllamaClient.summarize` and `summarizeTaskTitle`.
- [ ] Add `classifyHookEvent`, `hookSessionKey`, `sessionBriefing`, opencode branches in
      `detectPlatform`/`formatHookOutput`.
- [ ] `pnpm build` green. Commit.

### Task 4 — `KbIndexer` and KB subdir search

**Files:** create `packages/core/src/indexer.ts`, `indexer.test.ts`; modify `kb.ts`, `index.ts`.

**Consumes:** `KbStore`, `TaskService`, `SessionService`, `transcripts.extractTranscriptText`.
**Produces:** `KbIndexer`, `KbStore.search(q, limit, {subdir})`.

- [ ] Write `indexer.test.ts` with a stub embedder: `indexTask` writes `kb/tasks/SW-1.md` with
      title/tags frontmatter; `indexArtifacts` indexes only existing `.md` paths;
      `indexTranscript` returns `null` when no transcript resolves; `ingestSession` swallows
      indexing errors.
- [ ] Run — fails. Implement. Run — pass. Commit.

### Task 5 — Hook routes rewrite

**Files:** modify `packages/daemon/src/routes.ts`, `context.ts`; create `hookRoutes.test.ts`.

**Consumes:** everything above.

- [ ] Write `hookRoutes.test.ts`: scenarios 1, 2, 4, 9 from §8.2 against an in-process Fastify app
      with a temp `SWARM_HOME`.
- [ ] Run — fails.
- [ ] Rewrite the hook handler around `classifyHookEvent`; drop `MessageDisplay`,
      `afterAgentThought`, `afterAgentResponse` deltas, `/api/summaries/backfill`; add
      `GET /api/tags`; wire `tag` into `/api/board`; return `formatHookOutput` bodies.
- [ ] Run — pass. Commit.

### Task 6 — MCP tools

**Files:** modify `packages/daemon/src/mcp.ts`, `packages/mcp-stdio/src/index.ts`.

- [ ] Add `McpClientMeta` capture from `x-swarm-cwd` / `x-swarm-agent` / `x-swarm-session-id` at
      `initialize`; thread it into `createMcpServer`.
- [ ] Add `swarm_task_create`, `swarm_task_update`, `swarm_task_join`, `swarm_memory_write`,
      `swarm_memory_search`; add `tag` to `swarm_board`, `subdir` to `swarm_kb_search`,
      `tags` to `swarm_kb_write`; use the §6.2 descriptions verbatim.
- [ ] Make `mcp-stdio` send the three headers and register its session row on start.
- [ ] `pnpm build`; drive the tools through `scripts/e2e-smoke.mjs`. Commit.

### Task 7 — Board UI

**Files:** modify `packages/web/src/App.tsx`.

- [ ] Extend `Task` with `tags: string[]` and `sessions: TaskSessionLabel[]`.
- [ ] Render §5.1 tile, §5.2 header tag select (fed by `GET /api/tags`), §5.3 sessions strip.
- [ ] Add `opencode` to `AGENT_COLORS`.
- [ ] `pnpm --filter @swarm/web build`. Commit.

### Task 8 — OpenCode support

**Files:** create `plugin/opencode/swarm.js`, `plugin/.opencode-plugin/plugin.json`;
modify `packages/cli/src/commands.ts`, `packages/cli/src/index.ts`, `packages/core/src/types.ts`.

- [ ] Write `plugin/opencode/swarm.js` per §4 A1 (defensive session-id extraction), posting
      `SessionStart` / `Stop` / `SessionEnd` / `PreToolUse` / `PostToolUse` to
      `/hooks/opencode/<event>`.
- [ ] Add `opencode` to `ALL_AGENT_IDS` and `detectAgents` (`~/.config/opencode`).
- [ ] Add `mergeOpencodeConfig`: symlink `~/.config/opencode/plugins/swarm.js` →
      `<plugin>/opencode/swarm.js`; merge `mcp.swarm` (`type:"local"`,
      `command:["node", launcher]`, `environment:{SWARM_URL, SWARM_AGENT:"opencode"}`) and
      `instructions` into `~/.config/opencode/opencode.json`.
- [ ] Add an opencode row to `runDoctor`; remove `summaries`/`titles` backfill commands and add
      `swarm kb reindex`.
- [ ] `pnpm build`. Commit.

### Task 9 — Plugin instructions and skills

**Files:** modify `plugin/hooks/post-hook.mjs`, `plugin/hooks/hooks.json`,
`plugin/cursor-hooks.json`, `plugin/.codex-plugin/hooks.json`, `plugin/AGENTS.md`,
`plugin/CLAUDE.md`, `plugin/GEMINI.md`, `plugin/rules/*.md`, `plugin/skills/*/SKILL.md`,
`plugin/commands/*.md`; create `plugin/skills/swarm-task/SKILL.md`,
`plugin/skills/swarm-memory/SKILL.md`, `plugin/commands/task.md`.

- [ ] `post-hook.mjs`: relay the daemon's JSON body to stdout; fall back to the current defaults on
      any error.
- [ ] Remove `MessageDisplay` / `afterAgentThought` / `afterAgentResponse` hook registrations.
- [ ] Rewrite every instruction file around: hooks do not create tiles; you name your own work;
      `swarm_task_create` / `swarm_task_join` / `swarm_task_update` / `swarm_handoff` /
      `swarm_memory_*`; write summaries from your own conversation, not from a template.
- [ ] Commit.

### Task 10 — Smoke test, docs, verification

**Files:** modify `scripts/e2e-smoke.mjs`, `README.md`.

- [ ] Rewrite the smoke test to assert §8.2 scenarios 1–9.
- [ ] Update `README.md`: agent list incl. opencode, MCP tool table, "agents name their own tasks".
- [ ] Run the full §8.1 command order. Commit.

### Task 11 — Redeploy

- [ ] `launchctl bootout gui/$UID/dev.swarm.daemon` (or `kickstart -k` after the swap).
- [ ] In `~/.swarm/app/releases/dev`: `git fetch && git checkout <branch> && pnpm install &&
      pnpm build`.
- [ ] `launchctl kickstart -k gui/$UID/dev.swarm.daemon`; confirm `/api/health`, board loads,
      migration ran (`schema_meta.version = 3`).
- [ ] `swarm plugin sync` so every agent picks up the new instructions.
