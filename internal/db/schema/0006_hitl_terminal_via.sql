-- 0006_hitl_terminal_via.sql: allow 'terminal' in requests.responded_via
CREATE TABLE requests_new (
  id              TEXT PRIMARY KEY,
  kind            TEXT NOT NULL CHECK (kind IN
                    ('question','prompt','blocker','confirm_repos','approve_section',
                     'approve_plan','approve_report','accept_epic','accept_fix','close_spike')),
  is_hitl         INTEGER NOT NULL DEFAULT 0,
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
  responded_via   TEXT CHECK (responded_via IN ('menubar','board','cli','terminal')),
  responded_at    INTEGER,
  created_at      INTEGER NOT NULL
);

INSERT INTO requests_new (
  id, kind, is_hitl, agent_id, session_id, item_id, artifact_id,
  section_id, section_sha256, prompt, options_json, state,
  confirmed_json, artifact_revision, binding_json, response_text,
  responded_via, responded_at, created_at
)
SELECT
  id, kind, is_hitl, agent_id, session_id, item_id, artifact_id,
  section_id, section_sha256, prompt, options_json, state,
  confirmed_json, artifact_revision, binding_json, response_text,
  responded_via, responded_at, created_at
FROM requests;

DROP TABLE requests;

ALTER TABLE requests_new RENAME TO requests;

CREATE INDEX requests_open ON requests(state, created_at);
CREATE INDEX requests_hitl_open ON requests(is_hitl, state, created_at);
