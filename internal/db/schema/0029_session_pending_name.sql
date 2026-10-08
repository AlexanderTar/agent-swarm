-- 0029_session_pending_name.sql: session name the wake tick still has to paste into the agent's pane
-- (TASK-769). NULL = nothing pending; set at launch/resume and on an orchestrator rename.
ALTER TABLE sessions ADD COLUMN pending_name TEXT;
