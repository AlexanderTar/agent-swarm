-- 0025_finish_options.sql: free-form finish options the orchestrator proposes on integrated, and
-- item_merges kind 'kept' for a repo finished without a PR or local merge
-- (docs/specs/2026-10-01-relax-transitions-and-approvals.md).
ALTER TABLE checkpoints ADD COLUMN finish_options_json TEXT; -- integrated only, [{label, description}]; NULL = none

CREATE TABLE item_merges_new (
  id                    TEXT PRIMARY KEY,
  item_id               TEXT NOT NULL REFERENCES items(id),
  integrated_checkpoint TEXT NOT NULL REFERENCES checkpoints(id),
  repo                  TEXT NOT NULL,
  repo_id               TEXT NOT NULL REFERENCES repos(id),
  kind                  TEXT NOT NULL CHECK (kind IN ('pr','local','kept')),
  url                   TEXT,                   -- pr only
  number                INTEGER,                -- pr only
  base                  TEXT NOT NULL,
  head                  TEXT NOT NULL,
  auto_merge            INTEGER NOT NULL DEFAULT 0,
  state                 TEXT NOT NULL CHECK (state IN ('open','merged','closed')),
  checks                TEXT NOT NULL DEFAULT '' CHECK (checks IN ('','pending','passing','failing')),
  merged_sha            TEXT,
  checked_at            INTEGER,
  created_at            INTEGER NOT NULL,
  note                  TEXT,                   -- kept only: what the orchestrator did instead
  UNIQUE (item_id, integrated_checkpoint, repo)
);
INSERT INTO item_merges_new (id, item_id, integrated_checkpoint, repo, repo_id, kind, url, number, base, head,
    auto_merge, state, checks, merged_sha, checked_at, created_at)
  SELECT id, item_id, integrated_checkpoint, repo, repo_id, kind, url, number, base, head,
    auto_merge, state, checks, merged_sha, checked_at, created_at FROM item_merges;
DROP TABLE item_merges;
ALTER TABLE item_merges_new RENAME TO item_merges;
CREATE INDEX item_merges_open ON item_merges(state, kind);
