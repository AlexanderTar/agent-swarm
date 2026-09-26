-- 0016_agent_kind_reason.sql: why a worker's kind/model differs from the
-- user's role default (a user override, a parent's role override, or a
-- usage fallback). NULL means it came from the user's settings.
-- Additive only; no backfill.
ALTER TABLE agents ADD COLUMN kind_reason TEXT;
