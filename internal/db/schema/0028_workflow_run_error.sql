-- 0028_workflow_run_error.sql: why a workflow run failed to spawn (BUG-55). NULL = no recorded error.
-- error_fatal = 1 marks a deterministic refusal (e.g. brief too long) that auto-retry cannot fix.
ALTER TABLE workflow_runs ADD COLUMN error TEXT;
ALTER TABLE workflow_runs ADD COLUMN error_fatal INTEGER NOT NULL DEFAULT 0;
