-- 0001_init.sql: Agent Swarm 2 schema v1 (spec §5). Timestamps are INTEGER ms since the epoch (UTC).
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE key_counters (
  type TEXT PRIMARY KEY,
  next INTEGER NOT NULL
);

CREATE TABLE repos (
  id             TEXT PRIMARY KEY,
  path           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  remote_url     TEXT,
  remote_owner   TEXT,
  default_branch TEXT,
  source         TEXT NOT NULL CHECK (source IN ('scan','manual','history')),
  missing        INTEGER NOT NULL DEFAULT 0,
  last_used_at   INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE repo_groups (
  repo_id    TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  source     TEXT NOT NULL CHECK (source IN ('remote_owner','workspace_dir','code_workspace')),
  origin     TEXT,
  PRIMARY KEY (repo_id, name, source)
);

CREATE TABLE items (
  id              TEXT PRIMARY KEY,
  key             TEXT NOT NULL UNIQUE,
  type            TEXT NOT NULL CHECK (type IN ('epic','story','task','bug','spike')),
  parent_id       TEXT REFERENCES items(id) ON DELETE RESTRICT,
  root_id         TEXT NOT NULL REFERENCES items(id),
  title           TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  brief           TEXT NOT NULL DEFAULT '' CHECK (length(brief) <= 600),
  acceptance_json TEXT NOT NULL DEFAULT '[]',
  status          TEXT NOT NULL CHECK (status IN
                    ('draft','ready','in_progress','blocked','in_review',
                     'awaiting_approval','done','cancelled')),
  status_before_block TEXT,
  priority        INTEGER NOT NULL DEFAULT 2 CHECK (priority BETWEEN 0 AND 3),
  role_hint       TEXT,
  tdd_exempt      TEXT CHECK (tdd_exempt IN ('docs','config','mechanical-rename','spike-research')),
  confirmed_repos_json TEXT NOT NULL DEFAULT '[]',
  repos_version   INTEGER NOT NULL DEFAULT 0,
  repo_hints_json TEXT NOT NULL DEFAULT '[]',
  spike_intent    TEXT CHECK (spike_intent IN ('feature','debug')),
  suggested_repos_json TEXT NOT NULL DEFAULT '[]',
  origin_spike_id TEXT REFERENCES items(id),
  legacy_key      TEXT,
  sort_order      INTEGER NOT NULL DEFAULT 0,
  revision        INTEGER NOT NULL DEFAULT 1,
  archived_at     INTEGER,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX items_parent ON items(parent_id);
CREATE INDEX items_root   ON items(root_id, status);
CREATE INDEX items_type   ON items(type, status);

CREATE TABLE item_deps (
  item_id       TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  blocked_by_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (item_id, blocked_by_id),
  CHECK (item_id <> blocked_by_id)
);
CREATE INDEX item_deps_rev ON item_deps(blocked_by_id);

CREATE VIRTUAL TABLE items_fts USING fts5(key, title, brief, content='items', content_rowid='rowid');
CREATE TRIGGER items_fts_ai AFTER INSERT ON items BEGIN
  INSERT INTO items_fts(rowid, key, title, brief) VALUES (new.rowid, new.key, new.title, new.brief);
END;
CREATE TRIGGER items_fts_ad AFTER DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, key, title, brief) VALUES ('delete', old.rowid, old.key, old.title, old.brief);
END;
CREATE TRIGGER items_fts_au AFTER UPDATE OF key, title, brief ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, key, title, brief) VALUES ('delete', old.rowid, old.key, old.title, old.brief);
  INSERT INTO items_fts(rowid, key, title, brief) VALUES (new.rowid, new.key, new.title, new.brief);
END;

CREATE TABLE artifacts (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES items(id),
  kind          TEXT NOT NULL CHECK (kind IN ('spec','plan','debug_report','note')),
  path          TEXT NOT NULL,
  head_revision INTEGER NOT NULL,
  created_by    TEXT REFERENCES agents(id),
  created_at    INTEGER NOT NULL,
  UNIQUE (item_id, path)
);

CREATE TABLE artifact_revisions (
  artifact_id   TEXT NOT NULL REFERENCES artifacts(id),
  revision      INTEGER NOT NULL,
  sha256        TEXT NOT NULL,
  content       TEXT NOT NULL,
  sections_json TEXT NOT NULL,
  tree_json     TEXT,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (artifact_id, revision)
);

CREATE TABLE agents (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  kind            TEXT NOT NULL CHECK (kind IN ('claude','codex','agy','cursor','fake')),
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
  finished_at     INTEGER
);
CREATE INDEX agents_parent ON agents(parent_agent_id);
CREATE INDEX agents_root   ON agents(root_item_id, state);

