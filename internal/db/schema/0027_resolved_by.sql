-- 0027_resolved_by.sql: a Draft/Ready root closed as Done because another, Done root resolved it
-- (docs/specs/2026-10-03-resolved-by-close.md). NULL = not closed that way.
ALTER TABLE items ADD COLUMN resolved_by_id TEXT REFERENCES items(id);
