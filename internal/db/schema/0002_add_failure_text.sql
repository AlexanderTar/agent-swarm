-- Live incident (2026-09-20): the prior version of this column was added by
-- editing 0001_init.sql in place. migrate() applies each schema/*.sql file
-- exactly once, tracked by PRAGMA user_version, so any database that had
-- already run migration 1 never got the column -- LatestSession's SELECT
-- then failed on every call, silently swallowed by agentNodeOut's `if err ==
-- nil`, and every agent's session showed as null (displayed as "Queued") on
-- an already-initialized daemon the moment it picked up a binary built after
-- that edit. Schema changes to an existing table always belong in a new
-- numbered file, never a rewrite of one already shipped.
ALTER TABLE sessions ADD COLUMN failure_text TEXT;
