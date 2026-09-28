-- The daemon can start a spike orchestrator with no Name when a Request is
-- given (spec 2026-09-28): it sets a placeholder title right away and the
-- orchestrator supplies the real one through its first accepted checkpoint.
-- This flag marks an item whose title is still that placeholder.
ALTER TABLE items ADD COLUMN title_pending INTEGER NOT NULL DEFAULT 0;
