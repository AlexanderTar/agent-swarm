-- 0032_web_auth.sql: the single web login (one row) and its cookie sessions for lan and tunnel callers.
-- Times are ms since the epoch like every other table. Session tokens are stored as SHA-256 hashes.
CREATE TABLE web_auth (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  email         TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE TABLE web_sessions (
  token_hash TEXT PRIMARY KEY,
  origin     TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX web_sessions_expires ON web_sessions(expires_at);
