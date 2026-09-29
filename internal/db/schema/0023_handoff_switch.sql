-- 0023_handoff_switch.sql: a handoff may carry a pending agent switch, applied when the
-- successor starts (docs/specs/2026-09-29-menubar-board-handoff.md). '' = no switch.
ALTER TABLE agent_operations ADD COLUMN switch_json TEXT NOT NULL DEFAULT '';
