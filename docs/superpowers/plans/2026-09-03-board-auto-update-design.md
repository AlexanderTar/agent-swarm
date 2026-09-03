# Board Auto-Update with Tags and Agent Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Board stays correct when agents claim/join tasks, with tags, status, and full agent metadata auto-extracted.

**Architecture:** Daemon auto-enrichment (tags writers, join tool, MCP auto-inject, pid/transcript capture, UI fallback) + unified instruction mandates for all 5 agents + one-time board repair. Instructions are fallback, daemon is source of truth.

**Tech Stack:** TypeScript, better-sqlite3, Fastify daemon, MCP SDK, Vite React UI, launchd

**Spec:** Brainstorming approved hybrid design SW-374 (daemon-centric + instruction mandates + repair + deploy refresh)

## Global Constraints

- Node >=20, pnpm 10.25.0
- Board UI: http://127.0.0.1:7777
- Never delete user tasks, only archive stale orphans after confirmation
- All MCP tool changes must keep backward compat (optional params only)
- Hooks always exit 0, 4s timeout, fire-and-forget

---

### Task 1: Tags writers in core + daemon

**Files:**
- Modify: `packages/core/src/tasks.ts:54-89,253-275`
- Modify: `packages/core/src/types.ts:45-75`
- Modify: `packages/daemon/src/mcp.ts:39-67,91-111`
- Modify: `packages/daemon/src/routes.ts:412-458`
- Test: `packages/core/src/tasks.test.ts` (create if missing, check existing tests first)

**Interfaces:**
- Consumes: existing `TaskService.create/update/claim/stage`
- Produces: `create({tags?: string[]})`, `update(id, {tags?: string[]})`, `setTags/addTags`, MCP `swarm_task_create({tags?})`, `swarm_task_stage({tags?})`

- [ ] **Step 1: Write failing test for tags on create**

