-- 0012_workflows.sql: the workflow engine schema (spec B1). NULL
-- workflow_json on an item means it's a legacy task with no workflow.
ALTER TABLE items ADD COLUMN workflow_json TEXT;
ALTER TABLE items ADD COLUMN steps_json    TEXT;
ALTER TABLE items ADD COLUMN units_json    TEXT; -- spec B3/C4: batched units, instead of steps
ALTER TABLE items ADD COLUMN solo          TEXT; -- spec B3/C4: why this is a single-unit package
ALTER TABLE items ADD COLUMN verify_json   TEXT;

ALTER TABLE checkpoints ADD COLUMN verdict TEXT
  CHECK (verdict IS NULL OR verdict IN ('pass','changes_requested','blocked'));
ALTER TABLE checkpoints ADD COLUMN findings_json TEXT;

CREATE TABLE workflows (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES items(id),       -- many rows over time; at most one non-terminal
  root_item_id  TEXT NOT NULL REFERENCES items(id),
  owner_agent_id TEXT NOT NULL REFERENCES agents(id),   -- the orchestrator that started it
  state         TEXT NOT NULL CHECK (state IN
                  ('running','succeeded','escalated','failed','cancelled')),
  round         INTEGER NOT NULL DEFAULT 1,
  extra_rounds  INTEGER NOT NULL DEFAULT 0,              -- spec B7: swarm_workflow resume retry grant
  escalation    TEXT,                                    -- reason when escalated
  context_json  TEXT,                                    -- extra brief context from start
  worktrees_json TEXT NOT NULL,                          -- [{worktree_id, mode}] for run steps
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE workflow_runs (
  id            TEXT PRIMARY KEY,
  workflow_id   TEXT NOT NULL REFERENCES workflows(id),
  step_id       TEXT NOT NULL,
  round         INTEGER NOT NULL,
  role          TEXT NOT NULL,
  agent_id      TEXT REFERENCES agents(id),              -- NULL while waiting for budget
  state         TEXT NOT NULL CHECK (state IN
                  ('waiting','active','completed','failed','cancelled')),
  verdict       TEXT,
  findings_json TEXT,
  review_worktree_id TEXT,
  sha           TEXT,                                    -- sha produced (run) or reviewed (review)
  auto_retries  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  ended_at      INTEGER,
  UNIQUE (workflow_id, step_id, round, role)
);
CREATE INDEX workflow_runs_agent ON workflow_runs(agent_id);
CREATE UNIQUE INDEX workflows_one_live ON workflows(item_id)
  WHERE state IN ('running','escalated');
