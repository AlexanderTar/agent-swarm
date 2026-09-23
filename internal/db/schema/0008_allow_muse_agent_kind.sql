-- 0008_allow_muse_agent_kind.sql: add 'muse' to agents.kind CHECK constraint.
-- Muse was wired in at the application layer (kinds.AgentKinds, the muse
-- adapter, catalog, usage source, menubar) across several merges today, but
-- this constraint was never migrated -- every attempt to spawn a real muse
-- agent has been failing with "CHECK constraint failed: kind IN
-- ('claude','codex','agy','cursor','fake')" since. SQLite has no ALTER
-- TABLE ... ALTER CONSTRAINT, so this follows 0003_add_chore.sql's own
-- table-rebuild pattern (already proven live against this exact table's
-- self-referential parent_agent_id -> agents(id) FK, via items' identical
-- parent_id -> items(id) self-reference).
CREATE TABLE agents_new (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  kind            TEXT NOT NULL CHECK (kind IN ('claude','codex','agy','cursor','muse','fake')),
  model           TEXT NOT NULL,
  effort          TEXT,
  role            TEXT NOT NULL CHECK (role IN
                    ('orchestrator','coder','reviewer','ui_reviewer','researcher','debugger','mechanical')),
  item_id         TEXT NOT NULL REFERENCES items(id),
  root_item_id    TEXT NOT NULL REFERENCES items(id),
  parent_agent_id TEXT REFERENCES agents(id),
  advisor_kind    TEXT,
  advisor_model   TEXT,
  advisor_effort  TEXT,
  advisor_mode    TEXT CHECK (advisor_mode IN ('native','simulated')),
  brief           TEXT NOT NULL,
  state           TEXT NOT NULL CHECK (state IN ('queued','active','finished','acknowledged')),
  preflight_error TEXT, -- set only when the agent never spawned (contracts §3.2 AgentNode)
  created_at      INTEGER NOT NULL,
  finished_at     INTEGER,
  role_overrides  TEXT
);

INSERT INTO agents_new SELECT * FROM agents;

DROP TABLE agents;

ALTER TABLE agents_new RENAME TO agents;

CREATE INDEX agents_parent ON agents(parent_agent_id);
CREATE INDEX agents_root   ON agents(root_item_id, state);
