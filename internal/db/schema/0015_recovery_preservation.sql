-- 0015_recovery_preservation.sql: Batch 2 (agent continuity) recovery state.
-- Additive only: three manifest/checkpoint columns on agent_operations and
-- one first-sync marker on sessions. Fresh databases and replays from any
-- earlier version pick these up with defaults; no rebuild, no backfill.

-- Where the predecessor's handoff manifest lives (written atomically under
-- <swarm home>/handoffs/<agent-id>/<operation-id>/manifest.json) and its
-- sha256, so the successor's first sync can name it and the daemon can
-- validate it before marking the operation ready. Empty until written.
ALTER TABLE agent_operations ADD COLUMN manifest_path TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_operations ADD COLUMN manifest_hash TEXT NOT NULL DEFAULT '';

-- The handoff checkpoint the predecessor wrote last (checkpoint binding:
-- manifest first, then checkpoint). Empty until written.
ALTER TABLE agent_operations ADD COLUMN checkpoint_id TEXT NOT NULL DEFAULT '';

-- First swarm_sync of this session (NULL = never synced). Each generation
-- is a new sessions row, so this marks every generation's first sync --
-- the only sync that carries the recovery bundle.
ALTER TABLE sessions ADD COLUMN first_sync_at INTEGER;
