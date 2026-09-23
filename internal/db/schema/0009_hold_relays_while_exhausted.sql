-- 0009_hold_relays_while_exhausted.sql: hold daemon relays while the target's
-- agent kind is confirmed exhausted, flushing one digest on quota reset instead
-- of queueing hundreds of per-event relays (2026-09-23 orchestrator-2 incident:
-- 309 daemon relays acked while claude five_hour sat at 100%).
-- Plain CREATE TABLE: nothing references it yet, so no rebuild is needed.
CREATE TABLE suppressed_relays (
  agent_id    TEXT NOT NULL REFERENCES agents(id),
  event       TEXT NOT NULL,
  count       INTEGER NOT NULL DEFAULT 0,
  first_at    INTEGER NOT NULL,
  last_at     INTEGER NOT NULL,
  sample_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (agent_id, event)
);
CREATE INDEX suppressed_relays_agent ON suppressed_relays(agent_id);