```typescript
// packages/core/src/tasks.test.ts
import { describe, it, expect } from "vitest";
import Database from "better-sqlite3";
import { initDb } from "./db.js";
import { TaskService } from "./tasks.js";

describe("tags", () => {
  it("persists tags on create", () => {
    const db = new Database(":memory:");
    initDb(db);
    const svc = new TaskService(db);
    const t = svc.create({ title: "t", originAgent: "claude", tags: ["agent-swarm", "backend"] } as never);
    expect(JSON.parse(t.tagsJson)).toEqual(["agent-swarm", "backend"]);
  });
}
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `pnpm --filter @swarm/core test -- tags`
Expected: FAIL (tags param ignored / TS error)

- [ ] **Step 3: Minimal implementation in tasks.ts**

```typescript
// create(): add tags?: string[] to input, normalize lowercase, INSERT tags_json
// update(): accept tags?: string[], addTags?: string[], removeTags?: string[]
// add setTags(id, tags: string[]) helper with JSON.stringify + lowercase + dedup
```

Exact: `INSERT INTO tasks (..., tags_json, ...)` with `JSON.stringify((input.tags ?? []).map(t=>t.toLowerCase()))`. Update merges.

- [ ] **Step 4: Expose via MCP + REST**

```typescript
// mcp.ts swarm_task_create: add tags: z.array(z.string()).optional(), pass through
// mcp.ts swarm_task_stage: add tags/addTags/removeTags optional, apply after stage
// routes.ts POST /api/tasks: accept tags, PATCH: accept tags/addTags/removeTags
```

- [ ] **Step 5: Run tests PASS + commit**

Run: `pnpm --filter @swarm/core test`
Expected: PASS
```bash
git add packages/core/src/tasks.ts packages/daemon/src/mcp.ts packages/daemon/src/routes.ts
git commit -m "feat: persist task tags via core, MCP and REST"
```

### Task 2: swarm_task_join + claim backfill + UI fallback

**Files:**
- Modify: `packages/core/src/tasks.ts:307-327`
- Modify: `packages/daemon/src/mcp.ts:55-111`
- Modify: `packages/web/src/App.tsx:86-140`

**Interfaces:**
- Consumes: `TaskService.claim/heartbeat`
- Produces: `join(key, {agent, sessionId, by, cwd, model, pid, transcriptPath})`, MCP `swarm_task_join`, UI shows claimed_agent fallback

- [ ] **Step 1: Write failing test for join + backfill**

```typescript
it("join appends session without stealing claim", () => {
  const t = svc.create({ title: "t", originAgent: "unknown" } as never);
  const r = svc.join(t.key, { agent: "codex", sessionId: "s1", by: "codex" } as never);
  expect(r.ok).toBe(true);
});
it("claim backfills unknown origin", () => {
  const t = svc.create({ title: "t", originAgent: "unknown" } as never);
  svc.claim(t.key, { agent: "claude", sessionId: "s2", by: "claude" }, 300);
  expect(svc.getByKey(t.key)!.originAgent).toBe("claude");
});
```

- [ ] **Step 2: Run, verify FAIL**

Run: `pnpm --filter @swarm/core test -- join`
Expected: FAIL (join undefined)

- [ ] **Step 3: Implement join + backfill**

```typescript
join(key, joiner): if unclaimed -> claim; if claimed by other -> appendEvent("join", joiner) + touch, return ok with task (do NOT overwrite claim). If origin_agent==="unknown", backfill origin_agent/session/model/cwd/pid from joiner.
claim(): after UPDATE, if existing origin_agent==="unknown", UPDATE origin_agent/session/model/cwd where provided.
```

- [ ] **Step 4: MCP swarm_task_join + UI fallback**

```typescript
// mcp.ts: server.tool("swarm_task_join", {key, agent?, sessionId?, by?, cwd?, model?, pid?}, async => svc.join + broadcast)
// App.tsx: AgentBadge shows claimed_agent ?? origin_agent; tooltip shows sessionId + transcript if present
```

- [ ] **Step 5: Tests PASS + commit**

Run: `pnpm --filter @swarm/core test && pnpm --filter @swarm/daemon build`
```bash
git commit -m "feat: swarm_task_join with claim backfill and UI fallback"
```

### Task 3: Hook + MCP auto-extraction (pid, model, transcript, cwd)

**Files:**
- Modify: `packages/core/src/hooks.ts:3-37,86-162`
- Modify: `packages/core/src/hookEnrichment.ts:84-103`
- Modify: `packages/daemon/src/routes.ts:68-120,353-368`
- Modify: `packages/mcp-stdio/src/index.ts:19-59`
- Modify: `packages/core/src/db.ts:37-67` (add transcript_path col if missing, else reuse artifacts)

**Interfaces:**
- Consumes: `normalizeHookInput`, `upsertSessionTask`, `syncTaskSessionMetadata`
- Produces: pid + transcript persisted, MCP auto-injects SWARM_AGENT/SESSION/CWD/PID/MODEL

- [ ] **Step 1: Failing test for pid parse**

```typescript
it("parses pid from hook payload", () => {
  const n = normalizeHookInput({ session_id: "s", pid: 1234, transcript_path: "/tmp/x.jsonl" }, "claude");
  expect((n as never as {pid:number}).pid).toBe(1234);
});
```

- [ ] **Step 2: Run FAIL, then implement**

```typescript
// hooks.ts: add pid?: number to NormalizedHookInput, parse from pid/process_pid/agent_pid
// routes.ts: pass pid + transcriptPath through to upsertSessionTask
// tasks.ts upsertSessionTask: accept transcriptPath?, persist to artifacts_json.transcript or new column
// mcp-stdio: always inject {agent: SWARM_AGENT ?? detect, sessionId: SWARM_SESSION_ID ?? uuid, cwd: process.cwd(), pid: process.pid} into stage/pickup/create calls if caller omitted
```

- [ ] **Step 3: Tests PASS + commit**

```bash
git commit -m "feat: auto-extract pid, transcript, model in hooks and MCP proxy"
```

### Task 4: Unified agent instructions (5 platforms)

**Files:**
- Modify: `plugin/CLAUDE.md`, `plugin/AGENTS.md`, `plugin/GEMINI.md`
- Modify: `plugin/rules/swarm.md`, `plugin/rules/swarm-cursor.md`
- Modify: `plugin/skills/swarm-status/SKILL.md`, `plugin/skills/swarm-pickup/SKILL.md`, `plugin/skills/swarm-handoff/SKILL.md`
- Create: `plugin/skills/swarm-task/SKILL.md` (claim/join/tags mandate)
- Create: `opencode.json` or `.opencode/` config + `plugin/.opencode-plugin/` if pattern matches others (check .cursor-plugin structure first)

**Interfaces:**
- Consumes: new MCP tools from Task 2
- Produces: identical board mandate block in all 5 entry points

Mandate block (copy verbatim into all):
```markdown
## Board (required)
- Session start: `swarm_board` (repo filter), `swarm_task_join` or `swarm_task_stage claim` when starting work.
- During: `swarm_task_stage heartbeat` every ~5min, `swarm_task_update` summary + tags when goal/state changes.
- Tags: lowercase `["<repo>", "<area>", "<kind>"]` e.g. `["agent-swarm","daemon","bugfix"]`.
- Metadata auto-captured (session id, transcript path, model, cwd, pid) — do NOT hand-edit, verify via `swarm_task_get`.
- End: `swarm_handoff` with goal/done/next/decisions/gotchas/verification/files.
```

- [ ] **Step 1: Edit CLAUDE.md + AGENTS.md + GEMINI.md with block above**
- [ ] **Step 2: Edit rules/swarm.md + swarm-cursor.md same block**
- [ ] **Step 3: Create skills/swarm-task/SKILL.md + opencode wiring**
- [ ] **Step 4: Verify no placeholders, commit**

```bash
git add plugin/CLAUDE.md plugin/AGENTS.md plugin/GEMINI.md plugin/rules/ plugin/skills/ opencode.json
git commit -m "docs: mandate board claim/join/tags/metadata for all agents"
```

### Task 5: One-time board repair + deploy refresh

**Files:** none (ops) — uses built CLI + direct SQL only for tags until Task 1 lands, else MCP.

- [ ] **Step 1: Backup DB**

```bash
cp ~/.swarm/swarm.db ~/.swarm/swarm.db.bak.$(date +%F)
```

- [ ] **Step 2: Archive stale orphans (manual review via board UI :7777)**

```bash
# list candidates
node packages/cli/dist/index.js status
# archive via MCP swarm_task_stage archive after user confirms each key
```

- [ ] **Step 3: Backfill tags via new API (after Task 1 deploy) or direct SQL as fallback**
- [ ] **Step 4: Refresh deploy**

```bash
cd /Users/alexandertar/GitHub/agent-swarm
pnpm install
pnpm build
node packages/cli/dist/index.js install --yes
node packages/cli/dist/index.js doctor
node packages/cli/dist/index.js status
curl -s http://127.0.0.1:7777/api/health
```

- [ ] **Step 5: Update SW-374 summary + heartbeat**
