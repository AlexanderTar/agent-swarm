-- 0024_item_overrides.sql: orchestrator gate waivers and forced status, both audited
-- (docs/specs/2026-10-01-relax-transitions-and-approvals.md). NULL = none.
ALTER TABLE items ADD COLUMN waivers_json  TEXT; -- [{gate, reason, agent, at}]
ALTER TABLE items ADD COLUMN override_json TEXT; -- {status, reason, agent, at}