CREATE TABLE sessions (
  id                  TEXT PRIMARY KEY,
  agent_id            TEXT NOT NULL REFERENCES agents(id),
  attempt             INTEGER NOT NULL,
  generation          INTEGER NOT NULL,
  provider_session_id TEXT,
  token_hash          TEXT NOT NULL UNIQUE,
  tmux_name           TEXT NOT NULL,
  cwd                 TEXT NOT NULL,
  state               TEXT NOT NULL CHECK (state IN
                        ('spawning','running','pause_requested','quiescing','stopping','paused',
                         'interrupted','completed','failed','crashed','cancelled')),
  waiting             INTEGER NOT NULL DEFAULT 0,
  cwd_kind            TEXT NOT NULL CHECK (cwd_kind IN ('neutral','worktree')),
  pause_scope         TEXT CHECK (pause_scope IN ('session','subtree')),
  pause_deadline_at   INTEGER,
  pause_root          INTEGER NOT NULL DEFAULT 0,
  stop_blocks         INTEGER NOT NULL DEFAULT 0,
  needs_compaction_notice INTEGER NOT NULL DEFAULT 0,
  failure_text        TEXT,
  last_seen_at        INTEGER,
  last_wake_at        INTEGER,
  exit_code           INTEGER,
  started_at          INTEGER NOT NULL,
  ended_at            INTEGER,
  UNIQUE (agent_id, generation)
);
CREATE INDEX sessions_state ON sessions(state);

CREATE TABLE worktrees (
  id             TEXT PRIMARY KEY,
  repo_id        TEXT NOT NULL REFERENCES repos(id),
  path           TEXT NOT NULL UNIQUE,
  branch         TEXT,
  detached_sha   TEXT,
  base_ref       TEXT NOT NULL,
  base_sha       TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL REFERENCES agents(id),
  root_item_id   TEXT NOT NULL REFERENCES items(id),
  state          TEXT NOT NULL CHECK (state IN ('active','retained','removed')),
  retained_reason TEXT,
  created_at     INTEGER NOT NULL,
  removed_at     INTEGER
);

CREATE TABLE worktree_reservations (
  worktree_id TEXT NOT NULL REFERENCES worktrees(id),
  agent_id    TEXT NOT NULL REFERENCES agents(id),
  mode        TEXT NOT NULL CHECK (mode IN ('rw','ro')),
  created_at  INTEGER NOT NULL,
  released_at INTEGER,
  PRIMARY KEY (worktree_id, agent_id)
);

CREATE TABLE checkpoints (
  id              TEXT PRIMARY KEY,
  session_id      TEXT NOT NULL REFERENCES sessions(id),
  agent_id        TEXT NOT NULL REFERENCES agents(id),
  item_id         TEXT NOT NULL REFERENCES items(id),
  kind            TEXT NOT NULL CHECK (kind IN
                    ('accepted','progress','blocked','handoff','completed','failed','integrated')),
  attempt         INTEGER NOT NULL,
  resolution      TEXT,
  summary         TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 500),
  next_json       TEXT NOT NULL DEFAULT '[]',
  blockers_json   TEXT NOT NULL DEFAULT '[]',
  git_json        TEXT NOT NULL DEFAULT '[]',
  verify_json     TEXT NOT NULL DEFAULT '[]',
  artifacts_json  TEXT NOT NULL DEFAULT '[]',
  processed_json  TEXT NOT NULL DEFAULT '[]',
  daemon_written  INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL
);
CREATE INDEX checkpoints_item    ON checkpoints(item_id, created_at);
CREATE INDEX checkpoints_session ON checkpoints(session_id, created_at);

CREATE TABLE messages (
  id             TEXT PRIMARY KEY,
  seq            INTEGER NOT NULL UNIQUE,
  kind           TEXT NOT NULL CHECK (kind IN
                   ('assignment','question','answer','finding','control',
                    'relay','approval_result','user_answer','repos_confirmed','assignment_update','digest','advice')),
  wake_class     TEXT NOT NULL DEFAULT 'immediate' CHECK (wake_class IN ('immediate','deferred')),
  priority       INTEGER NOT NULL DEFAULT 1,
  origin         TEXT NOT NULL CHECK (origin IN ('daemon','agent','user_action')),
  from_agent_id  TEXT REFERENCES agents(id),
  from_session_id TEXT REFERENCES sessions(id),
  to_agent_id    TEXT NOT NULL REFERENCES agents(id),
  root_item_id   TEXT NOT NULL REFERENCES items(id),
  item_id        TEXT REFERENCES items(id),
  correlation_id TEXT,
  reply_to       TEXT REFERENCES messages(id),
  request_id     TEXT REFERENCES requests(id),
  payload_json   TEXT NOT NULL,
  state          TEXT NOT NULL CHECK (state IN ('pending','delivered','acked')),
  delivery_count INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  delivered_at   INTEGER,
  acked_at       INTEGER
);
CREATE INDEX messages_inbox ON messages(to_agent_id, state, priority, seq);

