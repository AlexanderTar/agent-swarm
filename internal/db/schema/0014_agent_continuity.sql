-- 0014_agent_continuity.sql: durable agent-continuity state.
-- agents.id is canonical across restarts: a new session changes only the
-- sessions row (id, generation, token, provider session). Nothing here
-- renames, deletes or merges an agent row: the backfill only appends each
-- pre-migration agent as the root of its own lineage chain.

-- One durable row per replacement intent (mode pause|handoff|recover).
-- The intent row is committed before any side effect runs, so a daemon
-- restart resumes from the durable phase instead of losing the intent.
CREATE TABLE agent_operations (
  id           TEXT PRIMARY KEY,
  agent_id     TEXT NOT NULL REFERENCES agents(id),
  mode         TEXT NOT NULL CHECK (mode IN ('pause', 'handoff', 'recover')),
  phase        TEXT NOT NULL CHECK (phase IN
                 ('requested', 'preserving', 'stopping', 'ready',
                  'queued', 'starting', 'succeeded', 'blocked', 'cancelled')),
  request_key  TEXT NOT NULL DEFAULT '',
  session_id   TEXT, -- predecessor session observed when the intent was recorded
  generation   INTEGER NOT NULL DEFAULT 0, -- predecessor generation, compared on every transition
  note         TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
-- One nonterminal operation per agent: a second intent while one is still
-- in flight is a 409 naming the active operation, never a second row.
-- Terminal phases (succeeded, blocked, cancelled) are excluded so history
-- accumulates while only one operation can ever be in flight.
CREATE UNIQUE INDEX agent_operations_one_active ON agent_operations(agent_id)
  WHERE phase IN ('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting');
-- Request identity is durable per canonical agent + request key: replaying
-- the same key returns the same operation row instead of starting a new one.
CREATE UNIQUE INDEX agent_operations_agent_key ON agent_operations(agent_id, request_key)
  WHERE request_key <> '';
CREATE INDEX agent_operations_phase ON agent_operations(phase, updated_at);

-- Lineage links each agent to its predecessor without touching agent rows.
CREATE TABLE agent_lineage (
  id                   TEXT PRIMARY KEY,
  agent_id             TEXT NOT NULL REFERENCES agents(id),
  session_id           TEXT REFERENCES sessions(id),
  generation           INTEGER NOT NULL DEFAULT 1,
  predecessor_agent_id TEXT REFERENCES agents(id),
  root_item_id         TEXT NOT NULL REFERENCES items(id),
  item_id              TEXT NOT NULL REFERENCES items(id),
  role                 TEXT NOT NULL,
  created_at           INTEGER NOT NULL
);
CREATE INDEX agent_lineage_agent ON agent_lineage(agent_id, generation);
CREATE INDEX agent_lineage_lookup ON agent_lineage(root_item_id, item_id, role);

-- Backfill: every pre-migration agent is the root of its own lineage chain.
INSERT INTO agent_lineage (id, agent_id, session_id, generation, predecessor_agent_id,
  root_item_id, item_id, role, created_at)
  SELECT 'lin_' || a.id, a.id, NULL, 1, NULL,
    a.root_item_id, a.item_id, a.role, CAST(strftime('%s', 'now') AS INTEGER) * 1000
  FROM agents a;

-- User Cancel flips this to 0 (execution stops, auto-restart disabled, the
-- agent row itself keeps its identity). It defaults to 1: stop, crash or
-- session-cancel with an unfinished assignment stays recoverable.
ALTER TABLE agents ADD COLUMN auto_restart INTEGER NOT NULL DEFAULT 1;
