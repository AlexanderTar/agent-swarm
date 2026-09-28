-- 0021_orchestrator_todos.sql: spike step statuses on checkpoints; last todos hash sent per session.
ALTER TABLE checkpoints ADD COLUMN todos_json TEXT;
ALTER TABLE sessions ADD COLUMN todos_sent_hash TEXT;
