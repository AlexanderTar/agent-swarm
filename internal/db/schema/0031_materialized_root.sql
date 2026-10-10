-- 0031_materialized_root.sql: marks the root swarm_materialize built from origin_spike_id (BUG-75).
-- A top-level item an orchestrator proposed, or a bug it reported, also sets origin_spike_id and
-- must not close the spike. Backfill: a materialized root carries its spike's copied plan or debug report.
ALTER TABLE items ADD COLUMN materialized INTEGER NOT NULL DEFAULT 0;
UPDATE items SET materialized = 1 WHERE origin_spike_id IS NOT NULL AND EXISTS (
  SELECT 1 FROM artifacts a WHERE a.item_id = items.id AND a.kind IN ('plan', 'debug_report'));
