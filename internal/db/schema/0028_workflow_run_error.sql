-- 0028_workflow_run_error.sql: why a workflow run failed to spawn (BUG-55). NULL = no recorded error.
ALTER TABLE workflow_runs ADD COLUMN error TEXT;
