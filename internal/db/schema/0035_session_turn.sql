-- 0035_session_turn.sql: hook turn state on the session row.
-- turn_open NULL means no hook or paste has spoken yet (pane fallback).
-- 0 closed, 1 open. Written only when the turn opens or closes.
ALTER TABLE sessions ADD COLUMN turn_open INTEGER;
ALTER TABLE sessions ADD COLUMN turn_since INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN turn_close_event TEXT NOT NULL DEFAULT '';
