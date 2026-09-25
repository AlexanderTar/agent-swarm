-- 0011_designer_and_artifact_kinds.sql: add 'designer' to agents.role CHECK;
-- add 'design' and 'research' to artifacts.kind CHECK (spec A5). SQLite has
-- no ALTER TABLE ... ALTER CONSTRAINT, so both rebuilds follow 0008's
-- table-rebuild pattern (create new, copy rows, drop old, rename new into
-- place, recreate indexes -- proven live against agents' self-referential
-- parent_agent_id).
CREATE TABLE agents_new (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  kind            TEXT NOT NULL CHECK (kind IN ('claude','codex','agy','cursor','muse','fake')),
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
  role_overrides  TEXT
);

INSERT INTO agents_new SELECT * FROM agents;

DROP TABLE agents;

ALTER TABLE agents_new RENAME TO agents;

CREATE INDEX agents_parent ON agents(parent_agent_id);
CREATE INDEX agents_root   ON agents(root_item_id, state);

CREATE TABLE artifacts_new (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES items(id),
  kind          TEXT NOT NULL CHECK (kind IN ('spec','plan','debug_report','note','design','research')),
  path          TEXT NOT NULL,
  head_revision INTEGER NOT NULL,
  created_by    TEXT REFERENCES agents(id),
  created_at    INTEGER NOT NULL,
  UNIQUE (item_id, path)
);

INSERT INTO artifacts_new SELECT * FROM artifacts;

DROP TABLE artifacts;

ALTER TABLE artifacts_new RENAME TO artifacts;
