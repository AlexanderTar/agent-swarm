-- 0026_bug_reports.sql: local store for swarm_report_bug. Every report is recorded here; a board item
-- is created only when the reporter passes board:true (board_item_key then names it).
CREATE TABLE bug_reports (
  id                  TEXT PRIMARY KEY,           -- bug_<ULID>
  created_at          INTEGER NOT NULL,
  reporter_agent_id   TEXT NOT NULL,
  reporter_agent_name TEXT NOT NULL,
  session_id          TEXT NOT NULL,
  root_item_key       TEXT NOT NULL,
  title               TEXT NOT NULL,
  what_happened       TEXT NOT NULL,
  repro               TEXT NOT NULL DEFAULT '',
  evidence            TEXT NOT NULL DEFAULT '',
  user_said           TEXT NOT NULL DEFAULT '',
  cause               TEXT NOT NULL DEFAULT '',
  area                TEXT NOT NULL DEFAULT '',
  transcript_path     TEXT NOT NULL DEFAULT '',   -- copied path, or "unavailable (<reason>)"
  board_item_key      TEXT                        -- NULL unless board:true
);
CREATE INDEX bug_reports_created ON bug_reports(created_at DESC);
