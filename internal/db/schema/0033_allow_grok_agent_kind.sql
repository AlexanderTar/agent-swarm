-- 0033_allow_grok_agent_kind.sql: add 'grok' to agents.kind CHECK constraint.
-- SQLite has no ALTER TABLE ... ALTER CONSTRAINT, so this follows 0008's and
-- 0011's table-rebuild pattern (create new, copy rows, drop old, rename new
-- into place, recreate indexes). agents has no triggers. Columns are those
-- of 0011's table plus the later ALTER TABLE ADD COLUMNs, in the order they
-- were added (0014 auto_restart, 0016 kind_reason, 0017
-- advisor_requested_effort, 0030 low_token), so SELECT * copies positionally.
CREATE TABLE agents_new (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  kind            TEXT NOT NULL CHECK (kind IN ('claude','codex','agy','cursor','muse','grok','fake')),
  model           TEXT NOT NULL,
  effort          TEXT,
  role            TEXT NOT NULL CHECK (role IN
                    ('orchestrator','coder','reviewer','ui_reviewer','researcher','debugger','mechanical','designer')),
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
  role_overrides  TEXT,
  auto_restart    INTEGER NOT NULL DEFAULT 1,
  kind_reason     TEXT,
  advisor_requested_effort TEXT,
  low_token       INTEGER
);

INSERT INTO agents_new SELECT * FROM agents;

DROP TABLE agents;

ALTER TABLE agents_new RENAME TO agents;

CREATE INDEX agents_parent ON agents(parent_agent_id);
CREATE INDEX agents_root   ON agents(root_item_id, state);
