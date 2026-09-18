CREATE TABLE schema_meta (
  version INTEGER NOT NULL,
  embed_model TEXT NOT NULL,
  embed_dimensions INTEGER NOT NULL,
  migrated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL DEFAULT 'Untitled',
  status TEXT NOT NULL DEFAULT 'in_progress',
  priority TEXT NOT NULL DEFAULT 'medium',
  repo_path TEXT,
  repo_remote TEXT,
  branch TEXT,
  worktree TEXT,
  origin_agent TEXT NOT NULL DEFAULT 'unknown',
  origin_session_id TEXT,
  origin_model TEXT,
  origin_cwd TEXT,
  origin_pid INTEGER,
  claimed_by TEXT,
  claimed_agent TEXT,
  claimed_session_id TEXT,
  claimed_at TEXT,
  claim_expires_at TEXT,
  heartbeat_at TEXT,
  initial_context TEXT,
  handoff_note TEXT,
  artifacts_json TEXT NOT NULL DEFAULT '{}',
  kb_links_json TEXT NOT NULL DEFAULT '[]',
  tags_json TEXT NOT NULL DEFAULT '[]',
  turn_count INTEGER NOT NULL DEFAULT 0,
  last_activity_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  parent_task_id INTEGER,
  required INTEGER NOT NULL DEFAULT 1,
  coordinator_session_id TEXT,
  claim_token TEXT
);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_session ON tasks(origin_session_id);
CREATE INDEX idx_tasks_claim ON tasks(claimed_by, claim_expires_at);
CREATE INDEX idx_tasks_updated ON tasks(updated_at);
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id);

CREATE TABLE task_sessions (
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
CREATE UNIQUE INDEX idx_task_sessions_session ON task_sessions(session_id);
CREATE INDEX idx_task_sessions_task ON task_sessions(task_id);

INSERT INTO schema_meta (version, embed_model, embed_dimensions)
VALUES (4, 'nomic-embed-text', 256);
