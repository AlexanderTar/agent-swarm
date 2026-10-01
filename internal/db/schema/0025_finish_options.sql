-- 0025_finish_options.sql: free-form finish options the orchestrator proposes on integrated
-- (docs/specs/2026-10-01-relax-transitions-and-approvals.md). NULL = none.
ALTER TABLE checkpoints ADD COLUMN finish_options_json TEXT; -- integrated only, [{label, description}]