CREATE TABLE requests (
  id              TEXT PRIMARY KEY,
  kind            TEXT NOT NULL CHECK (kind IN
                    ('question','confirm_repos','approve_section','approve_plan','approve_report',
                     'accept_epic','accept_fix','close_spike')),
  agent_id        TEXT REFERENCES agents(id),
  session_id      TEXT REFERENCES sessions(id),
  item_id         TEXT NOT NULL REFERENCES items(id),
  artifact_id     TEXT REFERENCES artifacts(id),
  section_id      TEXT,
  section_sha256  TEXT,
  prompt          TEXT NOT NULL CHECK (length(prompt) <= 1000),
  options_json    TEXT NOT NULL DEFAULT '[]',
  state           TEXT NOT NULL CHECK (state IN ('open','approved','changes_requested','answered','withdrawn','stale')),
  confirmed_json  TEXT,
  artifact_revision INTEGER,
  binding_json    TEXT,
  response_text   TEXT,
  responded_via   TEXT CHECK (responded_via IN ('menubar','board','cli')),
  responded_at    INTEGER,
  created_at      INTEGER NOT NULL
);
CREATE INDEX requests_open ON requests(state, created_at);

CREATE TABLE notifications (
  id         TEXT PRIMARY KEY,
  level      TEXT NOT NULL CHECK (level IN ('info','attention','action')),
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL,
  agent_id   TEXT REFERENCES agents(id),
  item_id    TEXT REFERENCES items(id),
  request_id TEXT REFERENCES requests(id),
  dedup_key  TEXT NOT NULL,
  read_at    INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX notifications_unread ON notifications(read_at, created_at);

CREATE TABLE model_catalog (
  agent_kind  TEXT PRIMARY KEY,
  agent_version TEXT NOT NULL,
  models_json TEXT NOT NULL,
  default_model TEXT,
  source      TEXT NOT NULL,
  error       TEXT,
  fetched_at  INTEGER NOT NULL,
  attempted_at INTEGER NOT NULL
);

CREATE TABLE advice (
  id            TEXT PRIMARY KEY,
  session_id    TEXT NOT NULL REFERENCES sessions(id),
  item_id       TEXT NOT NULL REFERENCES items(id),
  advisor_kind  TEXT NOT NULL,
  advisor_model TEXT NOT NULL,
  advisor_effort TEXT,
  question      TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 4000),
  context_path  TEXT,
  context_chars INTEGER,
  state         TEXT NOT NULL CHECK (state IN ('queued','running','answered','failed','timed_out')),
  answer        TEXT,
  error         TEXT,
  duration_ms   INTEGER,
  mode          TEXT NOT NULL DEFAULT 'simulated' CHECK (mode IN ('native','simulated')),
  input_tokens  INTEGER,
  output_tokens INTEGER,
  cache_read_tokens INTEGER,
  cache_write_tokens INTEGER,
  cost_usd      REAL,
  source_request_id TEXT,
  created_at    INTEGER NOT NULL,
  finished_at   INTEGER
);
CREATE INDEX advice_session ON advice(session_id, created_at);
CREATE UNIQUE INDEX advice_native_request ON advice(session_id, source_request_id) WHERE source_request_id IS NOT NULL;

CREATE TABLE usage_snapshots (
  agent_kind  TEXT PRIMARY KEY,
  meters_json TEXT NOT NULL,
  headline_id TEXT,
  source      TEXT NOT NULL,
  error       TEXT,
  fetched_at  INTEGER NOT NULL,
  attempted_at INTEGER NOT NULL
);

CREATE TABLE idempotency (
  caller      TEXT NOT NULL,
  request_id  TEXT NOT NULL,
  tool        TEXT NOT NULL,
  result_json TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (caller, request_id)
);

CREATE TABLE events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  type       TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE kb_docs (
  id            INTEGER PRIMARY KEY,
  slug          TEXT NOT NULL UNIQUE,
  title         TEXT NOT NULL,
  path          TEXT NOT NULL,
  frontmatter_json TEXT NOT NULL DEFAULT '{}',
  content_hash  TEXT NOT NULL,
  superseded_by TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE TABLE kb_chunks (
  id          INTEGER PRIMARY KEY,
  doc_id      INTEGER NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
  chunk_index INTEGER NOT NULL,
  heading     TEXT,
  body        TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  UNIQUE (doc_id, chunk_index)
);
CREATE VIRTUAL TABLE kb_fts USING fts5(heading, body, content='kb_chunks', content_rowid='id');
CREATE TRIGGER kb_fts_ai AFTER INSERT ON kb_chunks BEGIN
  INSERT INTO kb_fts(rowid, heading, body) VALUES (new.id, new.heading, new.body);
END;
CREATE TRIGGER kb_fts_ad AFTER DELETE ON kb_chunks BEGIN
  INSERT INTO kb_fts(kb_fts, rowid, heading, body) VALUES ('delete', old.id, old.heading, old.body);
END;
CREATE TRIGGER kb_fts_au AFTER UPDATE ON kb_chunks BEGIN
  INSERT INTO kb_fts(kb_fts, rowid, heading, body) VALUES ('delete', old.id, old.heading, old.body);
  INSERT INTO kb_fts(rowid, heading, body) VALUES (new.id, new.heading, new.body);
END;
CREATE TABLE kb_vectors (
  chunk_id INTEGER PRIMARY KEY REFERENCES kb_chunks(id) ON DELETE CASCADE,
  model    TEXT NOT NULL,
  dim      INTEGER NOT NULL,
  vec      BLOB NOT NULL
);
